# FingerSaver Task Breakdown

Task dependency order. Each task is completable in a single focused session.

---

## Phase 1: Foundation

- [x] T01: Initialize Go module and project skeleton
  - Acceptance: `go build` succeeds, `go test ./...` passes (no tests yet), `go vet ./...` clean
  - Verify: `go build -o fingersaver . && ./fingersaver` runs without error
  - Files: `go.mod`, `main.go`

- [x] T02: Config loading with `.claude` fallback
  - Acceptance: `Load()` reads `~/.fingersaver/` config, falls back to `.claude` dir for model, creates data dirs, env var overrides work
  - Verify: `go test ./internal/config/... -v` passes
  - Files: `internal/config/config.go`, `internal/config/config_test.go`

- [x] T03: Wire config into main.go with signal handling
  - Acceptance: `main.go` loads config, prints summary, handles SIGINT for clean shutdown
  - Verify: `go run .` prints config summary, `Ctrl+C` exits cleanly
  - Files: `main.go`

---

## Phase 2: Tmux Control Mode Client (parallel with Phase 3)

- [x] T04: Tmux control mode notification parser
  - Acceptance: `ParseNotification()` handles all notification types: `%output`, `%session-changed`, `%session-renamed`, `%sessions-changed`, `%window-add`, `%window-close`, `%window-renamed`, `%window-pane-changed`, `%layout-change`, `%client-session-changed`, `%client-detached`, `%pane-mode-changed`, `%begin`, `%end`, `%error`, `%pause`, `%continue`. `DecodeEscape` decodes `\NNN` octal escapes.
  - Verify: `go test ./internal/tmux/... -run TestParse -v` — 90%+ coverage on parser.go
  - Files: `internal/tmux/parser.go`, `internal/tmux/parser_test.go`

- [x] T05: Tmux session state mirror
  - Acceptance: `StateMirror` tracks sessions/windows/panes from a stream of `Notification`. `Apply()` creates/updates/removes entries. `FindSession()`, `ActivePane()` return correct results after applying a realistic notification sequence.
  - Verify: `go test ./internal/tmux/... -run TestState -v`
  - Files: `internal/tmux/session.go`, `internal/tmux/session_test.go`

- [x] T06: Tmux command builders
  - Acceptance: `NewSessionCmd("test", "/tmp", "bash")` → `"new-session -d -s test -c /tmp bash"` (or equivalent). All command builders produce exact strings. `SendKeysCmd` handles special characters.
  - Verify: `go test ./internal/tmux/... -run TestCmd -v`
  - Files: `internal/tmux/commands.go`, `internal/tmux/commands_test.go`

- [x] T07: Tmux client — subprocess lifecycle
  - Acceptance: `Client` starts dedicated tmux server (`-S socketPath`), connects via `-CC`, reads notifications into channel, sends commands via stdin, stops cleanly. `TmuxClient` interface defined for mocking.
  - Verify: `go test ./internal/tmux/... -run TestClient -v` (unit tests with mocked subprocess)
  - Files: `internal/tmux/client.go`, `internal/tmux/client_test.go`

- [x] T08: Tmux integration tests with real binary
  - Acceptance: Full lifecycle test — start server, create session, send-keys "echo hello", read `%output` notification, kill session, stop server. Socket file cleaned up on exit.
  - Verify: `go test ./internal/tmux/... -tags=integration -run TestIntegration -v`
  - Files: `internal/tmux/client_integration_test.go`

---

## Phase 3: LLM Provider Abstraction (parallel with Phase 2)

- [x] T09: LLM shared types
  - Acceptance: `Role`, `Message`, `ToolCall`, `ToolResult`, `ToolDefinition`, `StreamEvent` (with TextDelta, ToolCallStart, ToolCallDelta, Done, Error variants), `GenerateOptions` all defined. Compiles clean.
  - Verify: `go build ./internal/llm/...`
  - Files: `internal/llm/types.go`

