# Spec: Makro

## Objective

Makro is a Go terminal application that lets a single developer manage multiple AI coding agents (Claude Code, GitHub Copilot) simultaneously through a split-pane TUI. The left pane is a hybrid chat/command interface powered by an LLM orchestrator; the right pane renders the active tmux session via `tmux -CC` control mode. Both panes accept input simultaneously — the user can chat in the left pane while directly typing into tmux sessions in the right pane.

**Target user:** A developer who runs multiple coding agents in parallel and wants a unified control surface to switch between them, relay messages, and monitor progress — without managing multiple terminal windows manually.

**Core user stories:**
- As a developer, I can `@mention` a session in the chat pane to switch the right-pane viewer to that session
- As a developer, I can type slash commands (`/switch`, `/create`, `/kill`, `/list`) for precise session operations
- As a developer, I can type natural language ("switch to the auth service session and tell it to run tests") and the LLM orchestrator figures out the right tool calls
- As a developer, I can directly interact with the tmux session in the right pane at any time without switching focus
- As a developer, agents can communicate through hooks — when Claude Code finishes, it can notify Copilot to pick up the next task

## Tech Stack

| Component | Choice | Version |
|-----------|--------|---------|
| Language | Go | 1.22+ |
| TUI Framework | Bubbletea v2 | `charm.land/bubbletea/v2` |
| Layout/Styling | Lipgloss v2 | `charm.land/lipgloss/v2` |
| Input | Bubbles v2 | `charm.land/bubbles/v2` |
| LLM Clients | Anthropic SDK + OpenAI SDK | Direct HTTP clients, no wrapper framework |
| Terminal I/O | vhs / pure Go PTY | `github.com/creack/pty` for PTY management |
| Build | Go modules | No Makefile |
| Lint | golangci-lint | Latest |
| Test | Go standard testing + testify | `github.com/stretchr/testify` |

No CGO. Pure Go for portability. tmux must be installed on the host.

## Commands

```bash
# Build
go build -o makro .

# Run
go run .

# Test all
go test ./...

# Test single package
go test ./internal/tmux/...

# Test single test (by name)
go test ./internal/tmux/... -run TestParseOutput

# Test with verbose output
go test -v ./...

# Lint
golangci-lint run

# Format
go fmt ./...
```

## Project Structure

```
makro/
├── main.go                       # Entry point, dependency wiring
├── internal/
│   ├── tui/                      # Bubbletea TUI layer
│   │   ├── app.go                # Root model: split-pane layout, dual input routing
│   │   ├── chat.go               # Chat pane: message history, input, @mentions
│   │   ├── viewer.go             # Tmux viewer: renders control mode output
│   │   ├── styles.go             # Lipgloss styles and theme constants
│   │   └── messages.go           # Internal message types (tea.Msg implementations)
│   ├── tmux/                     # Tmux control mode integration
│   │   ├── client.go             # tmux -CC subprocess lifecycle (start, stop, restart)
│   │   ├── parser.go             # Control mode line parser (%output, %session-*, etc.)
│   │   ├── session.go            # Session state mirror (tracks sessions/windows/panes)
│   │   └── commands.go           # High-level tmux command builders
│   ├── agent/                    # Orchestrator agent
│   │   ├── orchestrator.go       # LLM-backed coordinator: parses intent, calls tools
│   │   ├── tools.go              # Tool definitions (switch-session, create-session, etc.)
│   │   ├── commands.go           # Slash command parser and registry
│   │   └── hooks.go              # Hook manager: stop hooks, permission hooks, message hooks
│   ├── llm/                      # Multi-provider LLM abstraction
│   │   ├── provider.go           # Provider interface and registry
│   │   ├── anthropic.go          # Anthropic (Claude) provider implementation
│   │   ├── openai.go             # OpenAI provider implementation
│   │   └── types.go              # Shared types: Message, ToolCall, StreamEvent, etc.
│   ├── adapters/                 # Coding agent adapters
│   │   ├── adapter.go            # AgentAdapter interface
│   │   ├── claude.go             # Claude Code: launch config, hook wiring, output parsing
│   │   └── copilot.go            # Copilot: launch config, hook wiring, output parsing
│   └── config/
│       └── config.go             # Config loading: .claude dir for LLM model, ~/.makro/ paths
├── go.mod
├── go.sum
├── CLAUDE.md
└── SPEC.md
```

