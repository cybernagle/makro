# 调研报告:iOS 端消息发送稳定 vs 桌面端不稳定 — 机制对比与统一方案

**日期**:2026-07-06　·　**范围**:`cmd/gui/server.go`(xterm/snapshot/send handlers)、`cmd/gui/frontend/src/main.js`(桌面)、`ios/Makro/Makro/{ChatViewModel,TerminalViewModel}.swift`、`internal/agent/tools/{send_to_session,send}.go`

---

## TL;DR

iOS 和桌面 **聊天(chat)发送路径完全相同**(都 `POST /api/chat`),所以"消息发送"的差异**只可能出在终端输入(terminal input)那条路上**——而两条路的设计完全不同:

| | 桌面 | iOS |
|---|---|---|
| 终端输入通道 | **`/ws/xterm/<name>` WebSocket → PTY → `tmux new-session -A`**(有状态,长连接,真 PTY) | **`POST /api/sessions/<name>/send` → `SendConfirmed` → 原子 `tmux send-keys -l <text> ; send-keys Enter`**(无状态,HTTP) |
| 终端显示通道 | PTY stdout → WS 二进制帧(xterm.js 渲染) | `/ws/snapshot/<name>`(周期 `capture-pane`)+ `/api/sessions/<name>/capture` |

桌面端"不稳定"的根因是**那套 PTY + tmux-client 模型**:多客户端冲突、窗口尺寸协商、WS 生命周期与重连——都是 iOS 那套无状态快照+原子发送模型里**不存在**的问题。

---

## ① 两条发送链路(带代码位置)

### 桌面(`cmd/gui/frontend/src/main.js` + `server.go`)
- **聊天**:`sendChat()` → `fetch POST /api/chat`(`main.js:165/180`)。和 iOS 一样,稳定。
- **终端输入**:xterm.js `term.onData(data → ws.send(bytes))`(`main.js:385-388`)→ **WebSocket `/ws/xterm/<name>`** → 后端 `xtermWSHandler`(`server.go:699`)→ `sess.ptmx.Write(data)`(`server.go:757`)。
- **PTY 来源**:`startPtySession(name, cols, rows)`(`server.go:36`)跑的是 `tmux new-session -A -s <name> -x <cols> -y <rows>`,用 `pty.StartWithSize`(`server.go:52`)塞进一个真 PTY。也就是说每个桌面终端 tab = **一个独立 PTY 进程,里面跑着一个 tmux client(attached 到该 session)**。
- 先调 `detachStaleClients(name)`(`server.go:46`)把旧 client 踢掉,再 attach 新的。

### iOS(`ios/Makro/Makro/TerminalViewModel.swift` + `server.go`)
- **聊天**:`ChatViewModel.send` → `APIClient.sendChat` → `POST /api/chat`。同桌面。
- **终端输入**:`TerminalViewModel.send(text:)` → `POST /api/sessions/<name>/send` → `sessionSendHandler` → `chatSvc.SendToSession` → `tools.SendConfirmed` → `sendText`(原子 `send-keys -l <body> ; send-keys Enter`,`internal/agent/tools/send.go`)。
- **终端显示**:`/ws/snapshot/<name>`(`wsSnapshotHandler`,`server.go:776`)周期 `tmux capture-pane -t <name> -p` 推 JSON 帧;`TerminalViewModel.refresh()` 拉 `/api/sessions/<name>/capture`。

---

## ② 为什么桌面不稳定(具体机制)

PTY + tmux-client 模型带来 iOS 模型里**根本不存在**的几类故障:

1. **多 client 冲突 / `detachStaleClients` 竞争**(`server.go:46`)
   - 同一个 tmux session 可以被多个 client attach(开两个桌面 tab、桌面+某个遗留 client)。`new-session -A` 会再挂一个 client;`detachStaleClients` 试图先踢旧的——但"踢"和"挂"之间有窗口,且把别的正在用的 client 踢掉本身就是破坏性的。
   - iOS 的 snapshot 模式**不 attach 任何 client**(`capture-pane`/`send-keys` 是非 attach 的命令式操作),所以永远不会有 client 数量/冲突问题。

