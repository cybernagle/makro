# Makro

Makro is a multi-surface **AI coding-agent orchestrator**. It manages many coding agents (Claude Code, GitHub Copilot, Codex, …) running in parallel tmux sessions, and gives you three ways to drive them:

- a **terminal TUI** (split-pane, the original),
- a **desktop app** (Electron + Go backend, with chat / terminals / kanban / artifacts / cost dashboard),
- an **iOS app** (voice calls, sessions, kanban, artifacts — talk to your agents from your phone).

All three talk to the same Go backend over a TLS-secured, authenticated HTTP/WebSocket API.

## Features

### Desktop app (`cmd/gui`)
- **Split-pane workspace** — chat with the orchestrator on one side, live tmux terminals on the other.
- **Terminal tabs** — one tab per session; `Ctrl+Tab` / `Shift+Ctrl+Tab` cycle tabs (browser-style); `Ctrl+1..9` jumps.
- **Kanban** — a human-facing task board (todo / in-progress / review / done). Send a card to a session to dispatch it.
- **Artifacts** — auto-discovered HTML/video that agents generate, previewable in-app.
- **Cost dashboard** — per-model / per-session prompt-usage tracking with a 5h quota view.
- **`@mention` / `&monitor`** — target a specific session, or watch one until idle and auto-handle confirmations.
- **Views** — `Ctrl+T` terminals, `Ctrl+D` dashboard, `Ctrl+U` cost, `Ctrl+B` toggle chat, `Ctrl+J` focus chat↔terminal.