## Code Style

### Naming Conventions
- Packages: lowercase, single word (`tmux`, `tui`, `agent`, `llm`)
- Types: PascalCase (`TmuxClient`, `Session`, `Provider`)
- Interfaces: PascalCase, often `-er` suffix (`Provider`, `AgentAdapter`) or descriptive (`AgentAdapter`)
- Functions: PascalCase (exported), camelCase (unexported)
- Constants: PascalCase (exported), camelCase (unexported)
- Files: lowercase, underscore-separated, one primary type per file

### Example — Tmux Parser

```go
package tmux

// Notification represents a parsed tmux control mode notification.
type Notification struct {
    Type    NotifType
    PaneID  string // e.g., "%0"
    Session string // e.g., "my-session"
    Data    string // raw payload
}

type NotifType int

const (
    NotifOutput NotifType = iota
    NotifSessionChanged
    NotifWindowAdd
    NotifWindowClose
)

// ParseNotification parses a single line from tmux -CC stdout.
// Returns ErrUnknownNotification for lines that don't match known patterns.
func ParseNotification(line string) (Notification, error) {
    // ...
}
```

### Example — Tool Definition

```go
package agent

// Tool defines a callable tool the orchestrator can invoke.
type Tool struct {
    Name        string
    Description string
    Parameters  []Param
    Execute     func(args map[string]any) (string, error)
}

type Param struct {
    Name        string
    Type        string // "string", "number", "boolean"
    Description string
    Required    bool
}
```

### Formatting
- `go fmt` is the source of truth — no custom formatting rules
- Tabs for indentation (Go default)
- Error messages: lowercase, no trailing punctuation
- Comments: only for WHY, not WHAT

### Error Handling
```go
// Good: wrap with context
session, err := tmuxClient.CreateSession(ctx, name, workingDir)
if err != nil {
    return fmt.Errorf("create session %q: %w", name, err)
}

// Bad: silently swallow
_ = tmuxClient.CreateSession(ctx, name, workingDir)
```

## Testing Strategy

### Framework
- Go standard `testing` package + `github.com/stretchr/testify` for assertions
- Table-driven tests for parser and command logic

### Test Locations
- Tests live alongside source: `internal/tmux/parser_test.go` for `internal/tmux/parser.go`
- Integration tests: `internal/tmux/client_integration_test.go` (build tag: `//go:build integration`)
- Requires running tmux server for integration tests

### Coverage Expectations
- `internal/tmux/parser.go`: 90%+ — pure parsing logic, fully testable
- `internal/llm/`: 80%+ — provider logic mocked via interface
- `internal/agent/tools.go`: 90%+ — tool execution logic
- `internal/tui/`: 60%+ — visual rendering harder to test, focus on Update logic

### Test Levels

| Level | Scope | Example |
|-------|-------|---------|
| Unit | Single function | `TestParseNotification` validates `%output %0 hello` parses correctly |
| Unit | Tool execution | `TestSwitchSessionTool` verifies correct tmux command is dispatched |
| Integration | tmux subprocess | `TestClientLifecycle` starts real tmux, creates session, reads output |
| Manual | Full TUI | Launch app, create sessions, verify split-pane rendering |

### Mocking Strategy
- LLM providers: mock the `Provider` interface, not HTTP calls
- tmux: for unit tests, mock the `TmuxClient` interface; for integration tests, use real tmux
- Agent adapters: mock the `AgentAdapter` interface

### Key Test Patterns

```go
func TestParseNotification(t *testing.T) {
    tests := []struct {
        name     string
        input    string
        want     tmux.Notification
        wantErr  bool
    }{
        {
            name:  "output notification",
            input: "%output %0 hello world",
            want:  tmux.Notification{Type: tmux.NotifOutput, PaneID: "%0", Data: "hello world"},
        },
        {
            name:    "unknown line",
            input:   "some random output",
            wantErr: true,
        },
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, err := tmux.ParseNotification(tt.input)
            if tt.wantErr {
                require.Error(t, err)
                return
            }
            require.NoError(t, err)
            assert.Equal(t, tt.want, got)
        })
    }
}
```