2. **窗口尺寸协商**
   - `new-session -A -x <cols> -y <rows>`(`server.go:47-49`)强制设尺寸;多 client 尺寸不同 → tmux 的 `window-size` 策略(`latest`/`aggressive-resize`)会让 pane 反复 resize → coding-agent 的 TUI(claude 等)触发**全屏重绘**。重绘期间接收到的按键/`send-keys` 容易被 alternate-screen/重绘吞掉(正是 CLAUDE.md "Known Bugs" 里那个 Enter-loss 的诱因之一)。
   - iOS snapshot 不 attach、不改尺寸,agent 的 TUI 尺寸由真实 tmux pane 决定,稳定。

3. **WebSocket 生命周期 / 状态丢失**
   - xterm 路是有状态长连接:WS 一断(Electron 切后台、休眠、网络抖动、tmux server 重启),`ws.onclose`(`main.js:380`)直接把 tab 标记 `[disconnected]` 移除;重连 = 重新 `new-session -A` + 新 PTY,期间**所有击键丢失**(`term.onData` 里 `if (ws.readyState === OPEN)` 不是 OPEN 就静默丢)。
   - iOS snapshot 是**无状态拉取**:每次 `refresh()` 是独立 HTTP,WS 断了重连即恢复,没有"丢失输入"的概念(输入走独立 POST)。

4. **PTY 进程管理**
   - 每个桌面 tab 多一个 `tmux new-session -A` 子进程 + ptmx。tab 频繁开关 → 进程/PTY 起停 → `Close()`(`server.go:59`)路径一旦不干净(僵尸 client、fd 泄漏)会累积。iOS 不开 PTY,零进程负担。

> 注意:聊天(/api/chat)那条路两边一样、都稳; orchestrator → session 的 `send_to_session`(SendConfirmed,原子 send-keys + Enter 补救)也是**两边共用**的,它有自己的 Enter-loss 老毛病(长消息 bracketed paste 时),但那是**共享问题**,不是 iOS/桌面差异。

---

## ③ 为什么 iOS 稳定(对应每一条)

- **无 attach**:snapshot 用 `capture-pane`、send 用 `send-keys`,都是"对一个已存在的 session 下命令",不挂 client → 无 client 冲突、无强制尺寸、无 detach 副作用。
- **无状态 HTTP**:输入是 `POST /send`(fire-and-forget + SendConfirmed 的原子+补救),显示是周期拉取——没有需要保活的长连接,断网/后台恢复后下一帧就自愈。
- **无 PTY 进程**:不在 Makro 里起 tmux client 进程,故障面更小。

代价:iOS snapshot 不是"真终端",没有逐字节实时回显(是 ~10fps 的 capture-pane 快照)。但对接 coding-agent(主要是**看** agent 输出、偶尔发短指令)这个场景,这个代价完全可接受,换来的是稳定性。

---

## ④ 统一方案:两边都走"无状态快照 + 原子发送"通道

**目标**:桌面端弃用 `xterm.js + /ws/xterm + PTY` 这条有状态链路,改用 iOS 那条 **snapshot-view + SendConfirmed-send** 通道,让两个客户端共用同一套协议 + 同一份后端代码。

### 4.1 统一后的目标架构

```
iOS / 桌面 (统一)
  显示: GET /api/sessions/<n>/capture  +  WS /ws/snapshot/<n>   (capture-pane 快照)
  输入: POST /api/sessions/<n>/send  →  SendConfirmed  (原子 send-keys + Enter 补救)
后端:  无 PTY、无 attach、命令式 capture-pane/send-keys
```

`/ws/xterm` + `startPtySession` + `ptySession` 这整块**下线**(桌面专用代码删除)。

### 4.2 桌面端要改的

1. **终端渲染**:`xterm.js` 换成"snapshot 文本视图"——直接渲染 `capture-pane` 的纯文本(像 iOS 的 `TerminalDetailView` 那样 `Text(ANSI.clean(content))`),而不是 xterm.js 的逐字节终端模拟。
   - 入口:连 `/ws/snapshot/<name>` 收 `{content}` 帧 + 进 tab 时拉一次 `/capture` 立刻填充。
   - 纯文本渲染丢掉了 xterm 的光标/全彩 ANSI 细节,但换来稳定;若一定要 ANSI,后端 `capture-pane` 已带 ANSI 序列,前端做一个轻量 ANSI→HTML/SPAN 渲染即可(不依赖 xterm.js 的终端状态机)。
