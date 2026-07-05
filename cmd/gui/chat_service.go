package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/naglezhang/makro/internal/agent"
	"github.com/naglezhang/makro/internal/agent/tools"
	"github.com/naglezhang/makro/internal/apns"
	"github.com/naglezhang/makro/internal/brain"
	"github.com/naglezhang/makro/internal/config"
	"github.com/naglezhang/makro/internal/llm"
	"github.com/naglezhang/makro/internal/notify"
	"github.com/naglezhang/makro/internal/role"
	"github.com/naglezhang/makro/internal/tmux"
	"github.com/naglezhang/makro/internal/usage"
)

// stagedPlan is a plan the assistant proposed during a voice call, awaiting
// the user's confirmation before it is dispatched. Session = the coding-agent
// session that will execute; Summary = one-line "what" (shown/spoken); Brief =
// the concrete instruction text actually sent to that session.
type stagedPlan struct {
	Session string `json:"session"`
	Summary string `json:"summary"`
	Brief   string `json:"brief"`
}

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
	zcodeDBPath       string
	apns              *apns.Client
	barkKey           string
	barkURL           string
	monitors          map[string]context.CancelFunc
	mu                sync.Mutex
	initErr           string

	// Voice-call phase state for the discuss → propose → dispatch flow.
	// callActive is toggled by iOS on call start/end (POST /api/chat/call).
	// stagedPlan is non-nil between the assistant proposing a plan and the user
	// confirming/denying it; while set, dispatch tools are blocked by the
	// voice-call-dispatch-gate hook, and dispatch happens deterministically via
	// ConfirmPlan → tools.DirectSend (which bypasses the hook).
	callActive bool
	callMode   CallMode
	stagedPlan *stagedPlan

	// capture is the brain capture sink. nil until init() runs (and stays nil
	// when cfg.Brain.CaptureEnabled is false). SendMessage and the OnCapture
	// notifier callback both route through it.
	capture *brain.CaptureSink
	// brain is the proactive brain instance (P1). nil when cfg.Brain.Enabled
	// is false or inbox open failed. Powers /brain wake + /inbox commands.
	brain      *brain.Brain
	brainInbox *brain.InboxStore
}

