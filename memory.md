# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Vision

**Makro** is a Go application that manages multiple coding agents (Claude Code, Copilot, etc.) through a split-pane terminal UI. The left pane is a chat interface for orchestrating agents; the right pane renders tmux sessions. Users `@mention` sessions to switch context and issue commands to different coding agents. Both panes accept input simultaneously via Tab focus toggle.

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│                    Makro (main)                    │
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
- **Dedicated tmux server**: Runs on `~/.makro/tmux.sock`, isolated from user's tmux.
- **Chat persistence**: Markdown format at `~/.makro/chat.md`, append on every message.
- **LLM model from .claude**: Reads `.claude/settings.json` for model preference, falls back to provider defaults.

## Go Project Structure

```
makro/
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
go build -o makro .
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

## iOS App + Azure Speech Voice Call — Lessons Learned

The iOS app lives at `ios/Makro/` (separate from the Go desktop app). It uses
xcodegen + CocoaPods + xcodebuild (NOT SPM). These are the pitfalls hit while
integrating Azure Speech for bidirectional voice calls, with concrete root
causes so the same mistakes are not repeated.

### Azure Speech free-tier (F0) constraints

- **STT REST API is unavailable on F0.** Only the native SDK can use the free
  5h/month STT quota. The REST short-audio endpoint returns errors on F0.
  TTS REST *does* work on F0, but the app uses the SDK for both STT and TTS.
- **F0 quota has no "remaining" API.** Meter locally
  (`SpeechQuotaTracker`): TTS counts characters (cap 480k, F0 allows 500k),
  STT counts seconds (cap ~4.7h, F0 allows 5h). Reset monthly. Hard-caps sit
  below the real limits so we stop *before* Azure rejects.
- **Quota-exhausted signal:** TTS cancellation with `errorCode == .forbidden`
  (= 4) means "F0 free quota exhausted." `.authenticationFailure`,
  `.tooManyRequests`, `.connectionFailure` are related access failures.
  `.endOfStream` = intentional stop — do NOT report it as an error.

### Azure SDK ObjC→Swift bridging signatures (verified against SDK headers)

These differ from intuition — always check the SDK headers under
`Pods/.../MicrosoftCognitiveServicesSpeech.framework/Headers/`:

- `SPXSpeechSynthesizer` init **requires** an explicit `audioConfiguration:`
  argument (unlike the docs that show it optional).
- TTS event handlers need **explicit parameter type annotations**:
  `(_: SPXSpeechSynthesizer, evt: SPXSpeechSynthesisEventArgs)` — the closure
  types can't be inferred.
- `evt.result.audioData` is `Data?` (optional) and lives on
  `evt.result`, not `evt` directly.
- STT `SPXSpeechRecognitionCanceledEventArgs`: use `evt.reason`
  (`SPXCancellationReason` enum) and `evt.errorDetails`. There is no
  `errorCode` you can compare to a raw Int.
- TTS result has **no** `errorCode`/`errorDetails` — extract them via
  `SPXSpeechSynthesisCancellationDetails(fromCanceledSynthesisResult:)`
  (note the `from` prefix is kept in Swift — the ObjC `init` label is NOT
  stripped).
- Enum cases in Swift: `SPXResultReason.synthesizingAudioCompleted`,
  `SPXCancellationReason.error` / `.endOfStream`,
  `SPXCancellationErrorCode.forbidden`.

### xcodegen + CocoaPods workflow (the build-was-grey trap)

**Correct order:** `xcodegen generate` → `pod install` → open
`Makro.xcworkspace` (NOT `.xcodeproj`). Build with
`xcodebuild -workspace Makro.xcworkspace -scheme Makro`.

- **`pod install` after a bare xcodegen wipes the shared scheme.** The
  `Makro` scheme disappears and the build button goes grey. Fix: re-add
  `Makro.xcodeproj/xcshareddata/xcschemes/Makro.xcscheme` (target blueprint
  ID `3FF398F9834B5FD99FA0767F` is stable across regens). **Every `xcodegen
  generate` re-run deletes this file** — re-create it after.
- New Swift files are picked up automatically by xcodegen (it scans the
  `Makro/` sources dir), but you still must re-run `xcodegen generate` for
  them to land in the project.
- The makro-build skill's iOS section documents the full sequence.

### STT continuous-recognition behavior

- **`recognized` fires once PER SENTENCE, not per turn.** Treating it as
  end-of-turn truncates the user to their first sentence ("hello can you hear
  me" → "hello"). Accumulate results; only end the turn on user stop or
  silence timeout.
- **Fast tap-to-stop loses content** if you only deliver `accumulatedText` —
  a `recognized` event may not have fired yet. Fall back to the last partial
  transcript shown in the UI (`currentPartialText()`).
- The STT silence interval must be generous (2.5s) — 1.5s cuts off between
  sentences. In call (continuous) mode, silence delivers the turn but does
  NOT stop the recognizer; only `stopListening()` tears it down.
- Guard `finishListening`/stop paths against re-entrancy — the silence timer
  and a manual stop can race and double-fire.

### TTS gotchas

- **Don't pin a custom voice name** like `zh-CN-XiaoxiaoMultilingual` — it
  can be unavailable per-region and fails synthesis on first use. Let the
  service pick the default zh-CN voice.
- `speakText()` blocks until synthesis completes — run it on a background
  queue or the UI freezes. Check `result.reason ==
  .synthesizingAudioCompleted` on return.
- `feedAudio` must **copy** PCM bytes into the `AVAudioPCMBuffer`, never
  alias the `Data`'s pointer (the Data may be freed before playback).

### Audio session + background

- Lock-screen listening needs the `audio` background mode in Info.plist **and**
  `AVAudioSession.setCategory(.playAndRecord, mode: .voiceChat)`. The
  `.playback` category gets suspended when locked.
- `.voiceChat` mode enables the OS's built-in echo cancellation — the
  assistant's TTS won't be captured back by the mic. Prefer this over
  manual suspend/resume, though suspend/resume is kept as a backstop.
- Call `configureAudioSession()` BEFORE `startContinuousRecognition()`, not
  only at TTS playback, so the mic opens under the right category.

### Lock-screen controls (Now Playing)

- Use `MPNowPlayingInfoCenter` + `MPRemoteCommandCenter`, NOT CallKit (too
  heavy for an app-initiated session — no incoming-call UX needed).
- Map `togglePlayPauseCommand` → mute/unmute, `nextTrackCommand` → hang up.
  Disable the rest (`playCommand`, `pauseCommand`, etc.) for a clean control
  set, or they clutter the lock screen.
- Clear `MPNowPlayingInfoCenter.nowPlayingInfo = nil` and
  `removeTarget(nil)` on all commands when the call ends, or the card
  lingers.

### "Typed messages should not be spoken"

- A persistent `isVoiceMode` flag is wrong — once on, it never turns off and
  every reply (including typed-message replies) gets TTS'd. Use a one-shot
  `expectSpokenReply` flag: armed only when STT delivers text, cleared after
  playback ends/interrupts. In call mode, `isInCall` keeps replies armed for
  the whole call; typed messages outside a call never set it.

### Git / pre-commit gotchas

- The pre-commit hook runs `go vet` + `gofmt` + `go test` over the **entire
  working tree**, not just staged files. Unrelated unformatted Go files
  (even untracked ones) will block a pure-iOS commit. `gofmt -w` them first;
  they don't need to be staged.
- Keep iOS commits clean: stage only the iOS files. The Go desktop changes
  (`cmd/gui/*`, `internal/*`) are a separate concern and should be committed
  separately.
- A heredoc commit message with apostrophes (`assistant's`) breaks the shell.
  Use `git commit -F - <<'EOF' ... EOF` (quoted delimiter) instead of
  `git commit -m "$(...)"`.