2. **输入**:`term.onData` 那条 WS 删掉;输入框(已有的 `inputBar`)走 `POST /api/sessions/<name>/send`(和 iOS 完全一样)。
3. **删除**:`xterm.js`/`@xterm/*` 依赖、`/ws/xterm` 前端连接、后端 `xtermWSHandler` + `startPtySession` + `ptySession` + `detachStaleClients` + `setWinsize`。
   - 同步:kanban 的 send-to-session、@mention、role-routing 全都已经在用 `SendConfirmed`,不受影响。

### 4.3 后端要改的

- 删 `xtermWSHandler`、`startPtySession`、`ptySession`、`detachStaleClients`、`setWinsize`、`/ws/xterm/` 路由。
- `/ws/snapshot/` + `/api/sessions/<n>/(capture|send)` **保留并成为唯一终端通道**(它们已经是 iOS 在用的)。
- `SendConfirmed` 是统一发送的单一入口(已经是了)。

### 4.4 收益

- **稳定性**:消除桌面端的 PTY/多-client/WS 生命周期三类故障源,桌面获得和 iOS 一样的稳定性。
- **代码量**:`xtermWSHandler` + PTY 一整套(进程、winsize、stale-client、二进制帧泵)全部下线,后端只剩命令式 capture/send。
- **一致性**:两个客户端同一协议、同一发送语义、同一份 bug 修复(改 `SendConfirmed` 两边都受益)。

### 4.5 风险 / 取舍

- **失去真终端体验**:xterm.js 的实时逐字节回显、完整 ANSI、交互式 TUI 应用(vim/htop 等)在 snapshot 模式下体验下降(快照 + 发文本)。但 Makro 的核心用途是**监控/驱动 coding agent**,不是当通用终端——这个取舍是合理的。
- **snapshot 帧率**:`/ws/snapshot` 默认 ~10fps,快速滚屏可能丢中间帧;够用于"看 agent 干活 + 偶尔发指令",不适合高频交互。
- **输入**:`POST /send` 是"整段发"(SendConfirmed),适合发整条指令;不适合逐字符交互(补全、readline 微调)。对 coding-agent 场景够用。

---

## ⑤ 推荐落地顺序(小步、可回退)

1. **后端**:确认 `/ws/snapshot` + `/api/sessions/<n>/(capture|send)` 在桌面浏览器/Electron 里能跑通(它们本来就是 HTTP/WS,跨客户端通用)。
2. **桌面前端**:新增一个 snapshot-view 终端组件(先不删 xterm,做开关 `?view=snapshot`),实测稳定性。
3. **对比**:同一 session 同时开 xterm tab 和 snapshot tab,验证 snapshot 不再有"丢 Enter / 断连 / 尺寸抖动"。
4. **切换 + 下线**:snapshot 稳定后,把默认终端改成 snapshot,删掉 xterm 路径(前端依赖 + 后端 `/ws/xterm` + PTY 一整套)。
5. **统一发送**:确认所有发送(chat、@mention、kanban send、role-routing dispatch、iOS/桌面终端输入)都收敛到 `SendConfirmed` 单一入口(目前大部分已经是)。

---

## 附:关键代码位置速查

| 关注点 | 文件:行 |
|---|---|
| 桌面 xterm WS 输入 | `frontend/src/main.js:385`(term.onData → ws.send) |
| 桌面 xterm WS 后端 | `server.go:699`(xtermWSHandler)、`757`(ptmx.Write) |
| 桌面 PTY 来源 | `server.go:36`(startPtySession → `tmux new-session -A` + pty.StartWithSize)、`46`(detachStaleClients) |
| 桌面/iOS 聊天 | `main.js:165`(sendChat→/api/chat)、`ios/.../ChatViewModel.swift`(send→/api/chat) |
| iOS 终端输入 | `ios/.../TerminalViewModel.swift`(send→/api/sessions/<n>/send)、`server.go` sessionSendHandler→`SendToSession` |
| 统一发送入口 | `internal/agent/tools/send_to_session.go`(SendConfirmed)、`send.go`(sendText 原子 send-keys) |
| iOS/统一显示 | `server.go:776`(wsSnapshotHandler→capture-pane)、sessionCaptureHandler |