### iOS app (`ios/Makro`)
- **Voice calls** — phone-call-style continuous STT/TTS. Discuss an idea out loud, have the assistant propose a plan, confirm, and it dispatches the work to a session. See [Voice calls & modes](#voice-calls--modes-ios).
- **Sessions** — view and drive any tmux terminal from the phone (xterm over WebSocket).
- **Kanban** — the same task board, on the go.
- **Artifacts** — browse and preview agent-generated HTML/video.
- **Push** — APNs + Bark notifications when an agent stops or needs attention.

### Terminal TUI (root `main.go`)
- Split-pane chat + tmux viewer, phone layout for narrow terminals, `@mention`, slash commands, session guardian, cross-agent relay.

### Shared backend
- **Multi-provider LLM** — Anthropic Claude, OpenAI, and OpenAI-compatible endpoints (e.g. BigModel/GLM, auto-detected).
- **Tool-calling orchestrator** with a hook system (before/after tool call, agent start/stop, permission).
- **Send-to-session reliability** — atomic send + first-Enter-loss recovery (`SendConfirmed`), so the first message to a freshly-rendered agent actually submits.
- **Brain / memory** — optional proactive memory layer (`~/.makro/`, memory-cli integration).
- **TLS + password auth** — self-signed cert (TOFU pinning on iOS), constant-time password compare, path-traversal-safe serving.

## Screenshots

> _Placeholder — capture and drop into `docs/`:_
>
> | Shot | Suggested file | Shows |
> |------|----------------|-------|
> | Desktop app | `docs/shot-desktop.png` | Chat + terminal tabs + kanban |
> | iOS voice call | `docs/shot-ios-call.png` | Call screen with the mode dropdown + (ideally) a pending-plan card |
> | iOS artifacts | `docs/shot-ios-artifacts.png` | Artifacts list for a session |
>
> Reference them here as `![Desktop](docs/shot-desktop.png)` once added.

## Voice calls & modes (iOS)

A voice call runs a **discuss → propose → confirm → dispatch** flow, and the assistant can never dispatch without your confirmation (the `send_to_session` tool is gated during a call; dispatch is performed deterministically by the backend only after you confirm).

Pick a **mode** from the dropdown at the top of the call screen:

| Mode | Behavior |
|------|----------|
| **闲聊 / Chat** | Pure conversation — no tools, no dispatch. "Say it, get a reply." |
| **落实 / Plan** *(default)* | Discuss, then the assistant proposes a structured plan (who/what/how), you confirm, it dispatches to a session. |
| **查询 / Query** | Read-only tools (`list_sessions`, `read_session_output`) to answer "what's `dev` doing?" — no dispatch. |

Modes are switched manually (voice-switching is intentionally deferred). The selected mode is remembered across calls.

## Architecture

```
┌─────────────────────────┌──────────────────────────┐
│  Desktop app            │  iOS app                 │
│  Electron + Vite UI     │  SwiftUI                 │
│  (cmd/gui/frontend)     │  (ios/Makro)             │
├─────────────────────────┴──────────────────────────┤
│        HTTPS / WebSocket  (TLS, password-auth)      │
├─────────────────────────────────────────────────────┤
│            makro-serve  (cmd/gui, Go)               │
│  chat · terminals(xterm) · kanban · artifacts ·     │
│  cost · brain · push(APNs/Bark)                     │
├─────────────────────────────────────────────────────┤
│              Agent Orchestrator (internal/agent)     │
│  tool-calling loop · hooks · send-gate · modes      │
├─────────────────────────────────────────────────────┤
│   Tmux Manager (internal/tmux) · LLM (internal/llm) │
└─────────────────────────────────────────────────────┘
        ▲
        │  (the TUI at repo root is a standalone client
        │   of the same orchestrator/tmux/llm packages)
```

## Project structure

```
makro/
├── main.go                 # Terminal TUI (Bubbletea, split-pane)
├── cmd/gui/                # Desktop app
│   ├── *.go                # makro-serve backend (HTTP/WS server)
│   ├── electron/           # Electron shell (main/preload/afterPack)
│   ├── frontend/           # Vite UI (vanilla JS)
│   └── package.json
├── ios/Makro/              # iOS app (SwiftUI, xcodegen + CocoaPods)
├── internal/
│   ├── agent/              # orchestrator, tools, hooks, notifier
│   ├── brain/              # proactive memory layer
│   ├── llm/                # multi-provider streaming + tool use
│   ├── tmux/               # tmux CLI client + parser
│   ├── apns/               # Apple push notifications
│   ├── notify/             # hook → running-instance forwarding
│   ├── usage/              # prompt-usage tracking
│   ├── config/             # config loading, .claude fallback
│   ├── util/               # shared helpers
│   └── tui/                # Bubbletea TUI (root app)
└── docs/                   # diagrams (kanban-architecture.svg, …)
```

## Build & run

### Desktop app
```bash
# 1. Go backend binary
go build -o cmd/gui/bin/makro-serve ./cmd/gui/

# 2. Frontend bundle
cd cmd/gui/frontend && npm install && npm run build && cd ..

# 3. Electron deps + package the .app (--dir skips the DMG/notarize step;
#    DMG isn't needed for a local install)
cd cmd/gui && npm install && rm -rf release/ && ./node_modules/.bin/electron-builder --dir
# → release/mac-arm64/Makro.app

# Dev mode (hot frontend, spawns makro-serve)
cd cmd/gui && npm run dev
```

### iOS app
```bash
cd ios/Makro
xcodegen generate          # regenerates Makro.xcodeproj from project.yml
pod install                # Azure Speech SDK via CocoaPods
xcodebuild -workspace Makro.xcworkspace -scheme Makro \
  -sdk iphoneos -configuration Debug -derivedDataPath build
```
Open `Makro.xcworkspace` (not the `.xcodeproj`) in Xcode. Configure the Azure Speech key/region and the Makro server URL/password in Settings. Device builds require an Apple Developer signing identity (Xcode → Signing & Capabilities).

### Terminal TUI
```bash
go build -o makro .
./makro             # default split-pane
./makro --phone     # vertical layout
./makro --chat      # headless chat (for testing)
```

## Configuration

Makro reads `~/.makro/config.json` and falls back to `.claude/settings.json` for API key/model:

```json
{
  "llm_provider": "openai",
  "llm_model": "glm-4.7",
  "tmux_mode": "auto"
}
```

| Variable | Description |
|----------|-------------|
| `MAKRO_LLM_PROVIDER` | `anthropic` or `openai` |
| `MAKRO_LLM_API_KEY` | API key (overrides config) |
| `MAKRO_LLM_MODEL` | Model name |
| `MAKRO_TMUX_MODE` | `auto`, `dedicated`, or `shared` |
| `MAKRO_PASSWORD` | Fixed backend password (desktop app; random otherwise) |
| `ANTHROPIC_API_KEY` | Anthropic API key (fallback) |
| `OPENAI_API_KEY` | OpenAI(-compatible) API key (fallback) |

`~/.makro/` also holds `chat.jsonl` (history), `tasks.json` (kanban), and `prompt_usage.db` (cost tracking).

### Role-based routing (`~/.makro/roles.toml`)

Declare named **roles** that bind classes of work to the tmux session that handles them. When you send input that isn't a slash command or `@mention`, Makro asks the LLM which role best matches and routes the task there — instead of spawning yet another anonymous session. Missing or empty `roles.toml` disables routing (Makro behaves as before).

```toml
# ~/.makro/roles.toml — user-global. A project-local ./.makro/roles.toml overrides (see note below).
[[role]]
name        = "makro"
description = "Makro 项目本身的开发、编排、修复"
session     = "makro"          # tmux session that already has a coding agent running

[[role]]
name        = "juli"
description = "juli 项目的开发与维护"
session     = "juli"

[[role]]
name        = "default"
description = "兜底:无法路由时的默认角色"
```

| Field | Required | Notes |
|-------|----------|-------|
| `name` | yes | Identifier-safe (letters, digits, `_`, `-`); unique within a file. |
| `description` | yes | What this role owns. Written verbatim into the routing prompt — be specific. |
| `session` | no | Bound tmux session name. At dispatch time, an empty `session` resolves to the role's `name`. The session **must already exist and have a coding agent running** — Phase 1 does not auto-create sessions (auto-launch is planned for a later phase). |
| `clear_after` | no | `manual` (default), `marker`, `guardian`. Reserved for the reviewer-role auto-clear (Phase 3). |
| `state_file` | no | Per-project state file template, e.g. `reviewer/{project}.md`. Reserved for the reviewer role (Phase 3). |

**Routing cascade:** A-level (config) roles → fallback to a role named `default` (or the first role) when no match clears the confidence threshold (0.5). B-level (runtime-created) roles and graduation are Phase 2. Slash commands and `@mention` always bypass routing.

**Config override:** `~/.makro/roles.toml` (user-global) is loaded first, then `./.makro/roles.toml` (project-local) overrides it. A role name repeated across the two files is **silently last-wins** (the project entry replaces the user entry); duplicates *within one file* are an error.

## TUI key bindings & commands

| Key | Action | | Key | Action |
|-----|--------|-|-----|--------|
| `Ctrl+O` | Focus chat ↔ viewer | | `[` / `]` | Switch sessions (viewer) |
| `Ctrl+D` | Quit | | `Up`/`Down` | Input history (chat) |
| `Ctrl+R` | Reflow layout | | `Enter` | Send |
| `Ctrl+C` | Clear sticky target (×2 = quit) | | `Tab` | Accept autocomplete |

```
@session text    Send text to a tmux session
/create <name>   Create a session
/kill <name>     Kill a session
/list            List sessions
/switch <name>   Switch viewer
/watch <name>    Start guardian for a session
/layout phone    Vertical layout
/help            Show commands
```

## Development

```bash
go test ./...                              # unit tests
go test ./... -tags=integration -v         # integration tests (needs tmux)
go vet ./... && go fmt ./...               # vet + format
```

The pre-commit hook runs `go vet` + `gofmt` + `go test` over the **entire tree** (not just staged files) — format stray Go files before committing.

## Requirements

- Go 1.26+, tmux
- (Desktop app) Node.js, Electron
- (iOS app) Xcode, CocoaPods, an Azure Speech resource (for voice)
- Anthropic or OpenAI(-compatible) API key

## License

Apache-2.0
