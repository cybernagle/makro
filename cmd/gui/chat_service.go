package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/naglezhang/makro/internal/agent"
	"github.com/naglezhang/makro/internal/agent/tools"
	"github.com/naglezhang/makro/internal/apns"
	"github.com/naglezhang/makro/internal/config"
	"github.com/naglezhang/makro/internal/llm"
	"github.com/naglezhang/makro/internal/notify"
	"github.com/naglezhang/makro/internal/tmux"
)

type ChatService struct {
	hub         *chatHub
	orch        *agent.Orchestrator
	tc          *tmux.Client
	notifier    *agent.AgentNotifier
	assessor    tools.Assessor
	history     *ChatHistory
	devicestore *DeviceStore
	apns        *apns.Client
	barkKey     string
	barkURL     string
	monitors    map[string]context.CancelFunc
	mu          sync.Mutex
	initErr     string
}

func NewChatService() *ChatService {
	s := &ChatService{monitors: make(map[string]context.CancelFunc)}
	// Initialize chat history immediately (doesn't need orchestrator).
	s.initHistory()
	return s
}

// RegisterDeviceToken upserts an iOS push token (called from /api/device-token).
func (s *ChatService) RegisterDeviceToken(deviceID, token string) {
	if s.devicestore != nil {
		s.devicestore.Upsert(deviceID, token)
	}
}

func (s *ChatService) initHistory() {
	cfg, err := config.Load()
	if err != nil {
		log.Printf("[chat_service] config load for history: %v", err)
		return
	}
	if cfg.ChatHistoryPath == "" {
		return
	}
	// Migrate old markdown format if JSONL doesn't exist yet.
	jsonlPath := strings.TrimSuffix(cfg.ChatHistoryPath, ".md") + ".jsonl"
	if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
		if err := MigrateFromMarkdown(cfg.ChatHistoryPath, jsonlPath); err != nil {
			log.Printf("[chat_service] migration: %v", err)
		}
	}
	history, err := NewChatHistory(jsonlPath)
	if err != nil {
		log.Printf("[chat_service] history init: %v", err)
		return
	}
	s.history = history
	log.Printf("[chat_service] history loaded from %s", jsonlPath)
}

