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
	"github.com/naglezhang/makro/internal/usage"
)

type ChatService struct {
	hub               *chatHub
	orch              *agent.Orchestrator
	tc                *tmux.Client
	notifier          *agent.AgentNotifier
	assessor          tools.Assessor
	history           *ChatHistory
	devicestore       *DeviceStore
	usageStore        *usage.Store
	highCostModels    []string
	usageQuota5h      int64
	claudeProjectsDir string
	apns              *apns.Client
	barkKey           string
	barkURL           string
	monitors          map[string]context.CancelFunc
	mu                sync.Mutex
	initErr           string
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

// ApplySessionState fills Working + Unread on each session from the notifier
// so /api/sessions carries tab state. Safe to call before init completes
// (notifier nil → no-op, sessions still return with active only).
func (s *ChatService) ApplySessionState(sessions []Session) {
	if s.notifier == nil {
		return
	}
	for i := range sessions {
		sessions[i].Working = s.notifier.Working(sessions[i].Name)
		sessions[i].Unread = s.notifier.Unread(sessions[i].Name)
	}
}

// MarkSessionViewed clears the unread badge for a session (called when the
// user switches to / opens it) and broadcasts the cleared state.
func (s *ChatService) MarkSessionViewed(session string) {
	if s.notifier == nil {
		return
	}
	s.notifier.ClearUnread(session)
	s.emitSessionState(session)
}

// UsageStats returns windowed usage stats, optionally filtered by session/
// source/model. Nil-safe when tracking is disabled.
func (s *ChatService) UsageStats(session, source, model string, hours int) (*usage.Stats, error) {
	if s.usageStore == nil {
		return &usage.Stats{ByModel: map[string]usage.ModelStats{}, BySource: map[string]usage.ModelStats{}, BySession: map[string]usage.ModelStats{}}, nil
	}
	return s.usageStore.Stats(usage.Filter{Session: session, Source: source, Model: model}, hours, s.highCostModels, s.usageQuota5h)
}

// UsageDiagnostics returns duplicate/frequent/ineffective patterns.
func (s *ChatService) UsageDiagnostics(session string) (*usage.Diagnostics, error) {
	if s.usageStore == nil {
		return &usage.Diagnostics{}, nil
	}
	return s.usageStore.Diagnostics(session)
}

// UsageTimeline returns usage buckets (granMin-minute granularity, default hourly)
// over the last `hours`, optionally filtered by session/source/model.
func (s *ChatService) UsageTimeline(session, source, model string, hours, granMin int) ([]usage.TimelinePoint, error) {
	if s.usageStore == nil {
		return nil, nil
	}
	return s.usageStore.Timeline(usage.Filter{Session: session, Source: source, Model: model}, hours, granMin)
}

// UsageExport returns raw usage rows for CSV/detail download.
func (s *ChatService) UsageExport(session, source, model string, hours int) ([]usage.ExportRow, error) {
	if s.usageStore == nil {
		return nil, nil
	}
	return s.usageStore.Export(usage.Filter{Session: session, Source: source, Model: model}, hours)
}

// usageIngestLoop ingests Claude Code transcript usage immediately, then every
// minute. Mapped sessions (SessionStart hook) are attributed to their tmux name;
// the fallback project scan attributes already-running sessions by cwd basename.
// Goroutine lives for the process lifetime.
func (s *ChatService) usageIngestLoop() {
	ingest := func() {
		s.usageStore.IngestTranscripts()
		if s.claudeProjectsDir != "" {
			s.usageStore.IngestProjectTranscripts(s.claudeProjectsDir)
		}
	}
	ingest()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for range t.C {
		ingest()
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

	// Prompt-usage tracking (SQLite). Best-effort: a failure logs and disables
	// tracking rather than blocking the orchestrator.
	if usageStore, uerr := usage.Open(filepath.Join(cfg.DataDir, "prompt_usage.db")); uerr != nil {
		log.Printf("[chat_service] usage store: %v", uerr)
	} else {
		orch.SetUsageStore(usageStore)
		s.usageStore = usageStore
	}
	// Claude Code transcript projects dir — fallback ingestion attributes
	// already-running sessions by their cwd basename.
	s.claudeProjectsDir = filepath.Join(cfg.ClaudeDir, "projects")

	notifier.OnSession(func(session, content string) error {
		return tools.DirectSend(tc, session, content)
	})

	// Hook callbacks — same pattern as main.go for TUI.
	notifier.OnAgentStop(func(session, status string) {
		// working=false and unread++ already applied in the notifier; broadcast.
		s.emitSessionState(session)

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

	// OnAgentStart fires on Claude Code's UserPromptSubmit hook — the session's
	// agent has begun a turn. The working flag is set in the notifier before
	// this callback fires; broadcast the new state to connected clients.
	notifier.OnAgentStart(func(session string) {
		log.Printf("[chat_service] session %s started working", session)
		s.emitSessionState(session)
	})

	// Map Claude Code sessions → tmux sessions so transcript usage can be
	// attributed. Fired by the SessionStart hook (makro claude-start).
	notifier.OnClaudeSession(func(claudeID, tmuxSession, transcriptPath, cwd string) {
		if s.usageStore != nil {
			s.usageStore.RecordClaudeSession(claudeID, tmuxSession, transcriptPath, cwd)
		}
	})

	go notifier.Start(context.Background())

	// Periodic Claude Code transcript ingestion (token usage) — immediate
	// backfill, then every minute. Zero API cost: reads local transcript files.
	if s.usageStore != nil {
		go s.usageIngestLoop()
	}

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
		if err := agent.EnsureStartHook(cfg.ClaudeDir, exePath); err != nil {
			log.Printf("[chat_service] claude start hook: %v", err)
		}
		if err := agent.EnsureClaudeStartHook(cfg.ClaudeDir, exePath); err != nil {
			log.Printf("[chat_service] claude session-start hook: %v", err)
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
	s.highCostModels = cfg.HighCostModels
	s.usageQuota5h = cfg.UsageQuota5h
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
		case "chat:session_state":
			s.hub.Emit("session_state", data)
		}
		return
	}
}

// emitSessionState pushes a {session, working, unread} snapshot over the chat
// WS so every connected client (Electron + iOS) updates its tab/card state in
// real time. Called on agent_start, agent_stop, and when a session is viewed.
func (s *ChatService) emitSessionState(session string) {
	if s.notifier == nil {
		return
	}
	data, _ := json.Marshal(map[string]any{
		"session": session,
		"working": s.notifier.Working(session),
		"unread":  s.notifier.Unread(session),
	})
	s.emit("chat:session_state", string(data))
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