func NewChatService() *ChatService {
	s := &ChatService{monitors: make(map[string]context.CancelFunc), callMode: ModePlan}
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

// usageIngestLoop ingests Claude Code transcript usage + ZCode's model_usage,
// immediately then every minute. Mapped Claude sessions (SessionStart hook) are
// attributed to their tmux name; the fallback project scan attributes
// already-running sessions by cwd basename; ZCode calls are attributed by their
// session's working-directory basename (same convention, so costs align by
// project across sources). Goroutine lives for the process lifetime.
func (s *ChatService) usageIngestLoop() {
	ingest := func() {
		s.usageStore.IngestTranscripts()
		if s.claudeProjectsDir != "" {
			s.usageStore.IngestProjectTranscripts(s.claudeProjectsDir)
		}
		if s.zcodeDBPath != "" {
			s.usageStore.IngestZCode(s.zcodeDBPath)
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

	// Use the user's DEFAULT tmux socket (no -S) — same socket the desktop
	// session list (TmuxService / tmuxArgs) and the user's daily tmux server
	// use. Previously this passed detectTmuxSocket() (~/.makro/tmux.sock),
	// which made the agent's tools (list_sessions, send_to_session, etc.) query
	// a different, empty socket than the one the desktop UI shows — so the
	// agent would answer "no sessions" even while 7 were visible. An empty
	// socketPath makes tmux.Client omit -S and hit the default socket.
	tc := tmux.NewClient("", false)
	if err := tc.Start(context.Background()); err != nil {
		s.reportError("tmux: %v", err)
		return
	}

	hm := agent.NewHookManager()
	assessor := agent.NewSessionAssessor(provider, cfg.LLMModel, cfg.GuardianPrompt)
	cwd, _ := os.Getwd()
	notifier := agent.NewAgentNotifier()

	orch := agent.NewOrchestrator(provider, tc, hm, tools.AllTools(tc, assessor, cwd, notifier))
	cmdRegistry := agent.NewCommandRegistry(tc)
	orch.SetCommandRegistry(cmdRegistry)
	homeDir, _ := os.UserHomeDir()
	skillDirs := []string{
		filepath.Join(homeDir, ".makro", "skills"),
		filepath.Join(".", ".makro", "skills"),
	}
	orch.LoadSkills(skillDirs)

	// Load A-level roles (spec: role-based routing). Missing file is fine —
	// routing is simply disabled. Project-local overrides user-global, same
	// convention as skills. Later files override earlier ones.
	rolePaths := []string{
		filepath.Join(homeDir, ".makro", "roles.toml"),
		filepath.Join(".", ".makro", "roles.toml"),
	}
	var allRoles []role.Role
	for _, p := range rolePaths {
		rs, err := role.LoadFile(p)
		if err != nil {
			log.Printf("[gui] warning: roles config %s: %v", p, err)
		}
		allRoles = append(allRoles, rs...)
	}
	if len(allRoles) > 0 {
		orch.SetRoles(role.NewStore(allRoles))
	}
	orch.SetModel(cfg.LLMModel)
	orch.SetMaxContextMessages(cfg.MaxContextMessages)
	orch.SetSystemPrompt(agent.DefaultSystemPrompt())

	// Voice-call gate: while a call is active, refuse tools per the current
	// mode. Chat mode blocks ALL tools (pure conversation). Plan/Query block the
	// mutating tools (send_to_session etc.) so the assistant can't push content
	// into a session itself — in plan mode it proposes a plan instead and
	// dispatch happens deterministically via ConfirmPlan → SendConfirmed, which
	// bypasses this hook entirely.
	orch.Hooks().Register(agent.Hook{
		Type: agent.HookBeforeToolCall,
		Name: "voice-call-dispatch-gate",
		Handler: func(ctx context.Context, payload any) (any, error) {
			s.mu.Lock()
			active := s.callActive
			mode := s.callMode
			s.mu.Unlock()
			if !active {
				return agent.BeforeToolCallResult{}, nil
			}
			cfg := callModeConfigs[mode]
			name := ""
			if p, ok := payload.(agent.BeforeToolCallPayload); ok {
				name = p.Name
			}
			if cfg.BlockAllTools {
				return agent.BeforeToolCallResult{Block: true, Reason: "闲聊模式下不使用工具，直接对话即可。"}, nil
			}
			if voiceCallBlockedTools[name] {
				return agent.BeforeToolCallResult{
					Block:  true,
					Reason: "语音通话中不能直接派发；请用 ```plan 块提议并等用户确认。",
				}, nil
			}
			return agent.BeforeToolCallResult{}, nil
		},
	})

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
	// ZCode's own usage DB — ingested alongside Claude Code transcripts.
	// Resolved from the user home dir directly (ZCode doesn't share Claude's
	// config root) so a custom claude_dir can't mis-resolve it.
	s.zcodeDBPath = usage.ZCodeDBPath()

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

	// Brain capture sink (P0). Wires the memory-client-backed sink and registers
	// OnCapture so messages forwarded by the UserPromptSubmit hook (makro
	// capture) reach memory-cli. The sink is async and never blocks the socket
	// handler. Disabled entirely when cfg.Brain.CaptureEnabled is false.
	memClient := brain.NewClient(cfg.Brain.MemoryEndpoint, cfg.Brain.MemoryAPIKey, cfg.Brain.MemoryCLIPath, cfg.DataDir)
	s.capture = brain.NewCaptureSink(cfg.Brain, memClient)
	notifier.OnCapture(func(session, prompt, cwd string) {
		// source=claude: the message arrived via the Claude UserPromptSubmit
		// hook (not typed in makro's own chat pane).
		s.capture.Capture(brain.SourceClaude, session, prompt, cwd)
	})

	go notifier.Start(context.Background())

	// Brain (P1): build it over the same memory client as capture, open the
	// inbox, register /brain wake + /inbox commands, run the cron loop. The
	// pusher delivers proposals via the chat WS hub (chat:system) + Bark push.
	if cfg.Brain.Enabled {
		inbox, berr := brain.Open(filepath.Join(cfg.DataDir, "brain", "inbox.db"))
		if berr != nil {
			log.Printf("[chat_service] brain inbox open failed: %v", berr)
		} else {
			s.brainInbox = inbox
			proposer := brain.NewProposer(provider, cfg.LLMModel, "")
			s.brain = brain.NewBrain(cfg.Brain, memClient, proposer, inbox, &guiBrainPusher{
				emit:    s.emit,
				barkKey: cfg.BarkKey,
				barkURL: cfg.BarkURL,
			})
			brain.RegisterCommands(agentCmdRegistrar{cr: cmdRegistry}, s.brain)
			go s.brain.Run(context.Background())
			log.Printf("[chat_service] brain started (cron=%s)", cfg.Brain.CronTime)
		}
	}

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
		if err := agent.EnsureUserPromptCaptureHook(cfg.ClaudeDir, exePath); err != nil {
			log.Printf("[chat_service] claude capture hook: %v", err)
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

// CallMode selects the voice-call interaction style. Each mode is a backend
// config (prefix + tool gate + whether plans are staged) applied to voice
// turns; iOS picks the mode in a dropdown and sends it on call start / switch.
// Adding a mode = add a const + a callModeConfigs entry + an iOS option.
type CallMode string

const (
	ModeChat  CallMode = "chat"  // 闲聊: pure conversation, no tools, no propose/dispatch.
	ModePlan  CallMode = "plan"  // 落实: discuss → propose → confirm → dispatch (default).
	ModeQuery CallMode = "query" // 查询: read-only tools to answer state questions, no dispatch.
)

// parseCallMode maps the iOS-sent string to a CallMode. Unknown → ModePlan.
func parseCallMode(mode string) CallMode {
	switch CallMode(mode) {
	case ModeChat, ModePlan, ModeQuery:
		return CallMode(mode)
	}
	return ModePlan
}

// callModeConfig is the per-mode behavior.
//   - Prefix is prepended to every voice turn (style + the mode's rules).
//   - BlockAllTools true ⇒ the dispatch gate refuses EVERY tool (chat = pure
//     LLM, fastest/cheapest). When false, only the mutating tools in
//     voiceCallBlockedTools are refused — read/observe tools stay allowed
//     (plan's discuss phase + query).
//   - StagePlans true ⇒ the assistant's ```plan blocks are parsed for the
//     confirm/dispatch flow (plan only).
//
// In every mode, dispatch to a session happens ONLY deterministically via
// ConfirmPlan → SendConfirmed, which bypasses the gate — and ConfirmPlan only
// fires from a staged plan, which only exists in plan mode.
type callModeConfig struct {
	Prefix        string
	BlockAllTools bool
	StagePlans    bool
}

// planModePrefix is the 落实 (plan) mode protocol: discuss, then propose a
// structured plan, then dispatch only after the user confirms.
const planModePrefix = "[语音通话模式·落实] 用简洁口语回答：不用表格、代码块或 markdown 符号；可用「第一、第二」短要点；尽量短以节省语音额度。\n" +
	"本次通话分三阶段，你只能在前两阶段行动：\n" +
	"1) 讨论：澄清、细化、深化用户想法。不要调用 send_to_session / relay_message / create_session（会被拦截）。\n" +
	"2) 提议：当任务清晰且用户想做时，先用一句话口述计划，然后在回复【末尾】输出一个计划块，格式严格如下，输出后停下并问「要我落实吗？」：\n" +
	"```plan\n{\"session\":\"<真实session名>\",\"summary\":\"<一句话做什么>\",\"brief\":\"<给该session的可执行指令>\"}\n```\n" +
	"3) 派发：由系统在用户确认后自动执行，你绝不自己派发。\n" +
	"session 名必须先用 list_sessions 查真实结果；brief 要具体、可直接执行。确认前不得派发。\n\n"

// chatModePrefix is the 闲聊 (chat) mode: casual conversation, no tools, no
// propose/dispatch — "说完就答".
const chatModePrefix = "[语音通话模式·闲聊] 自然轻松地口语聊天，像朋友。不要调用任何工具，不要提议落实或派发。不用表格/代码/markdown，简短温暖，说完即答。\n\n"

// queryModePrefix is the 查询 (query) mode: answer questions about session or
// project state using read-only tools; never dispatch.
const queryModePrefix = "[语音通话模式·查询] 用简洁口语回答用户关于 session 或项目状态的问题。可用只读工具查真实状态：list_sessions（有哪些 session）、read_session_output / read_structured_output（某个 session 在干什么、有没有报错）。不要派发任务、不要提议落实、不要调用 send_to_session。不用表格/代码/markdown，简短。\n\n"

// callModeConfigs maps each mode to its behavior. Adding a mode = add a row.
var callModeConfigs = map[CallMode]callModeConfig{
	ModeChat:  {Prefix: chatModePrefix, BlockAllTools: true, StagePlans: false},
	ModePlan:  {Prefix: planModePrefix, BlockAllTools: false, StagePlans: true},
	ModeQuery: {Prefix: queryModePrefix, BlockAllTools: false, StagePlans: false},
}

// planBlockRe extracts the JSON object inside a ```plan fenced block from the
// assistant's streamed reply. Non-greedy up to the closing fence.
var planBlockRe = regexp.MustCompile("(?s)```plan\\s*(.*?)\\s*```")

// voiceCallBlockedTools are the tools the voice-call-dispatch-gate refuses while
// a call is active: anything that pushes content into (or auto-answers) a
// coding-agent session. Read/observe tools stay allowed so discussion is grounded.
var voiceCallBlockedTools = map[string]bool{
	"send_to_session":      true,
	"relay_message":        true,
	"create_session":       true,
	"respond_confirmation": true,
}

// isConfirmPhrase reports whether a recognized utterance is a clear confirmation
// of a staged plan. Used only when a plan is already staged (i.e. right after
// the assistant asked 「要我落实吗？」), so a liberal yes-set is safe. Ambiguous
// input is treated as "not confirmed" → returns to discussion.
func isConfirmPhrase(s string) bool {
	for _, k := range []string{"确认", "落实", "派发", "执行吧", "发吧", "就这样", "yes", "confirm", "confirmed", "approved", "go ahead", "do it"} {
		if strings.Contains(s, k) {
			return true
		}
	}
	// Terse affirmative, only when the whole utterance is short — so 「对」/「好的」
	// as a standalone yes right after a propose, not mid-sentence in discussion.
	if utf8.RuneCountInString(s) <= 6 {
		for _, k := range []string{"可以", "好的", "好了", "对的", "是的", "好", "对", "行", "没问题", "ok", "okay", "sure"} {
			if strings.Contains(s, k) {
				return true
			}
		}
	}
	return false
}

func (s *ChatService) SendMessage(input string, voice bool) error {
	// Capture the user's raw message before voice-prefixing and orchestrator
	// routing. The sink never blocks (async channel) and is nil-safe. We capture
	// the original text, not the voice-prefixed variant — the prefix is a TTS
	// instruction, not user intent.
	if s.capture != nil && strings.TrimSpace(input) != "" {
		s.capture.Capture(brain.SourceMakro, "", input, "")
	}
	// Voice-call confirm gate: if a plan is staged and this is a voice turn,
	// interpret the utterance as a confirm / amend rather than fresh input. A
	// clear confirm dispatches (and we stop — no orchestrator round-trip);
	// anything else drops the staged plan and routes the turn as continued
	// discussion. The on-screen confirm button is the unambiguous path.
	if voice && s.handleVoiceConfirmIntent(input) {
		return nil
	}

	if voice {
		s.mu.Lock()
		mode := s.callMode
		s.mu.Unlock()
		input = callModeConfigs[mode].Prefix + input
	}
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
				// Voice-call: scan the reply for a proposed ```plan and stage it
				// for user confirmation (flips phase to "proposed"). Only plan
				// mode stages plans; chat/query never do.
				if voice {
					s.mu.Lock()
					stage := callModeConfigs[s.callMode].StagePlans
					s.mu.Unlock()
					if stage {
						s.maybeStagePlan(assistantText.String())
					}
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

// ── Voice-call discuss → propose → dispatch ──

// handleVoiceConfirmIntent runs at the top of a voice turn. If a plan is staged
// (phase == proposed), it interprets the utterance: a clear confirm dispatches
// the plan and returns handled=true (no orchestrator round-trip); anything else
// drops the staged plan and returns handled=false so the turn routes as continued
// discussion. Returns false immediately when no plan is staged.
func (s *ChatService) handleVoiceConfirmIntent(text string) bool {
	s.mu.Lock()
	plan := s.stagedPlan
	s.mu.Unlock()
	if plan == nil {
		return false
	}
	if isConfirmPhrase(strings.ToLower(strings.TrimSpace(text))) {
		s.ConfirmPlan()
		return true
	}
	// Not a clear confirm → treat as amendment / continued discussion: clear the
	// staged plan so a future propose re-stages fresh, then route normally.
	s.clearStagedPlan()
	s.emit("chat:phase", "discuss")
	return false
}

// maybeStagePlan scans a voice-call assistant reply for a ```plan block. On a
// valid one it stores it as the pending plan and emits chat:plan + chat:phase
// so clients (iOS / desktop) surface a confirm affordance.
func (s *ChatService) maybeStagePlan(text string) {
	m := planBlockRe.FindStringSubmatch(text)
	if m == nil {
		return
	}
	var p stagedPlan
	if err := json.Unmarshal([]byte(strings.TrimSpace(m[1])), &p); err != nil {
		return
	}
	if p.Session == "" || p.Brief == "" {
		return
	}
	s.mu.Lock()
	s.stagedPlan = &p
	s.mu.Unlock()
	data, _ := json.Marshal(map[string]any{
		"session": p.Session,
		"summary": p.Summary,
		"brief":   p.Brief,
	})
	s.emit("chat:plan", string(data))
	s.emit("chat:phase", "proposed")
}

// ConfirmPlan dispatches the staged plan to its session deterministically via
// DirectSend (bypasses the voice-call-dispatch-gate hook), then clears state.
// Called by POST /api/chat/confirm and by a voice confirm utterance.
func (s *ChatService) ConfirmPlan() {
	s.mu.Lock()
	plan := s.stagedPlan
	s.stagedPlan = nil
	s.mu.Unlock()
	if plan == nil {
		return
	}
	if s.tc == nil {
		s.emit("chat:error", "派发失败: 后端未就绪")
		return
	}
	// Deterministic dispatch with first-send Enter-loss recovery: if claude
	// just rendered its welcome screen and swallowed the Enter, SendConfirmed
	// detects the missed submit and resends Enter once.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := tools.SendConfirmed(ctx, s.tc, s.notifier, plan.Session, plan.Brief); err != nil {
		s.emit("chat:error", "派发失败: "+err.Error())
		return
	}
	s.emit("chat:dispatched", plan.Session)
	s.emit("chat:phase", "discuss")
	s.emit("chat:system", fmt.Sprintf("已派发给 @%s ✓", plan.Session))
}

// DenyPlan drops the staged plan and returns to discussion. Called by POST
// /api/chat/deny and by the on-screen cancel button.
func (s *ChatService) DenyPlan() {
	s.clearStagedPlan()
	s.emit("chat:phase", "discuss")
}

func (s *ChatService) clearStagedPlan() {
	s.mu.Lock()
	s.stagedPlan = nil
	s.mu.Unlock()
}

// SendToSession sends text to a coding-agent session with first-send Enter-loss
// recovery (the same tools.SendConfirmed the send_to_session tool uses). Used by
// the kanban send-to-session HTTP path so it shares the tool's reliability
// instead of the fire-and-forget sendToTmuxSession helper — which silently lost
// the Enter on the first send to a freshly-rendered agent. Falls back to the
// legacy send when the backend/notifier isn't initialized.
func (s *ChatService) SendToSession(session, text string) error {
	if s.tc == nil || s.notifier == nil {
		return sendToTmuxSession(session, text)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return tools.SendConfirmed(ctx, s.tc, s.notifier, session, text)
}

// SetCallActive toggles voice-call mode and sets the interaction mode (called by
// POST /api/chat/call with {active, mode}). On any transition the staged plan is
// cleared; on start the phase resets to discuss. Mid-call mode switches reuse
// this with active=true + the new mode.
func (s *ChatService) SetCallActive(active bool, mode string) {
	s.mu.Lock()
	s.callActive = active
	s.callMode = parseCallMode(mode)
	s.stagedPlan = nil
	s.mu.Unlock()
	if active {
		s.emit("chat:phase", "discuss")
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
		case "chat:plan":
			s.hub.Emit("plan", data)
		case "chat:phase":
			s.hub.Emit("phase", data)
		case "chat:dispatched":
			s.hub.Emit("dispatched", data)
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

// CloseBrain stops the brain loop and closes the inbox, called on shutdown.
func (s *ChatService) CloseBrain() {
	if s.brain != nil {
		s.brain.Stop()
	}
	if s.brainInbox != nil {
		s.brainInbox.Close()
	}
}

// agentCmdRegistrar adapts *agent.CommandRegistry to brain.CommandRegistrar.
// Lives in the composition root so agent and brain stay decoupled at L2.
type agentCmdRegistrar struct {
	cr *agent.CommandRegistry
}

func (a agentCmdRegistrar) Register(spec brain.CommandSpec) {
	a.cr.Register(&agent.SlashCommand{
		Name:        spec.Name,
		Usage:       spec.Usage,
		Description: spec.Description,
		Execute:     spec.Execute,
	})
}

// guiBrainPusher implements brain.Pusher for the GUI. Delivers a proposal via
// the chat WS hub (chat:system event → frontend renders it) + Bark push.
// Skip notices are chat-only (no phone buzz for "nothing happened").
type guiBrainPusher struct {
	emit    func(event string, data string)
	barkKey string
	barkURL string
}

func (p *guiBrainPusher) Push(localID int64, prop brain.InboxProposal) {
	text := brain.FormatProposalForChat(prop)
	if p.emit != nil {
		p.emit("chat:system", text)
	}
	if prop.Status == "skip" || p.barkKey == "" {
		return
	}
	go func() {
		barkURL := p.barkURL
		if barkURL == "" {
			barkURL = "https://api.day.app"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		title := fmt.Sprintf("🧠 proposal [#%d] %s", localID, prop.Title)
		if err := notify.BarkPush(ctx, barkURL, p.barkKey, title, "brain", prop.Body); err != nil {
			log.Printf("[chat_service] brain bark push failed: %v", err)
		}
	}()
}
