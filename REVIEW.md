# PR #6: Add agent stop hook notification for instant idle detection

## Round 1 — 5 comments
| # | File | Issue | Status | Commit |
|---|------|-------|--------|--------|
| 1 | internal/agent/hook_config.go:43 | Preserve unknown Claude settings fields instead of rewriting settings.json through a partial struct. | ✅ fixed | 0a9a554 |
| 2 | internal/agent/hook_config.go:63 | Use a real executable path for the stop hook instead of relying on bare makro being on PATH. | ✅ fixed | 0a9a554 |
| 3 | internal/agent/hook_config.go:73 | Preserve the existing settings.json file mode instead of forcing 0644. | ✅ fixed | 0a9a554 |
| 4 | internal/agent/tools/agent_alive.go:170 | Limit wrapped-agent detection to executable chains so command arguments do not create false positives. | ✅ fixed | 0a9a554 |
| 5 | internal/agent/tools/wait_until_idle.go:59 | Remove session-wide waiter resets so concurrent waits on the same session do not interfere with each other. | ✅ fixed | 0a9a554 |

---

# Code Review: v0.4.0 → v0.4.8

> Reviewer: Copilot
> Scope: all changed files between tag v0.4.0 and tag v0.4.8
> Files reviewed: `internal/tmux/keepalive.go` (new), `internal/tmux/client.go`, `internal/agent/orchestrator.go`, `internal/agent/notifier.go`, `internal/agent/tools/wait_until_idle.go`, `internal/agent/tools/send.go`, `internal/tui/chat.go`, `internal/tui/app.go`, `internal/tui/messages.go`, `main.go`

## 🔴 High (3 issues)

| ID | File | Lines | Issue | Status | Commit |
|----|------|-------|-------|--------|--------|
| H-1 | `internal/tmux/keepalive.go` | 72–80 | **PTY fd 泄漏。** `cmd.Wait()` goroutine 从 map 删除条目时未调用 `entry.ptmx.Close()`。进程自然退出后 ptmx 成为孤儿 fd，只能等 GC finalizer 回收。session 频繁创建销毁时可能耗尽文件描述符。修复：在 `delete(k.clients, sessionName)` 后加 `entry.ptmx.Close()`。 | ✅ fixed | uncommitted |
| H-2 | `internal/tmux/keepalive.go` | 57–68 | **PTY master 未 drain，attach 进程会因缓冲区满被踢掉。** `pty.Start` 后没有任何 goroutine 读取 `ptmx`。tmux 连接新客户端时会立即渲染全屏内容（写入 PTY slave），内核 PTY 缓冲区（Linux 4KB / macOS 64KB）写满后 tmux 会断开该客户端 → keepalive 退出 → `attached=0` → 500ms 重建循环。这是修复 TMUX env filter 之后**仍可能存在的根本原因**。修复：`pty.Start` 后立即 `go io.Copy(io.Discard, ptmx)`。 | ✅ fixed | uncommitted |
| H-3 | `internal/tui/chat.go` | 244, 338 | **队列消息被追加两次到历史记录（内存 + 持久化文件）。** 消息入队时 line 244 调用一次 `appendMessage("user", text)`；出队时 line 338 又调用一次。聊天窗口里同一条消息出现两次，`~/.makro/chat.md` 也重复写入。`TestChatModelQueueSubmitWhileWorking` 只覆盖入队路径，出队路径未测试。修复：删除 line 338 的 `c.appendMessage("user", text)`。 | ✅ fixed | uncommitted |

## 🟠 Medium (4 issues)