## Boundaries

### Always Do
- Run `go test ./...` before every commit
- Run `go fmt ./...` before every commit
- Use `internal/` for all packages — no public API in v1
- Define interfaces for external dependencies (LLM providers, tmux client, agent adapters) to enable testing
- Wrap all errors with `fmt.Errorf("context: %w", err)` — never return bare errors from other packages
- Handle `context.Context` cancellation in all long-running operations (tmux reads, LLM streams)
- All new packages must have at least one test file

### Ask First
- Adding a new LLM provider
- Adding a new agent adapter beyond Claude Code / Copilot
- Changing the tmux communication protocol (e.g., switching from `-CC` to a custom socket protocol)
- Introducing a new dependency beyond what's listed in Tech Stack
- Changing the right-pane rendering strategy (e.g., embedding a full terminal emulator)
- Removing or renaming an existing package

### Never Do
- Reimplement tmux — always wrap the existing binary
- Use CGO — pure Go only
- Commit API keys, tokens, or credentials
- Use `panic()` outside of truly unrecoverable situations (prefer returning errors)
- Use `init()` functions for side effects
- Import coding agent CLIs as Go libraries — always interact via subprocess (tmux `send-keys` / hooks)

## Success Criteria

### v1 MVP — Core + Both Adapters

- [x] **Split-pane TUI renders** — Left chat pane + right tmux viewer, both accepting input simultaneously
- [x] **Dedicated tmux server** — Makro starts its own tmux server at `~/.makro/tmux.sock`, isolated from user's existing tmux
- [x] **tmux integration works** — App connects to its dedicated tmux server via CLI commands, parses notifications, displays output in right pane
- [x] **Session lifecycle** — Create, list, switch, kill tmux sessions via chat commands
- [x] **Slash commands work** — `/create <name>`, `/switch <name>`, `/kill <name>`, `/list` all functional
- [x] **@mention targeting** — Typing `@session-name <message>` sends message to that session via `send-keys`
- [x] **LLM orchestrator responds** — Natural language input triggers tool calls via LLM (multi-provider: Anthropic + OpenAI)
- [x] **Claude Code adapter** — Can launch Claude Code in a tmux session, wire stop/permission hooks
- [x] **Copilot adapter** — Can launch Copilot in a tmux session, wire hooks
- [x] **Hook integration** — Stop hooks fire when an agent task completes; permission hooks route approval requests to chat pane
- [x] **Cross-agent messaging** — Orchestrator can read output from one agent and relay context to another
- [x] **All tests pass** — `go test ./...` green (151 tests), coverage targets met per package
- [x] **Chat persistence** — Chat history written to `~/.makro/chat.md`, survives restarts
- [x] **Clean exit** — Ctrl+C cleanly shuts down dedicated tmux server and restores terminal state
- [ ] **End-to-end integration tests** — Full lifecycle test with real tmux (build tag: `//go:build integration`)

## Resolved Questions

1. **tmux session naming** — User-named. Each session maps to a coding agent, so names like `auth-service`, `frontend`, `copilot-review` are expected.
2. **Chat persistence** — Chat history stored in `~/.makro/chat.md` (Markdown format). Survives app restarts.
3. **LLM model selection** — Reads model config from `.claude` directory (reuses existing Claude Code configuration). Falls back to sensible defaults if not found.
4. **Dedicated tmux server** — Yes. Makro spawns its own tmux server via `tmux -S ~/.makro/tmux.sock` so it never interferes with the user's existing tmux sessions.
5. **tmux scrollback** — Handled by tmux itself. Makro just renders what tmux sends; no additional buffering needed.

---

## Session Log — 2026-06-13/14

### 1. Removed kill_session tool (commit `2af912b`)

