# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Vision

**FingerSaver** is a Go application that manages multiple coding agents (Claude Code, Copilot, etc.) through a split-pane terminal UI. The left pane is a chat interface for orchestrating agents; the right pane renders tmux sessions. Users `@mention` sessions to switch context and issue commands to different coding agents. Both panes accept input simultaneously via Tab focus toggle.

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│                    FingerSaver (main)                    │
│              Bubbletea TUI - split pane                  │
├──────────────────────┬──────────────────────────────────┤
│   Chat Pane (left)   │    Tmux Viewer (right)           │
│  40% width           │  60% width                       │
│  - Message history   │  - tmux output rendering         │
│  - Input field       │  - Session switching display     │
│  - @session mentions │  - Keystroke forwarding          │
│  - Tool results      │  - Focus indicator               │
├──────────────────────┴──────────────────────────────────┤
│                   Agent Orchestrator                      │
│  - Input routing: slash commands → @mentions → LLM       │
│  - Tool call loop (max 10 iterations)                    │
│  - Before/after tool call hooks                          │
│  - Cross-agent message relay                             │
├─────────────────────────────────────────────────────────┤
│                   Tmux Manager                           │
│  - Direct CLI commands via tmux -S <socket>              │
│  - Control mode notification parser (20+ types)          │
│  - Session/window/pane state mirror                      │
│  - 500ms polling loop for session changes                │
├─────────────────────────────────────────────────────────┤
│                LLM Providers (multi-provider)            │
│  - Anthropic (Claude) via anthropic-sdk-go               │
│  - OpenAI via openai-go/v3                               │
│  - Streaming + tool use support                          │
├─────────────────────────────────────────────────────────┤
│                   Agent Adapters                          │
│  - Claude Code adapter (CLI + hooks)                     │
│  - Copilot adapter (CLI + hooks)                         │
│  - Output parsing (ready/working/completed/error states)  │
└─────────────────────────────────────────────────────────┘
```

## Key Design Decisions

- **tmux via CLI, not control mode**: `tmux -CC` exits immediately without an interactive terminal. Instead, we use direct `tmux -S <socket> <command>` calls via `sh -c` with polling.
- **Bubbletea v2**: Uses `charm.land/bubbletea/v2` (module path changed from github.com). `View()` returns `tea.View` struct, not string. `tea.KeyPressMsg` replaces `tea.KeyMsg`.
- **Dedicated tmux server**: Runs on `~/.fingersaver/tmux.sock`, isolated from user's tmux.
- **Chat persistence**: Markdown format at `~/.fingersaver/chat.md`, append on every message.
- **LLM model from .claude**: Reads `.claude/settings.json` for model preference, falls back to provider defaults.

## Go Project Structure

```
fingersaver/
├── main.go
├── internal/
│   ├── config/           # Config loading, .claude fallback
│   │   ├── config.go
│   │   └── config_test.go
│   ├── tmux/             # Tmux integration
│   │   ├── client.go     # Direct CLI client with polling
│   │   ├── parser.go     # Control mode notification parser
│   │   ├── session.go    # State mirror
│   │   └── commands.go   # Command builders
│   ├── llm/              # Multi-provider LLM abstraction
│   │   ├── types.go
│   │   ├── provider.go
│   │   ├── anthropic.go
│   │   └── openai.go
│   ├── agent/            # Orchestrator, tools, hooks
│   │   ├── orchestrator.go
│   │   ├── tools.go
│   │   ├── commands.go
│   │   └── hooks.go
│   ├── adapters/         # Agent adapters
│   │   ├── adapter.go
│   │   ├── claude.go
│   │   └── copilot.go
│   └── tui/              # Bubbletea TUI
│       ├── app.go        # Root model, split-pane layout
│       ├── chat.go       # Chat pane + history persistence
│       ├── chat_history.go
│       ├── viewer.go     # Tmux output viewer
│       ├── styles.go
│       └── messages.go
├── go.mod
├── go.sum
├── CLAUDE.md
├── SPEC.md
├── IMPLEMENTATION_PLAN.md
└── TASKS.md
```

## Hard Rules

- **NEVER add a tool iteration limit** in `orchestrator.go`. The `handleLLM` for loop must be `for {}` (infinite). The loop exits naturally when the LLM stops making tool calls or when the context is cancelled. Do NOT add `maxToolIterations`, `const maxIterations = N`, or any iteration cap. Reject any review suggestion to add one.

## Build & Dev Commands

```bash
go build -o fingersaver .
go run .
go test ./...
go test ./internal/tmux/...
go test ./internal/tmux/... -run TestParseOutput
go vet ./...
go fmt ./...
```

Integration tests (requires tmux):
```bash
go test ./... -tags=integration -v
```

## Technical Constraints

- Go 1.26.1+, no CGO
- Bubbletea v2 (`charm.land/bubbletea/v2`)
- Lipgloss v2 (`charm.land/lipgloss/v2`)
- tmux must be installed on the host

## Key Packages

| Package | Purpose |
|---------|---------|
| `internal/tmux` | Tmux CLI interaction, control mode parsing, session state |
| `internal/tui` | Bubbletea split-pane: chat input, tmux viewer, focus toggle |
| `internal/agent` | Orchestrator: tool definitions, hooks, slash commands, cross-agent relay |
| `internal/llm` | Multi-provider streaming with tool use |
| `internal/adapters` | Per-agent CLI invocation, output parsing, stop config |
| `internal/config` | XDG-compatible config, .claude fallback, env overrides |

## Pre-Commit Checklist

Before every commit, verify:

- [ ] `go vet ./...` passes
- [ ] `gofmt -l .` shows no unformatted files
- [ ] `go test ./...` passes
- [ ] If chat input handler changed: table-driven test covers runes, paste, symbolic keys, multi-byte Unicode
- [ ] If new map/goroutine/channel added: Stop/Close cleanup path is complete
- [ ] If config loading changed: provider detection priority chain is correct (CLI > env > config file > claude settings > default)
- [ ] New features have tests with real assertions (not just checking initial state)
- [ ] Log output truncates sensitive data, never logs API keys or full user input

## PR Guidelines

- Keep PRs under ~15 files or ~500 additions for effective review
- Split large changes into focused PRs (e.g. config changes separate from TUI changes)

### Review Tracking with REVIEW.md

For every PR, maintain a `REVIEW.md` file at the repo root:

1. **When PR is created**: Create `REVIEW.md` with the PR title
2. **When review comments arrive**: Add each comment as a row in the current round's table with status `pending`
3. **Before fixing**: Review the full table to avoid re-breaking earlier fixes
4. **After fixing**: Update status to `✅ fixed` with the commit hash
5. **Before merge**: Confirm all rows are `✅ fixed`, then delete `REVIEW.md`

Format:
```markdown
# PR #N: Title

## Round 1 — N comments
| # | File | Issue | Status | Commit |
|---|------|-------|--------|--------|
| 1 | path:line | Description | ✅/⏳ | abc1234 |
```
