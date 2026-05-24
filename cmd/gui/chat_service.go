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

	"github.com/naglezhang/makro/internal/agent"
	"github.com/naglezhang/makro/internal/agent/tools"
	"github.com/naglezhang/makro/internal/config"
	"github.com/naglezhang/makro/internal/llm"
	"github.com/naglezhang/makro/internal/tmux"
	"github.com/wailsapp/wails/v3/pkg/application"
)

type ChatService struct {
	app      *application.App
	orch     *agent.Orchestrator
	tc       *tmux.Client
	notifier *agent.AgentNotifier
	assessor tools.Assessor
	history  *ChatHistory
	monitors map[string]context.CancelFunc
	mu       sync.Mutex
	initErr  string
}

func NewChatService(app *application.App) *ChatService {
	return &ChatService{app: app, monitors: make(map[string]context.CancelFunc)}
}

func (s *ChatService) SetApp(app *application.App) {
	s.app = app
}

func (s *ChatService) Startup(ctx application.Context) {
	s.init()
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
		if out, err := tools.ReadStructuredOutput(tc, session); err == nil && out.LastAssistantMessage != "" {
			msg += "\n" + out.LastAssistantMessage
		}
		s.emit("chat:system", msg)
	})

	notifier.OnPermission(func(session string) {
		s.emit("chat:system", fmt.Sprintf("Session %s waiting for permission", session))
		s.StartMonitor(session)
	})

	go notifier.Start(context.Background())

	// Chat history persistence.
	if cfg.ChatHistoryPath != "" {
		if history, err := NewChatHistory(cfg.ChatHistoryPath); err == nil {
			s.history = history
		} else {
			log.Printf("[chat_service] history init: %v", err)
		}
	}

	s.orch = orch
	s.tc = tc
	s.notifier = notifier
	s.assessor = assessor
	log.Printf("[chat_service] initialized provider=%s model=%s", cfg.LLMProvider, cfg.LLMModel)
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
	msgs, err := s.history.Load()
	if err != nil {
		log.Printf("[chat_service] load history: %v", err)
		return nil
	}
	return msgs
}

func (s *ChatService) emit(event string, data string) {
	if s.app != nil {
		s.app.Event.Emit(event, data)
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
