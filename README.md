# Makro

AI coding agent orchestrator with a split-pane terminal UI. Manage multiple coding agents (Claude Code, GitHub Copilot, etc.) in parallel through a unified interface.

## Features

- **Split-pane TUI** — Chat pane on the left, tmux session viewer on the right
- **Phone layout** — Vertical split for narrow terminals (`--phone` or auto-switches at < 80 columns)
- **@mention sessions** — Type `@session-name message` to send commands to a specific agent
- **Sticky targeting** — Tab-complete an `@session` to target all subsequent messages there
- **Slash commands** — `/create`, `/kill`, `/list`, `/switch`, `/watch`, `/layout`
- **Natural language** — Describe what you want, the LLM orchestrator figures out the tool calls
- **Session guardian** — Automated confirmation handling for background agents
- **Multi-provider LLM** — Anthropic Claude and OpenAI support
- **Cross-agent relay** — Agents communicate through hooks

## Install

```bash
brew install cybernagle/tap/makro
```

Or build from source:

```bash
go build -o makro .
```

## Quick Start

```bash
# Start with default settings
makro

# Use phone (vertical) layout
makro --phone

# CLI chat mode (no TUI, for testing)
makro --chat

# Show current config
makro --config
```

## Key Bindings

| Key | Action |
|-----|--------|
| `Ctrl+O` | Switch focus between Chat and Viewer panes |
| `Ctrl+D` | Quit immediately |
| `Ctrl+R` | Force layout recalculation |
| `Ctrl+C` | Clear sticky target (press twice to quit) |
| `[` / `]` | Switch between tmux sessions (in Viewer) |
| `Up` / `Down` | Navigate input history (in Chat) |
| `Enter` | Send message |
| `Tab` | Accept autocomplete suggestion |

## Chat Commands

```
@session text    Send text to a tmux session
/create <name>   Create a new tmux session
/kill <name>     Kill a tmux session
/list            List all sessions
/switch <name>   Switch viewer to a session
/watch <name>    Start guardian for a session
/watch stop      Stop all guardians
/layout phone    Switch to vertical layout
/layout default  Switch to horizontal layout
/help            Show available commands
```

## Configuration

Makro reads configuration from `~/.makro/config.json`:

```json
{
  "llm_provider": "anthropic",
  "llm_model": "claude-sonnet-4-20250514",
  "tmux_mode": "auto"
}
```

### Environment Variables

| Variable | Description |
|----------|-------------|
| `MAKRO_LLM_PROVIDER` | `anthropic` or `openai` |
| `MAKRO_LLM_API_KEY` | API key (overrides config) |
| `MAKRO_LLM_MODEL` | Model name |
| `ANTHROPIC_API_KEY` | Anthropic API key (fallback) |
| `OPENAI_API_KEY` | OpenAI API key (fallback) |
| `MAKRO_TMUX_MODE` | `auto`, `dedicated`, or `shared` |

### Claude Settings Integration

Makro automatically reads `.claude/settings.json` for API key and model preferences.

## Architecture

```
┌──────────────────────────────────────────────────────┐
│                  Makro (main)                  │
│              Bubbletea TUI - split pane              │
├─────────────────────┬────────────────────────────────┤
│   Chat Pane (40%)   │    Tmux Viewer (60%)           │
│   - Message history  │    - Session output rendering  │
│   - Input + @mention │    - Session switching         │
│   - Tool results     │    - Keystroke forwarding      │
├─────────────────────┴────────────────────────────────┤
│                 Agent Orchestrator                    │
│  - Input routing: slash commands → @mentions → LLM   │
│  - Tool call loop with hook system                   │
│  - Cross-agent message relay                         │
├──────────────────────────────────────────────────────┤
│                  Tmux Manager                         │
│  - Dedicated tmux server on ~/.makro/tmux.sock │
│  - Session/window/pane state mirror                  │
│  - 500ms polling loop                                │
├──────────────────────────────────────────────────────┤
│               LLM Providers                          │
│  - Anthropic (streaming + tool use)                  │
│  - OpenAI (streaming + tool use)                     │
└──────────────────────────────────────────────────────┘
```

## Development

```bash
# Build
go build -o makro .

# Test
go test ./...

# Vet
go vet ./...

# Format
go fmt ./...

# Integration tests (requires tmux)
go test ./... -tags=integration -v
```

## Requirements

- Go 1.26+
- tmux
- Anthropic or OpenAI API key

## License

Apache-2.0 license