func (s *ChatService) init() {
	if s.orch != nil || s.initErr != "" {
		return
	}

	cfg, err := config.Load()
	if err != nil {
		s.reportError("config load: %v", err)
		return
	}

	if err := cfg.ValidateAPIKey(); err != nil {
		s.reportError("API key: %v", err)
		return
	}

	provider, err := llm.NewProvider(cfg.LLMProvider, cfg.LLMAPIKey, cfg.LLMBaseURL)
	if err != nil {
		s.reportError("provider: %v", err)
		return
	}

	socketPath := detectTmuxSocket()
	tc := tmux.NewClient(socketPath, false)
	if err := tc.Start(context.Background()); err != nil {
		s.reportError("tmux: %v", err)
		return
	}

	hm := agent.NewHookManager()
	assessor := agent.NewSessionAssessor(provider, cfg.LLMModel, cfg.GuardianPrompt)
	cwd, _ := os.Getwd()
	notifier := agent.NewAgentNotifier()

	orch := agent.NewOrchestrator(provider, tc, hm, tools.AllTools(tc, assessor, cwd, notifier))
	orch.SetCommandRegistry(agent.NewCommandRegistry(tc))
	homeDir, _ := os.UserHomeDir()
	skillDirs := []string{
		filepath.Join(homeDir, ".makro", "skills"),
		filepath.Join(".", ".makro", "skills"),
	}
	orch.LoadSkills(skillDirs)
	orch.SetModel(cfg.LLMModel)
	orch.SetMaxContextMessages(cfg.MaxContextMessages)
	orch.SetSystemPrompt(agent.DefaultSystemPrompt())

	notifier.OnSession(func(session, content string) error {
		return tools.DirectSend(tc, session, content)
	})

	// Hook callbacks — same pattern as main.go for TUI.
	notifier.OnAgentStop(func(session, status string) {
		st := status
		if st == "" {
			st = "stopped"
		}
		msg := fmt.Sprintf("Session %s %s", session, st)
		var lastAssistant string
		if out, err := tools.ReadStructuredOutput(tc, session); err == nil && out.LastAssistantMessage != "" {
			lastAssistant = out.LastAssistantMessage
			msg += "\n" + lastAssistant
		}
		s.emit("chat:system", msg)

		// Desktop notification only when Makro is not frontmost AND Bark is not
		// configured (Bark covers both Mac + iPhone, avoids duplicate on Mac).
		const makroBundleID = "com.cybernagle.makro"
		if s.barkKey == "" {
			notifyCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if !notify.IsFrontmost(notifyCtx, makroBundleID) {
				notify.Notify(notifyCtx, "Makro", session, lastAssistant, makroBundleID)
			}
			cancel()
		}

		// iOS push to all registered devices (best-effort, in parallel).
		if s.apns != nil && s.devicestore != nil {
			for _, tok := range s.devicestore.All() {
				go func(token string) {
					pctx, pcancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer pcancel()
					if err := s.apns.Push(pctx, token, "Makro", session, lastAssistant, session); err != nil {
						log.Printf("[chat_service] apns push %s: %v", session, err)
					}
				}(tok)
			}
		}
		// Bark push (bypasses APNs signing, uses Bark app APNs cert).
		if s.barkKey != "" {
			go func() {
				bctx, bcancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer bcancel()
				if err := notify.BarkPush(bctx, s.barkURL, s.barkKey, "Makro", session, lastAssistant); err != nil {
					log.Printf("[chat_service] bark push: %v", err)
				}
			}()
		}
	})

	notifier.OnPermission(func(session string) {
		s.emit("chat:system", fmt.Sprintf("Session %s waiting for permission", session))
		s.StartMonitor(session)
	})

	go notifier.Start(context.Background())

	s.orch = orch
	s.tc = tc
	s.notifier = notifier
	s.assessor = assessor
	log.Printf("[chat_service] initialized provider=%s model=%s", cfg.LLMProvider, cfg.LLMModel)

	// Inject Claude Code Stop/Permission hooks so agent events reach this
	// instance over the socket. Idempotent. The hook command points at this
	// executable (makro-serve), which forwards via the "notify"/"permission"
	// subcommands handled in main.go.
	if exePath, err := os.Executable(); err == nil {
		if err := agent.EnsureStopHook(cfg.ClaudeDir, exePath); err != nil {
			log.Printf("[chat_service] claude stop hook: %v", err)
		}
		if err := agent.EnsurePermissionHook(cfg.ClaudeDir, exePath); err != nil {
			log.Printf("[chat_service] claude permission hook: %v", err)
		}
	}

	// APNs (iOS push). Device-token store always on; APNs client only when key
	// material is configured (disabled otherwise, no-op).
	s.devicestore = NewDeviceStore(filepath.Join(cfg.DataDir, "device_tokens.json"))
	if cfg.APNsKeyPath != "" {
		client, err := apns.NewClient(cfg.APNsKeyPath, cfg.APNsKeyID, cfg.APNsTeamID, cfg.APNsBundleID, cfg.APNsSandbox)
		if err != nil {
			log.Printf("[chat_service] APNs disabled: %v", err)
		} else {
			s.apns = client
			log.Printf("[chat_service] APNs enabled (bundle=%s sandbox=%v)", cfg.APNsBundleID, cfg.APNsSandbox)
		}
	}
	s.barkKey = cfg.BarkKey
	s.barkURL = cfg.BarkURL
	if s.barkURL == "" {
		s.barkURL = "https://api.day.app"
	}
	if s.barkKey != "" {
		log.Printf("[chat_service] Bark push enabled")
	}
}

func (s *ChatService) SendMessage(input string) error {
	if s.orch == nil {
		if s.initErr == "" {
			s.init()
		}
	}
	if s.orch == nil {
		errMsg := "Orchestrator not initialized"
		if s.initErr != "" {
			errMsg = s.initErr
		}
		s.emit("chat:error", errMsg)
		return nil
	}

	// &session: start background monitor.
	if strings.HasPrefix(strings.TrimSpace(input), "&") {
		sessionName := agent.ExtractMonitor(input)
		if sessionName != "" {
			s.StartMonitor(sessionName)
			return nil
		}
		names := s.ListMonitors()
		if len(names) == 0 {
			s.emit("chat:system", "No active monitors.")
		} else {
			s.emit("chat:system", "Active monitors: "+strings.Join(names, ", "))
		}
		return nil
	}

	// @session: switch terminal tab on frontend side.
	if name, _ := agent.ExtractMention(input); name != "" {
		s.emit("chat:switch_tab", name)
	}

	// Persist user message.
	if s.history != nil {
		s.history.Append("user", input)
	}

	// Route to orchestrator (handles @mention, LLM, tools).
	go func() {
		ctx := context.Background()
		ch, err := s.orch.ProcessInput(ctx, input)
		if err != nil {
			s.emit("chat:error", err.Error())
			return
		}

		var assistantText strings.Builder
		for ev := range ch {
			switch ev.Type {
			case agent.EventThinking:
				s.emit("chat:thinking", ev.Content)
			case agent.EventText:
				s.emit("chat:text", ev.Content)
				assistantText.WriteString(ev.Content)
			case agent.EventToolCall:
				data, _ := json.Marshal(map[string]any{
					"tool": ev.ToolName,
					"args": ev.ToolArgs,
				})
				s.emit("chat:tool_call", string(data))
			case agent.EventToolResult:
				data, _ := json.Marshal(map[string]any{
					"tool":   ev.ToolName,
					"result": ev.ToolResult,
				})
				s.emit("chat:tool_result", string(data))
			case agent.EventDone:
				s.emit("chat:done", "")
				if s.history != nil && assistantText.Len() > 0 {
					s.history.Append("assistant", assistantText.String())
				}
			}
		}
	}()
	return nil
}