| ID | File | Lines | Issue | Status | Commit |
|----|------|-------|-------|--------|--------|
| M-1 | `internal/agent/orchestrator.go` | 471–477 | **`isRetryableError` 裸子串匹配产生误判。** `strings.Contains(lower, "500")` 会匹配 `"timeout after 500ms"`、`"EOF after 5029 bytes"` 等非 LLM 错误，导致错误触发 3 分钟重试。修复：改为带 HTTP 语境的模式如 `"status 429"`、`"status 500"`。 | ✅ fixed | uncommitted |
| M-2 | `main.go` | 447–458 | **`chat`/`send` 子命令多单词 content 静默截断。** `os.Args[3]` 只取第三个参数，`makro chat assistant hello world` 只发送 `"hello"`，无任何报错。修复：`strings.Join(os.Args[3:], " ")`。 | ✅ fixed | uncommitted |
| M-3 | `main.go` | 184–192 | **`os.Executable()` 失败后仍用空路径调用 `EnsureStopHook`。** `executablePath` 为 `""` 时会向 Claude 配置写入一个空路径的 stop hook，导致所有后续 Claude Code session 的 stop hook 失效。修复：改为 `if executablePath, err := os.Executable(); err != nil { log } else if err := agent.EnsureStopHook(...) { log }`。 | ✅ fixed | uncommitted |
| M-4 | `main.go` | 152–157 | **`--chat` 模式下 `onChat`/`onSession` 回调未注册，`makro chat` 和 `makro send` 静默无效。** `--chat` 分支只调用 `notifier.Start(ctx)`，回调注册代码（lines 169–174）只在 TUI 路径执行。修复：在 chat 模式下也注册回调，或将回调注册提前到分支之前。 | ✅ fixed | uncommitted |

## 🟡 Low (4 issues)

| ID | File | Lines | Issue | Status | Commit |
|----|------|-------|-------|--------|--------|
| L-1 | `internal/tmux/keepalive.go` | 38–75 | **`Add()` 存在 TOCTOU 竞态。** 先检查 map 存在性并释放锁，`pty.Start()` 耗时期间锁已释放，并发的 `Close()`（来自 `Stop()`）可能清空 map，之后 `Add()` 再插入一个永远不会被清理的条目。日常 `pollLoop` 单 goroutine 调用不触发，但 shutdown 时序下有风险。修复：加入 `closed bool` 字段，`pty.Start` 后重新检查。 | ✅ fixed | uncommitted |
| L-2 | `internal/tui/chat.go` | 81, 252 | **`pendingQueue` 无上限。** LLM 长时间不响应时用户可无限入队，内存持续增长。建议设置上限（如 50 条）并在达到阈值时显示警告。 | ✅ fixed | uncommitted |
| L-3 | `internal/agent/tools/send.go` | 28–44 | **`sendMultiLine` 静默丢弃末尾 `\n`，行为与 `sendSingleLine` 不一致。** | ✅ fixed | N-1 合并时消除 |
| L-4 | `internal/agent/orchestrator.go` | 329 | **LLM 重试等待固定 3 分钟，无 jitter。** 多实例同时出错时会在同一时刻打流量。修复：添加 0–30s 随机抖动。 | ✅ fixed | uncommitted |

## ⚪ Nit (4 issues)

| ID | File | Lines | Issue | Status | Commit |
|----|------|-------|-------|--------|--------|
| N-1 | `internal/agent/tools/send.go` | 13–44 | `sendSingleLine` 和 `sendMultiLine` 产生的 payload 结构完全相同，可合并为一个函数，同时消除 L-3 的行为不一致。 | ✅ fixed | previous |
| N-2 | `internal/tmux/keepalive.go` | 37 | `Add(_ context.Context, ...)` 用 `_` 丢弃 context，但调用方仍传入 5 秒超时的 ctx，接口具有误导性。修复：去掉参数，更新调用方。 | ✅ fixed | uncommitted |
| N-3 | `internal/tui/app.go` | 216–219 | `ExternalChatMsg` 处理器直接调用指针方法 `a.chat.AppendMessage()`，绕过标准的 `ChatModel.Update(msg)` 路径。修复：改为通过 `ChatModel.Update` 路由。 | ✅ fixed | uncommitted |
| N-4 | `main.go` | 392–418 | `runSocketCommand` 编程错误（JSON 序列化失败、`SetDeadline` 失败）使用 `os.Exit(0)` 掩盖。修复：改为 `os.Exit(1)` 并输出错误信息。 | ✅ fixed | uncommitted |

