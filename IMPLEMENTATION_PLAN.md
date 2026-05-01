# FingerSaver Implementation Plan

## Dependency Graph

```
config ──────────────────────────────────────────────────┐
llm ─────────────────────────────────────────────────────┤
tmux ──────────────────────┬────────────────────────────┤
                           │                            │
                    ┌──────┴──────┐                     │
                    │             │                     │
                 agent ──────► adapters                 │
                    │             │                     │
                    └──────┬──────┘                     │
                           │                            │
                         tui ◄──────────────────────────┘
                           │
                        main.go
```

- **Leaf packages** (no internal deps): `config`, `llm`, `tmux`
- **Mid packages** (depend on leaves): `agent`, `adapters`
- **Top packages** (depend on mid): `tui`
- **Entry point**: `main.go`

## Parallelism

```
Phase 1: config ████████
                    ├── Phase 2: tmux     ████████████████
                    ├── Phase 3: llm      ████████████████    (parallel with Phase 2)
                    │              ├── Phase 4: agent  ████████████████
                    │              │           ├── Phase 5: adapters ████████████
                    │              │           ├── Phase 6: tui     ████████████████████
                    │              │           │        └── Phase 7: integration ████████
```

---

## Phase 1: Foundation — Config and Go Module Setup

**Goal**: Bootable Go module with config loading, directory scaffolding, and a skeleton entry point.
**Components**: `go.mod`, `main.go`, `internal/config`
**Depends on**: Nothing
**Verification**: `go build` succeeds, `go test ./...` passes, running the binary prints a config-derived message.
**Status**: Complete

### Steps

1. **Initialize Go module**
   - `go mod init github.com/naglezhang/fingersaver`
   - Go 1.26.1+ in go.mod
   - Run `go mod tidy` after adding deps in later steps

2. **Create `internal/config/config.go`**
   - `Config` struct: `LLMProvider`, `LLMModel`, `LLMAPIKey`, `TmuxSocketPath`, `DataDir`, `ChatHistoryPath`, `ClaudeDir`
   - `Load() (*Config, error)`: XDG-compatible, reads `.claude` dir for model preferences, creates `~/.fingersaver/` tree
   - Env var overrides: `FINGERSAVER_LLM_PROVIDER`, `FINGERSAVER_LLM_MODEL`, `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`

3. **Create `internal/config/config_test.go`**
   - Table-driven tests for `Load()` with temp directories
   - Test env var override, `.claude` fallback, directory creation

4. **Create `main.go`** (skeleton)
   - Parse flags, call `config.Load()`, print config summary
   - Signal handling for clean shutdown via `context.Context`

---

## Phase 2: Tmux Control Mode Client

**Goal**: Fully functional tmux control mode client — dedicated server, protocol parser, session state, streaming pane output.
**Components**: `internal/tmux/`
**Depends on**: Phase 1 (config for socket path)
**Verification**: Parser unit tests 90%+. Integration tests with real tmux: start server, create session, read output, kill session, stop server.
**Status**: Complete

### Steps

1. **`internal/tmux/parser.go`** — control mode line parser
   - `NotifType` enum: `NotifOutput`, `NotifSessionChanged`, `NotifWindowAdd`, `NotifWindowClose`, `NotifLayoutChange`, `NotifBegin`, `NotifEnd`, `NotifError`, etc.
   - `Notification` struct: `Type`, `PaneID`, `SessionID`, `WindowID`, `SessionName`, `Data`, `Timestamp`, `Number`
   - `ParseNotification(line string) (Notification, error)` — handles all notification types
   - `DecodeEscape(data string) string` — decodes `\NNN` octal escapes
   - `EncodeEscape(data []byte) string` — encodes for sending

2. **`internal/tmux/parser_test.go`** — table-driven tests for every NotifType, edge cases, escape roundtrip

3. **`internal/tmux/session.go`** — session state mirror
   - `Session`, `Window`, `Pane` structs
   - `StateMirror`: `map[string]*Session`, auto-updates from notification stream
   - `Apply(n Notification) error`, `FindSession(name)`, `ActivePane(session)`

4. **`internal/tmux/session_test.go`** — test Apply with notification sequence simulating real lifecycle

5. **`internal/tmux/commands.go`** — high-level tmux command builders (strings for control mode stdin)
   - `NewSessionCmd`, `KillSessionCmd`, `SwitchClientCmd`, `SendKeysCmd`, `ListSessionsCmd`, `CapturePaneCmd`, etc.

6. **`internal/tmux/commands_test.go`** — verify command builders produce exact strings