The `kill_session` LLM tool was error-prone — the orchestrator occasionally killed the wrong tmux session. Removed the tool definition (`internal/agent/tools/kill_session.go`), the `/kill` slash command (`internal/agent/commands.go`), tool docs from the orchestrator system prompt, and the `TestKillSessionTool` test. The GUI's manual DELETE endpoint (`cmd/gui/tmux_service.go`) is preserved — only the LLM-accessible tool was removed.

### 2. Removed Wails v3 dead scaffolding (commit `fedab01`)

The GUI ships via **Electron + electron-builder** (`cmd/gui/package.json`), but a complete **Wails v3** project scaffold was left over from early bootstrap:
- `APP_NAME: "gui"`, placeholder metadata (`"My Company"`, `version: 0.0.1`)
- Per-platform Taskfiles calling `wails3` against outputs the Electron flow never produces

Deleted 72 files across `cmd/gui/build/{android,docker,linux,windows,ios}`, `build/appicon.{icon,iconset}`, top-level `Taskfile.yml`s, `config.yml`, and Wails-only darwin assets. Kept `build/darwin/icons.icns` (referenced by `package.json` mac.icon), `build/appicon.png` (icon_gen.py output), and `build/icons_preview/` (icon variants).

### 3. Removed Wails v3 from Go code layer

After the build scaffolding, removed Wails from the Go code itself:
- **`cmd/gui/main.go`**: Deleted the default Wails GUI branch (`application.New`, `app.Window.NewWithOptions`, `app.Run`). Only `serve` (HTTP server for Electron) + `notify`/`permission` (hook forwarders) remain.
- **`cmd/gui/terminal_service.go`**: Deleted `TerminalService` struct + all Wails RPC methods (`AttachSession`, `WriteInput`, `ResizeTerminal`, `DetachSession`, `SetApp`, `Startup`). Kept PTY helper functions (`terminalProcess`, `setWinsize`, `winsize`, `ensureTerm`, `detachStaleClients`) used by the xterm WebSocket handler.
- **`cmd/gui/chat_service.go`**: Deleted `app *application.App` field, `SetApp()`, `Startup()`, and the Wails `emit` fallback path (`s.app.Event.Emit`). Only the SSE hub path remains. `NewChatService()` signature simplified (no args).
- **`go.mod`**: `github.com/wailsapp/wails/v3` dependency removed.

### 4. Desktop notification feature (v0.6.0, commit `08fa043` + `c32134a`)

When a coding agent finishes its task, Makro posts a macOS desktop notification so the user notices even when not focused.

**New package `internal/notify/`:**
- `notify.go` — `Notify()` posts via `terminal-notifier` (clickable, activates Makro on click) with `osascript display notification` fallback. `IsFrontmost()` checks if Makro is the active app via `osascript` (fail-open: notify when unsure). `truncateBody()` keeps first line, ≤200 runes.
- `client.go` — `SendHook()` forwards hook payloads to a running Makro instance over the Unix socket (`~/.makro/hooks.sock`). Used by the `makro-serve notify`/`permission` subcommands.
- `bark.go` — `BarkPush()` sends push via the Bark app's HTTP API (see §6 below).