---

# Code Review: Round 2 — fix verification

> Reviewer: Copilot
> Scope: verification of all 15 fixes from the previous review section; new issues introduced by those fixes
> Date: 2026-05-05

## Fix Verification Summary

All 15 issues verified as correctly fixed, with one caveat on N-3 (see R2-N-2 below).

| ID | Verified? | Notes |
|----|-----------|-------|
| H-1 | ✅ | `entry.ptmx.Close()` called in `cmd.Wait()` goroutine before `delete` |
| H-2 | ✅ | `go io.Copy(io.Discard, ptmx)` present after `pty.Start` |
| H-3 | ✅ | `appendMessage("user", text)` removed from the `"done"` dequeue path; queued messages are only appended on entry |
| M-1 | ✅ | `isRetryableError` now uses `"status 429/500/502/503/504"` patterns; no more bare `"500"` substring |
| M-2 | ✅ | Both `"chat"` and `"session"` payloads use `strings.Join(os.Args[3:], " ")` |
| M-3 | ✅ | `EnsureStopHook` guarded by `else if` so an empty executable path never reaches it |
| M-4 | ✅ | `notifier.OnSession` moved before the `*chatMode` branch; both modes now receive session messages |
| L-1 | ✅ | `closed bool` field added; `pty.Start` followed by a re-check of `k.closed` under lock |
| L-2 | ✅ | Queue capped at 50; overflow shows inline warning (see R2-N-1 for formatting nit) |
| L-3 | ✅ | `sendSingleLine`/`sendMultiLine` collapsed into single `sendText` |
| L-4 | ✅ | Retry wait is `3*time.Minute + rand.Intn(30)*time.Second` |
| N-1 | ✅ | (merged with L-3) |
| N-2 | ✅ | `Add` takes `sessionName string` only; context parameter removed |
| N-3 | ✅ | Fixed in R2-N-2: app.go case removed, falls through to ChatModel.Update |
| N-4 | ✅ | `json.Marshal` and `SetDeadline` failures now exit with code 1 and print to stderr |

## 🟡 Low (1 issue)

| ID | File | Lines | Issue | Status | Commit |
|----|------|-------|-------|--------|--------|
| R2-L-1 | `internal/tmux/client.go` | 260–262 | **Misleading log on first poll of unattached session.** `pollSessions` logs `"unattached, removing stale keepalive"` for every session where `attached == "0"`, including sessions that never had a keepalive entry. `Remove()` is a no-op in that case, but the log implies a stale client was cleaned up when none existed. Fix: only log if `Remove()` actually deleted an entry (return bool from `Remove`), or change the log to fire after Remove only when an entry existed. | ✅ fixed | uncommitted |

## ⚪ Nit (3 issues)