7. **`internal/tmux/client.go`** — subprocess lifecycle and I/O
   - `TmuxClient` interface (for mocking): `Start`, `SendCommand`, `Notifications`, `State`, `Stop`
   - `Client` struct: `*exec.Cmd`, stdin/stdout, notification channel goroutine
   - `Start(ctx, socketPath)`: start dedicated tmux server + control mode client
   - `SendCommand(cmd)`: write to tmux stdin
   - `Notifications()`: read-only channel
   - `Stop()`: kill subprocess, clean up socket

8. **`internal/tmux/client_test.go`** — mock subprocess, test channel delivery, context cancellation

9. **`internal/tmux/client_integration_test.go`** (build tag: `//go:build integration`)
   - Full lifecycle with real tmux binary

---

## Phase 3: LLM Provider Abstraction

**Goal**: Multi-provider LLM abstraction — Anthropic + OpenAI with streaming and tool use.
**Components**: `internal/llm/`
**Depends on**: Phase 1 (config for API keys and model selection)
**Verification**: Unit tests 80%+ with mocked providers. Manual test with real API keys.
**Status**: Complete

### Steps

1. **`internal/llm/types.go`** — shared types
   - `Role`, `Message`, `ToolCall`, `ToolResult`, `ToolDefinition`
   - `StreamEvent` variants: `EventTextDelta`, `EventToolCallStart`, `EventToolCallDelta`, `EventDone`, `EventError`
   - `GenerateOptions`: `Model`, `MaxTokens`, `Temperature`, `Tools`, `SystemPrompt`

2. **`internal/llm/provider.go`** — interface and factory
   - `Provider` interface: `Stream(ctx, messages, opts) (<-chan StreamEvent, error)`, `Name()`
   - `NewProvider(name, apiKey) (Provider, error)` factory

3. **`internal/llm/anthropic.go`** — Anthropic implementation with streaming + tool use

4. **`internal/llm/openai.go`** — OpenAI implementation with streaming + tool use

5. **`internal/llm/anthropic_test.go`**, `internal/llm/openai_test.go` — mock HTTP server tests

6. **`internal/llm/provider_test.go`** — factory and registry tests

---

## Phase 4: Agent Orchestrator — Tools and Slash Commands

**Goal**: Orchestrator that routes user input to slash commands or LLM tool calls, executes tools, manages hooks.
**Components**: `internal/agent/`
**Depends on**: Phase 2 (tmux), Phase 3 (llm)
**Verification**: Unit tests for every tool and command 90%+. Integration: natural language "list sessions" → LLM calls tool → result returned.
**Status**: Complete

### Steps

1. **`internal/agent/tools.go`** — tool definitions
   - `Tool` struct, `Param` struct
   - Tools: `ListSessions`, `CreateSession`, `SwitchSession`, `KillSession`, `SendToSession`, `ReadSessionOutput`
   - Each accepts `TmuxClient` interface for testability

2. **`internal/agent/tools_test.go`** — table-driven tests with mocked TmuxClient

3. **`internal/agent/commands.go`** — slash command parser and registry
   - `SlashCommand` struct, `CommandRegistry`
   - Built-in: `/create`, `/switch`, `/kill`, `/list`, `/help`
   - `ParseSlashCommand(input)`, `Execute(ctx, input)`

4. **`internal/agent/commands_test.go`** — test parsing and execution

5. **`internal/agent/hooks.go`** — hook management
   - `HookType` enum: `HookBeforeToolCall`, `HookAfterToolCall`, `HookAgentStop`, `HookPermissionRequest`
   - `HookManager`: register, fire, ordered execution

6. **`internal/agent/hooks_test.go`** — test interception, blocking, ordering

7. **`internal/agent/orchestrator.go`** — central coordinator
   - `Orchestrator` struct: provider, tmuxClient, tools, commands, hooks, messages, systemPrompt
   - `ProcessInput(ctx, input) (<-chan OrchestratorEvent, error)`:
     1. Slash command → execute directly
     2. `@mention` → extract target, prepend context
     3. Otherwise → LLM with tools, stream response, execute tool calls with hooks, feed back
   - `OrchestratorEvent`: `EventText`, `EventToolCall`, `EventToolResult`, `EventDone`

8. **`internal/agent/orchestrator_test.go`** — slash routing, @mention, mock LLM tool call loop

---

## Phase 5: Agent Adapters

**Goal**: Claude Code and Copilot adapters — launch agents in tmux sessions, wire hooks, parse output.
**Components**: `internal/adapters/`
**Depends on**: Phase 2 (tmux), Phase 4 (hooks)
**Verification**: Unit tests with mocked tmux. Integration: adapter launches real agent, hook fires.
**Status**: Complete

### Steps