func (s *ChatService) Cancel() {
	if s.orch != nil {
		s.orch.Cancel()
	}
}

// StartMonitor watches a session: waits for idle, auto-handles confirmations.
func (s *ChatService) StartMonitor(sessionName string) {
	s.mu.Lock()
	if cancel, ok := s.monitors[sessionName]; ok {
		cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.monitors[sessionName] = cancel
	s.mu.Unlock()

	s.emit("chat:system", fmt.Sprintf("Monitoring @%s until idle...", sessionName))

	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.monitors, sessionName)
			s.mu.Unlock()
		}()

		for {
			tool := tools.NewWaitUntilIdleTool(s.tc, s.notifier)
			result, err := tool.Execute(ctx, map[string]any{
				"session_name":    sessionName,
				"timeout_seconds": float64(300),
			})
			if err != nil {
				s.emit("chat:system", fmt.Sprintf("Monitor @%s error: %v", sessionName, err))
				return
			}

			var parsed struct {
				Status string `json:"status"`
			}
			if jsonErr := json.Unmarshal([]byte(result), &parsed); jsonErr != nil {
				s.emit("chat:system", fmt.Sprintf("Monitor @%s done: %s", sessionName, result))
				return
			}

			switch parsed.Status {
			case "idle":
				s.emit("chat:system", fmt.Sprintf("Monitor @%s idle ✓", sessionName))
				return
			case "timeout":
				s.emit("chat:system", fmt.Sprintf("Monitor @%s timeout — still running", sessionName))
				return
			case "agent_dead":
				s.emit("chat:system", fmt.Sprintf("Monitor @%s: agent process exited — please check manually", sessionName))
				return
			case "error":
				s.emit("chat:system", fmt.Sprintf("Monitor @%s error", sessionName))
				return
			case "blocked":
				assessTool := tools.NewAssessConfirmationTool(s.tc, s.assessor)
				assessResult, assessErr := assessTool.Execute(ctx, map[string]any{
					"session_name": sessionName,
				})
				approve := false
				promptReason := ""
				if assessErr == nil {
					var ar struct {
						Decision string `json:"decision"`
						Reason   string `json:"reason"`
					}
					if json.Unmarshal([]byte(assessResult), &ar) == nil {
						approve = ar.Decision == "approve"
						promptReason = ar.Reason
					}
				}
				label := "rejecting"
				if approve {
					label = "approving"
				}
				s.emit("chat:system", fmt.Sprintf("Monitor @%s: auto-%s (%s)", sessionName, label, promptReason))
				resp := tools.NewRespondConfirmationTool(s.tc)
				respResult, respErr := resp.Execute(ctx, map[string]any{
					"session_name": sessionName,
					"approve":      approve,
				})
				if respErr != nil {
					s.emit("chat:system", fmt.Sprintf("Monitor @%s respond error: %v", sessionName, respErr))
					return
				}
				_ = respResult
				continue
			default:
				s.emit("chat:system", fmt.Sprintf("Monitor @%s done: %s", sessionName, result))
				return
			}
		}
	}()
}

// ListMonitors returns names of sessions being monitored.
func (s *ChatService) ListMonitors() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var names []string
	for name := range s.monitors {
		names = append(names, name)
	}
	return names
}

// LoadChatHistory returns persisted chat messages for frontend to render.
func (s *ChatService) LoadChatHistory() []HistoryMessage {
	if s.history == nil {
		return nil
	}
	// Live context: last 50 messages
	msgs, err := s.history.Load(50)
	if err != nil {
		log.Printf("[chat_service] load history: %v", err)
		return nil
	}
	return msgs
}

func (s *ChatService) emit(event string, data string) {
	if s.hub != nil {
		switch event {
		case "chat:thinking":
			s.hub.Emit("thinking", data)
		case "chat:text":
			s.hub.Emit("assistant", data)
		case "chat:tool_call":
			s.hub.Emit("tool_call", data)
		case "chat:tool_result":
			s.hub.Emit("tool_result", data)
		case "chat:done":
			s.hub.Emit("done", "")
		case "chat:error":
			s.hub.Emit("error", data)
		case "chat:system":
			s.hub.Emit("system", data)
		case "chat:switch_tab":
			s.hub.Emit("switch_tab", data)
		}
		return
	}
}

func (s *ChatService) reportError(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	log.Printf("[chat_service] init error: %s", msg)
	s.initErr = msg
}

func detectTmuxSocket() string {
	home, _ := os.UserHomeDir()
	sock := filepath.Join(home, ".makro", "tmux.sock")
	if _, err := os.Stat(sock); err == nil {
		return sock
	}
	return ""
}