- [x] T10: LLM Provider interface and factory
  - Acceptance: `Provider` interface with `Stream()` and `Name()`. `NewProvider("anthropic", key)` returns Anthropic provider, `NewProvider("openai", key)` returns OpenAI. Unknown name returns error.
  - Verify: `go test ./internal/llm/... -run TestProvider -v`
  - Files: `internal/llm/provider.go`, `internal/llm/provider_test.go`

- [x] T11: Anthropic provider implementation
  - Acceptance: `Stream()` converts `[]Message` to Anthropic params, streams text deltas and tool calls back via channel. Handles context cancellation.
  - Verify: `go test ./internal/llm/... -run TestAnthropic -v` with mock HTTP server returning canned streaming responses
  - Files: `internal/llm/anthropic.go`, `internal/llm/anthropic_test.go`

- [x] T12: OpenAI provider implementation
  - Acceptance: `Stream()` converts `[]Message` to OpenAI params, streams text deltas and tool calls. Handles context cancellation.
  - Verify: `go test ./internal/llm/... -run TestOpenai -v` with mock HTTP server
  - Files: `internal/llm/openai.go`, `internal/llm/openai_test.go`

---

## Phase 4: Agent Orchestrator

- [x] T13: Tool definitions — session operations
  - Acceptance: Six tools defined: `ListSessions`, `CreateSession`, `SwitchSession`, `KillSession`, `SendToSession`, `ReadSessionOutput`. Each has correct parameters and calls the right tmux commands. All work with mocked `TmuxClient`.
  - Verify: `go test ./internal/agent/... -run TestTool -v` — 90%+ coverage on tools.go
  - Files: `internal/agent/tools.go`, `internal/agent/tools_test.go`

- [x] T14: Slash command parser and registry
  - Acceptance: `ParseSlashCommand("/create test")` → `("create", ["test"], true)`. `ParseSlashCommand("hello")` → `("", nil, false)`. Built-in commands: `/create`, `/switch`, `/kill`, `/list`, `/help`. Unknown command returns error.
  - Verify: `go test ./internal/agent/... -run TestCommand -v`
  - Files: `internal/agent/commands.go`, `internal/agent/commands_test.go`

- [x] T15: Hook manager
  - Acceptance: `HookManager` registers and fires hooks by type. `BeforeToolCall` can block execution. `AfterToolCall` can modify results. Multiple hooks fire in registration order. `AgentStop` and `PermissionRequest` types supported.
  - Verify: `go test ./internal/agent/... -run TestHook -v`
  - Files: `internal/agent/hooks.go`, `internal/agent/hooks_test.go`

- [x] T16: Orchestrator — input routing and LLM tool call loop
  - Acceptance: `ProcessInput()` routes slash commands directly, extracts `@mention` targets, sends natural language to LLM. When LLM returns tool calls, executes them with hooks, feeds results back, continues until LLM returns text-only. Events streamed via channel.
  - Verify: `go test ./internal/agent/... -run TestOrchestrator -v` with mock LLM provider
  - Files: `internal/agent/orchestrator.go`, `internal/agent/orchestrator_test.go`

---

## Phase 5: Agent Adapters (parallel with Phase 6)

- [x] T17: Agent adapter interface and registry
  - Acceptance: `AgentAdapter` interface defines `Name`, `Launch`, `SendMessage`, `IsRunning`, `ParseOutput`, `StopConfig`. `Registry` pre-registers Claude and Copilot adapters. Compiles clean.
  - Verify: `go build ./internal/adapters/...`
  - Files: `internal/adapters/adapter.go`

- [x] T18: Claude Code adapter
  - Acceptance: `Launch()` sends correct CLI command via `send-keys`. `SendMessage()` sends text + Enter. `ParseOutput()` detects tool use and completion markers. `StopConfig()` returns hook command config. Works with mocked `TmuxClient`.
  - Verify: `go test ./internal/adapters/... -run TestClaude -v`
  - Files: `internal/adapters/claude.go`, `internal/adapters/claude_test.go`