**Wiring:**
- `cmd/gui/main.go`: Added `notify`/`permission` subcommands that forward events over the socket to a running instance.
- `cmd/gui/chat_service.go`: `OnAgentStop` callback now fires desktop notify (when not frontmost) + Bark push (when configured). `init()` injects Claude Code Stop/Permission hooks (idempotent `EnsureStopHook`/`EnsurePermissionHook`). `serve()` eager-inits so the notifier is live at startup.
- `main.go` (TUI): `OnAgentStop` fires desktop notify always (TUI can't reliably detect focus).

### 5. Chat duplicate message fix

**Root cause:** `chatWSHandler` (`cmd/gui/server.go`) replayed the entire `hub.history` (up to 200 events) on every WebSocket (re)connect. iPhone's `loadHistory()` (HTTP) + WS history replay = double display.

**Fix:** Removed the history replay block from `chatWSHandler` (lines ~667-673). WS now only streams **new** events. Clients load history via `GET /api/chat/history`. Verified Electron frontend (`main.js`) also uses HTTP history (not WS replay).

### 6. iOS push notification — APNs attempt (blocked)

Full APNs infrastructure built but blocked by iOS 26 beta installd:

**Backend (`internal/apns/`):**
- `apns.go` — APNs HTTP/2 client. ES256 JWT signing (PKCS#8 `.p8` key, `x509.ParsePKCS8PrivateKey` + `pem.Decode`). `Push()` POSTs to `api.sandbox.push.apple.com` (sandbox) or `api.push.apple.com` (production).
- Config: `apns_key_path`, `apns_key_id`, `apns_team_id`, `apns_bundle_id`, `apns_sandbox` in config.json + `MAKRO_APNS_*` env overrides.
- `cmd/gui/device_store.go` — Device token persistence (`~/.makro/device_tokens.json`, `map[device_id]token`).
- `POST /api/device-token` endpoint in `server.go`.

**iOS app (`ios/Makro/`):**
- `AppDelegate.swift` — `registerForRemoteNotifications`, token hex-encode + `APIClient.registerDeviceToken()` upload, `UNUserNotificationCenterDelegate` for tap deep-link.
- `MakroApp.swift` — `@UIApplicationDelegateAdaptor`, tab selection + `makroOpenSession` notification.
- `APIClient.swift` — `registerDeviceToken(deviceID:token:)`.
- `Makro.entitlements` — `aps-environment = development`.
- `project.yml` — `CODE_SIGN_ENTITLEMENTS`, `capabilities: [com.apple.Push]`.
- `Info.plist` — `UIBackgroundModes: [remote-notification]`.

**Blocker:** iOS 26 beta installd rejects `aps-environment` entitlement with `0xe8008015` ("A valid provisioning profile for this executable was not found"). Confirmed via controlled experiment: `keychain-access-groups` installs fine, `aps-environment` uniquely rejected. All config verified correct (profile has aps, cert in profile, device in profile, portal Push capability + Development SSL Certificate created). The rejection happens regardless of install method (devicectl, ios-deploy, ideviceinstaller, Xcode Cmd+R). Likely an iOS 26 beta installd bug. **Deferred to iOS 26 stable.**

### 7. iOS push notification — Bark (working)

Since APNs is blocked by iOS 26 beta, Bark provides push via the Bark app's own APNs certificate:

**Implementation:**
- `internal/notify/bark.go` — `BarkPush()` sends HTTP POST to `https://api.day.app/<key>` with JSON payload (title, subtitle, body, group, sound).
- Config: `bark_key`, `bark_url` (default `https://api.day.app`) in config.json + `MAKRO_BARK_*` env overrides.
- `chat_service.go` `OnAgentStop`: When `barkKey` is configured, sends Bark push (covers Mac + iPhone). When Bark is configured, **skips desktop notify** (avoids Mac duplicate). When Bark is not configured, falls back to desktop notify (frontmost check).

**Current config:** `bark_key: 6nSWNuG8TsNrajVgc3Xywi`, sound: `multiwayinvitation`.

**Full chain:** Agent finishes → Claude Code Stop hook → `makro notify <session> done` → socket → `OnAgentStop` → `BarkPush` → iPhone + Mac notification.

### 8. Model upgrade

- Makro config: `llm_model: glm-5.1` → `glm-5.2`
- Claude Code settings: `ANTHROPIC_DEFAULT_OPUS_MODEL` + `ANTHROPIC_DEFAULT_SONNET_MODEL` → `glm-5.2` (haiku stays `glm-4.7`)

### Releases

| Version | Tag | What |
|---------|-----|------|
| v0.5.0 | (skipped — already taken) | — |
| v0.6.0 | `v0.6.0` | Desktop notifications + Wails dead code removal + kill_session removal |
| (uncommitted) | — | Bark push + chat duplicate fix + APNs infrastructure (blocked) + model upgrade |

### Backlog (not yet implemented)

1. **Session tab UX** — tabs don't show which session is working vs idle. Want per-session color/state (working=pulse, idle=muted, notification=badge). Tabs may be replaced with mini-window cards.
2. **iOS app launch** — next session.
3. **APNs push** — deferred to iOS 26 stable (installd beta bug).
