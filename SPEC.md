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