- [x] T19: Copilot adapter
  - Acceptance: Analogous to T18. `Launch()` sends correct CLI command. Hook wiring works. `ParseOutput()` handles Copilot output format.
  - Verify: `go test ./internal/adapters/... -run TestCopilot -v`
  - Files: `internal/adapters/copilot.go`, `internal/adapters/copilot_test.go`

---

## Phase 6: TUI — Split-Pane Bubbletea Application (parallel with Phase 5)

- [x] T20: TUI styles and message types
  - Acceptance: Lipgloss styles for chat pane, viewer pane, focus indicators. All internal `tea.Msg` types defined. Compiles clean.
  - Verify: `go build ./internal/tui/...`
  - Files: `internal/tui/styles.go`, `internal/tui/messages.go`

- [x] T21: Chat pane model
  - Acceptance: `ChatModel` renders message history (viewport) + input textarea. Enter submits. AppendMessage scrolls viewport. Resize handled.
  - Verify: `go test ./internal/tui/... -run TestChat -v`
  - Files: `internal/tui/chat.go`

- [x] T22: Tmux viewer pane model
  - Acceptance: `ViewerModel` renders tmux output. Appends output from notifications. Switches session buffers. When focused, keystrokes forwarded to tmux via `send-keys`.
  - Verify: `go test ./internal/tui/... -run TestViewer -v`
  - Files: `internal/tui/viewer.go`

- [x] T23: Root app model — split-pane layout and dual input
  - Acceptance: `AppModel` renders split pane via `lipgloss.JoinHorizontal`. `Tab` toggles focus. `FocusChat` routes keys to chat, `FocusViewer` routes keys to tmux. Tmux notifications bridge to viewer. Chat submit routes to orchestrator.
  - Verify: `go test ./internal/tui/... -run TestApp -v`
  - Files: `internal/tui/app.go`, `internal/tui/app_test.go`

- [x] T24: Wire main.go — full startup and shutdown
  - Acceptance: `main.go` loads config → creates tmux client → creates LLM provider → creates orchestrator → creates TUI → runs Bubbletea. SIGINT triggers clean shutdown. All deferred cleanup runs.
  - Verify: `go run .` launches split-pane TUI, `Ctrl+C` exits cleanly, no orphaned tmux processes
  - Files: `main.go`

---

## Phase 7: Integration and Polish

- [x] T25: Chat persistence — `~/.fingersaver/chat.md`
  - Acceptance: Messages written to Markdown file on every new message. Loaded on startup. Roundtrip preserves content and timestamps.
  - Verify: `go test ./internal/tui/... -run TestChatHistory -v`
  - Files: `internal/tui/chat.go`, `internal/tui/chat_test.go` (add persistence tests)

- [x] T26: Hook wiring — stop hooks and permission hooks
  - Acceptance: Claude Code stop hook fires `HookAgentStop` when task completes. Permission hook routes approval request to chat pane, user types y/n, result sent back to agent via `send-keys`.
  - Verify: `go test ./internal/agent/... -run TestHookIntegration -v` with mock adapter
  - Files: `internal/agent/orchestrator.go`, `internal/adapters/claude.go`

- [x] T27: Cross-agent messaging
  - Acceptance: When one agent completes (stop hook), orchestrator reads its output, extracts relevant context via LLM, sends summary to another agent session.
  - Verify: `go test ./internal/agent/... -run TestCrossAgent -v` with mock LLM and tmux
  - Files: `internal/agent/orchestrator.go`

- [x] T28: End-to-end integration tests
  - Acceptance: Full workflow test with real tmux: create session, launch agent adapter, send message via orchestrator, verify viewer output, switch session, kill session, clean shutdown.
  - Verify: `go test ./... -tags=integration -v`
  - Files: `integration_test.go` (project root, build tag: `//go:build integration`)

- [x] T29: Polish — lint, format, final pass
  - Acceptance: `go fmt ./...` clean, `golangci-lint run` clean, `go test ./...` green, all SPEC.md success criteria met.
  - Verify: `go fmt ./... && golangci-lint run && go test ./...`
  - Files: All files (formatting/linting pass)
