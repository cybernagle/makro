# Makro 发送链路分析(函数级,grep 验证 2026-07-07)

## 已验证的调用图

### SendConfirmed 的调用方(3个)
| 调用方 | 位置 | 场景 |
|---|---|---|
| `chatSvc.ConfirmPlan` | chat_service.go:783 | iOS 语音派发 |
| `chatSvc.SendToSession` | chat_service.go:817 | iOS终端/kanban 的统一 wrapper |
| `send_to_session` 工具 Execute | send_to_session.go:66 | orchestrator/chat 聊天派发 |

### sendText 的调用方(4个)
| 调用方 | 位置 | 场景 |
|---|---|---|
| `SendConfirmed` 内 | send_to_session.go:143 | 所有经 SendConfirmed 的路径 |
| `DirectSend` | send_to_session.go:250 | manual @session / notifier OnSession |
| `relay_message` 工具 | relay_message.go:40 | 跨 session 消息转发 |
| `restore_context` 工具 | context.go:140 | 恢复 session 上下文 |

### SendToSession 的调用方(2个)
| 调用方 | 位置 | 场景 |
|---|---|---|
| `sessionSendHandler` | server.go:462 | POST /api/sessions/<n>/send(iOS终端) |
| `handleTaskSend` | server.go:1021 | POST /api/tasks/<id>/send(kanban) |

### 其他关键调用
| 调用 | 位置 | 说明 |
|---|---|---|
| `SendMessage → orch.ProcessInput` | chat_service.go:657 | 聊天入口(POST /api/chat) |
| `xtermWSHandler → ptmx.Write` | server.go:757 | 桌面终端 PTY(不走 sendText) |
| `SendToSession fallback → sendToTmuxSession` | chat_service.go:813 | tc/notifier nil 时(legacy inline send-keys) |

## 三条发送通道

### 🟢 主路径:SendConfirmed → sendText → tc.Exec(send-keys)
所有以下路径最终到 `sendText` → `tc.Exec(SendKeysLiteralCmd ; SendEnterCmd)`(send.go:27):
- **iOS 终端输入**:TerminalViewModel.send → POST /send → sessionSendHandler → SendToSession → SendConfirmed → sendText
- **聊天(iOS+桌面)**:ChatViewModel/sendChat → POST /api/chat → SendMessage → ProcessInput → [send_to_session 工具] → SendConfirmed → sendText
- **kanban**:APIClient.sendTask → POST /tasks/<id>/send → handleTaskSend → SendToSession → SendConfirmed → sendText
- **voice 派发**:ConfirmPlan → SendConfirmed → sendText
- **DirectSend / relay_message / restore_context** → sendText 直达(不经 SendConfirmed)

### 🔴 桌面终端交互:PTY 字节流
`main.js term.onData → ws.send → /ws/xterm → xtermWSHandler → sess.ptmx.Write → PTY slave → tmux new-session -A(tmux client) → tmux server → pane`

**不走 sendText、不发 send-keys**。字节流逐字符进 PTY → tmux client。

### 🟡 legacy 兜底:inline send-keys
`SendToSession(tc/notifier nil 时) → sendToTmuxSession → tmuxSendKeys + tmuxSendEnter`(server.go inline exec.Command)

本质和 sendText 一样(原子 send-keys),只是 inline 拼字符串。

## 关键结论

1. **Enter-loss 在 `sendText`(send.go:9)**:tmux `send-keys` 的 Enter 在 agent TUI redraw 时被吞。影响 **所有 🟢 路径**(iOS终端/聊天/kanban/voice/DirectSend/relay/restore)。

2. **🔴 PTY 不受 Enter-loss 影响**:PTY 发送是字节流(ptmx.Write),不发 send-keys,不存在"Enter 被 redraw 吞"这个机制。PTY 的病是连接层(多 client / resize / WS 断连)。

3. **桌面 vs iOS 在 agentic(tool)层面没区别**:两端聊天都 → /api/chat → SendMessage → orchestrator → send_to_session → SendConfirmed → sendText → tc.Exec。tool 执行是 client-agnostic 的。

4. **唯一的客户端差异在终端输入**:iOS 终端 = SendConfirmed(send-keys),桌面终端 = PTY(字节流)。两条不同的后端函数。

5. **显示与发送是独立的**:PTY 把它们耦合在一个连接里;snapshot + send-keys 天然解耦(显示走 capture-pane,发送走 send-keys,不同端点)。

## 相关文件
- 发送核心:`internal/agent/tools/send.go`(sendText)、`send_to_session.go`(SendConfirmed, DirectSend)
- Wrapper:`cmd/gui/chat_service.go`(SendToSession, SendMessage, ConfirmPlan)
- Handlers:`cmd/gui/server.go`(sessionSendHandler, handleTaskSend, xtermWSHandler)
- PTY:`cmd/gui/terminal_service.go`(startPtySession)、`server.go`(xtermWSHandler, ptySession)
- inline legacy:`cmd/gui/server.go`(sendToTmuxSession, tmuxSendKeys, tmuxSendEnter)
- 配套报告:`docs/send-channel-unification.md`、`docs/tmux-paths.svg`
- Artifact(手机版):`~/.makro/artifacts/dev/send-chain.html`、`tmux-paths.html`