1. **`internal/adapters/adapter.go`** — common interface
   - `AgentAdapter` interface: `Name`, `Launch`, `SendMessage`, `IsRunning`, `ParseOutput`, `StopConfig`
   - `AgentEvent`, `AgentEventType`, `StopHookConfig`
   - `Registry` with Claude and Copilot pre-registered

2. **`internal/adapters/claude.go`** — Claude Code adapter
   - `Launch()`: `send-keys` with claude CLI + flags
   - Hook wiring: stop hook (`--stop-hook`), permission hook
   - `ParseOutput()`: detect tool use, completion markers

3. **`internal/adapters/copilot.go`** — Copilot adapter
   - Analogous to Claude adapter

4. **`internal/adapters/claude_test.go`**, `internal/adapters/copilot_test.go` — mocked TmuxClient tests

---

## Phase 6: TUI — Split-Pane Bubbletea Application

**Goal**: Split-pane TUI — chat input left, tmux viewer right, both accepting input via Tab focus toggle.
**Components**: `internal/tui/`
**Depends on**: Phase 2 (tmux viewer), Phase 4 (agent chat routing)
**Verification**: Manual launch shows split pane. Chat accepts input. Viewer shows tmux output. Tab toggles focus.
**Status**: Complete

### Steps

1. **`internal/tui/styles.go`** — Lipgloss styles (borders, colors, focus indicators)

2. **`internal/tui/messages.go`** — internal `tea.Msg` types (TmuxNotificationMsg, OrchestratorEventMsg, etc.)

3. **`internal/tui/chat.go`** — chat pane model
   - `ChatModel`: textarea + viewport for message history
   - Submit → orchestrator, AppendMessage, chat history persistence

4. **`internal/tui/viewer.go`** — tmux viewer pane model
   - `ViewerModel`: viewport + per-session output buffers
   - Append tmux output, switch session, forward keystrokes to tmux via `send-keys`

5. **`internal/tui/app.go`** — root model: split-pane layout and dual input routing
   - `AppModel`: chat + viewer + orchestrator + tmuxClient + focus state
   - `Tab` toggles focus between panes
   - FocusChat → keystrokes to chat input
   - FocusViewer → keystrokes to tmux via `send-keys`
   - `View()`: `lipgloss.JoinHorizontal`, `AltScreen = true`
   - Goroutine bridges `tmuxClient.Notifications()` to tea.Msg

6. **`internal/tui/app_test.go`** — focus toggle, message routing, resize, View output

7. **Update `main.go`** — wire config → tmux → llm → hooks → orchestrator → TUI, run `tea.NewProgram(app).Run()`, defer cleanup

---

## Phase 7: Integration, Hooks, and Polish

**Goal**: End-to-end: hooks fire, cross-agent messaging works, chat persists, clean exit.
**Components**: Cross-cutting
**Depends on**: Phases 4, 5, 6
**Verification**: All SPEC.md success criteria met. Integration tests for hook flow.
**Status**: Complete

### Steps

1. **Chat persistence** — `~/.fingersaver/chat.md` in Markdown format, append on every message, load on startup

2. **Hook wiring** — Claude stop hook → `HookAgentStop` → orchestrator relays; permission hook → chat pane approval prompt

3. **Cross-agent messaging** — orchestrator reads output from one agent, parses actionable content with LLM, sends to another

4. **Clean shutdown** — SIGINT → cancel context → Bubbletea quits → tmux stops → socket cleaned → terminal restored

5. **End-to-end integration tests** (build tag: `//go:build integration`)
   - Full workflow: create session, launch agent, send message, switch, kill, shutdown

6. **Polish** — `@mention` tab-completion, session status indicators, lint/format pass, README

---

## Risk Assessment

| Risk | Impact | Likelihood | Mitigation |
|------|--------|------------|------------|
| tmux -CC protocol changes between versions | High | Low | Pin minimum tmux version. Parse defensively — skip unknown notification types. |
| tmux output escape encoding edge cases | Medium | Medium | Exhaustive parser tests. Integration tests with binary data in panes. |
| Bubbletea v2 API instability | Medium | Medium | Pin exact version in go.mod. Adapt to View/Model interface changes. |
| Dual-input routing complexity | High | Medium | Tab-toggle focus model is simplest correct approach. Terminal has one stdin. |
| LLM tool call loops | Medium | Low | Max iterations guard (10 tool calls per message). Token budget. |
| Hook callback mechanism | Medium | Medium | Start with file-based hooks (monitored file). Simpler than named pipes. |
| PTY management | Medium | Low | `github.com/creack/pty`. tmux handles its own PTYs; we only manage control mode stdin/stdout. |