| ID | File | Lines | Issue | Status | Commit |
|----|------|-------|-------|--------|--------|
| R2-N-1 | `internal/tui/chat.go` · `internal/agent/orchestrator.go` | 255–258 · imports | **`gofmt` violations.** `gofmt -l .` reports two files unformatted: (1) `chat.go` lines 255–258 — the L-2 queue-cap block has inconsistent tab depth (body uses extra tab, closing `}` and the following `c.pendingQueue` line are over-indented); (2) `orchestrator.go` — `"math/rand"` appears before `"log"` in the import block, which `gofmt` reorders. Fix: run `gofmt -w internal/tui/chat.go internal/agent/orchestrator.go`. | ✅ fixed | uncommitted |
| R2-N-2 | `internal/tui/app.go` · `internal/tui/chat.go` | 216–218 · 160–161 | **N-3 fix incomplete — `ExternalChatMsg` handler in `ChatModel.Update` is dead code.** `app.go:216–218` still catches `ExternalChatMsg` and calls `a.chat.AppendMessage(...)` directly before routing to `ChatModel.Update`. The new `case ExternalChatMsg:` added to `chat.go:160–161` is therefore unreachable. Functionally equivalent, but creates a confusing split: two handlers for the same message type, only one of which runs. Fix: remove the explicit `case ExternalChatMsg:` from `app.go` so it falls through to the `default:` branch, which already routes to `ChatModel.Update`. | ❌ not fixed | — |
| R2-N-3 | `internal/agent/hook_config.go` | 147–149 | **`buildStopHookCommand` empty-path fallback is now dead code.** Before M-3, `EnsureStopHook` could be called with `executablePath == ""`, so the `if command == "" { command = "makro" }` guard was load-bearing. After M-3, the call-site is guarded by `else if`, so an empty path can never reach this function. The dead branch is harmless but misleads future readers into thinking empty-path calls are still possible. Fix: remove the fallback and document that `executablePath` must be non-empty. | ✅ fixed | uncommitted |

---

# Code Review: Round 3 — fix verification

> Reviewer: Copilot
> Scope: verification of R2-L-1, R2-N-1, R2-N-2, R2-N-3; new issues introduced
> Date: 2026-05-05

## Fix Verification

| ID | Verified? | Evidence |
|----|-----------|---------|
| R2-L-1 | ✅ | `Remove()` now returns `bool`; `client.go:261–263` wraps the log in `if c.keepAlive.Remove(name) { ... }` |
| R2-N-1 | ✅ | `gofmt -l .` returns empty — both files clean |
| R2-N-2 | ❌ | `app.go:216–218` **unchanged** — still has `case ExternalChatMsg:` calling `AppendMessage` directly. The `case ExternalChatMsg:` added to `chat.go:160–161` is still dead code (never reached). Only `chat.go` was modified; `app.go` has no diff against HEAD. |
| R2-N-3 | ✅ | `buildStopHookCommand` reduced to single `return shellQuote(executablePath) + fsNotifyHookSuffix` |

**Overall:** `go vet ./...` clean, `go build ./...` clean, `go test ./...` all pass.

## ⚪ Nit (1 remaining issue)

| ID | File | Lines | Issue | Status | Commit |
|----|------|-------|-------|--------|--------|
| R2-N-2 | `internal/tui/app.go` | 216–218 | **`ExternalChatMsg` still intercepted by `AppModel.Update` before `ChatModel.Update`.** The fix added a handler in `chat.go:160–161` but left the `case ExternalChatMsg:` block in `app.go` intact, so the chat.go handler remains dead code. Fix: delete the `case ExternalChatMsg:` block from `app.go` (lines 216–218) entirely — the message will then fall through to the `default:` branch and be routed to `ChatModel.Update` where the new handler fires. | ✅ fixed | uncommitted |

No new issues introduced by the three correctly applied fixes.

---

# Code Review: Round 4 — final verification

> Reviewer: Copilot
> Scope: verification of R2-N-2 fix; overall sign-off
> Date: 2026-05-05

## Fix Verification

| ID | Verified? | Evidence |
|----|-----------|---------|
| R2-N-2 | ✅ | `app.go:216–218` `case ExternalChatMsg:` block deleted. `ExternalChatMsg` now falls through to the outer `switch msg.(type)` `default:` branch → `a.chat.Update(msg)` → `chat.go:160–161` handler (now reachable). |

**Overall health:** `go vet ./...` clean · `gofmt -l .` clean · `go test ./...` all pass.

## Conclusion

All issues from the v0.4.0 → v0.4.8 review (15 items) and subsequent rounds (R2: 4 items, R3: 1 carry-over) are **fully resolved**. No new issues introduced. REVIEW.md may be deleted before merge.
