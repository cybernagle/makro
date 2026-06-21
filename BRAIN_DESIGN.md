# Brain Module Design — Makro 从反应式调度器 → 反应式调度器 + 主动式大脑

> 状态：v2 设计稿（已与 `memory-cli/docs/RECONCILE.md` 对齐）。
> 只设计"手脚 + 大脑"（makro）这一侧；不设计 memory-cli 内部结构。
>
> **修订记录见 §0.2**。v1 → v2 的主要变化：写入端点 `:8090/a2a` → REST `:8765`；capture/proposal 不再塞 `knowledge`，用专用 category；proposal 状态用 `PATCH metadata` 而非 tag 切换；inbox.db 降为 UI 缓存，memory 为单一真相源。

---

## 0. 背景

### 0.1 先决事实（影响地基）

1. **机器上已有一条 memory 捕获在跑** —— `~/.claude/hooks/memory-stop.sh`（Stop hook，批处理：扫 Claude JSONL transcript → 抓 `type==user` → 写 memory）。它走的是 `:8090/a2a` JSON-RPC（那套 transport 真实存在），**capture 管道不是从零做，是把批处理 Stop-hook 升级成 push 的 UserPromptSubmit-hook**。
2. **memory-cli 有三套并行 API**（`memory serve` 同时启动）：
   - **REST `127.0.0.1:8765`**（`api.NewServer`，路由表全，无条件启动）—— **brain 的主通道**。
   - transport HTTP `:8090`（`transport.NewHTTPServer`，含 `/a2a` JSON-RPC）—— stop-hook.sh 在用。
   - Unix socket `~/.memory/memory.sock`（JSON-RPC）—— CLI 兜底。
3. **CLI `~/bin/memory` 直接打 SQLite**，不依赖 daemon（stop-hook.sh 的实际写入路径之一）。
4. category：源码定义 14 个常量，但 `NormalizeCategory` 放行任意非标准值（长度 ≤40、不含 `[]` 即可）。新增 `capture`/`proposals` 不必先改常量，但建议补常量让 dashboard/dream 认得。
5. `:8765` REST 的 `auth()` 写了但**没有任何 handler 调用**——安全漏洞，接缝落地时一并修（P0 阻塞项，RECONCILE §7.1）。

### 0.2 对齐修订记录（v1 → v2）

| # | v1（错） | v2（裁决后） | 来源 |
|---|---------|-------------|------|
| A1 | capture/proposal 塞 `knowledge` category | 专用 `capture`/`proposals` category | RECONCILE §2 D1 |
| A2 | 全篇 `:8090/a2a` JSON-RPC | 统一 `:8765` REST `POST /memories` | RECONCILE §3 D2 |
| A3 | 未提 `POST /memories` 缺 project/role/metadata | 写入需带全字段，memory-cli 侧 P0 扩展 | RECONCILE §3.2 D3 |
| A4 | proposal status 用 `memory_tag` add/remove 切换 | `PATCH /memories/{id}` 改 metadata | RECONCILE §4.1 D4 |
| A5 | inbox.db 与 memory 双写状态 | memory 单一真相源，inbox.db 是 UI 缓存启动重建 | RECONCILE §4.2 |
| A6 | 假设 `--created_after` 支持任意值 | 实际只有 `recent=true`(24h)，需 memory 加 `from=`/`tags=` | RECONCILE §3.3 |
| A7 | 完全没提 `:8765` auth 漏洞 | 提升为 P0 阻塞项 | RECONCILE §7.1 |
| A8 | 画像来源不明 | 独立 ProfileTask 输出 `character` category，brain 只读 | RECONCILE §5.3 |
| A9 | 漏判：daemon 必须 launchctl load | §10 R-rec-1 升级：daemon 常驻是 P0 前置（已验证可常驻） | RECONCILE §10 |

---

## 1. 全貌（一张图）

```
        ┌─────────────────────────── memory-cli (真相源, 不本设计) ─────────────────────┐
        │  memory serve 同时起三套:                                                         │
        │    REST 127.0.0.1:8765  (brain 主通道, 路由全)                                    │
        │    transport :8090/a2a (stop-hook.sh 在用)                                        │
        │    Unix socket ~/.memory/memory.sock (CLI 兜底)                                   │
        │  category: capture,proposals,feedback,preferences,soul,knowledge,character,...   │
        │  现有: memory-stop.sh (Stop hook, 批量扫 JSONL)                                   │
        └──────▲───────────────────────────────────────────────────────▲────────────────┘
               │ write: capture / proposal / feedback (POST :8765)      │ read: wake (GET :8765)
               │ PATCH: proposal status (PATCH :8765/memories/{id})      │
   ┌───────────┴───────────────────────────────────────────────────────┴───────────┐
   │                              makro (本设计)                                    │
   │                                                                                │
   │  ┌──────────────── 反应式 orchestrator (现有, 不变) ────────────────┐          │
   │  │  TUI chat → ProcessInput → LLM tool-call loop → tmux agents      │          │
   │  │  HookManager: before/after tool, agent_stop, permission          │          │
   │  └────────────┬──────────────────────────────────────────────┬─────┘          │
   │               │ (共用)                                        │ (共用)         │
   │   ┌───────────▼───────────┐                    ┌──────────────▼───────────┐    │
   │   │  AgentNotifier        │                    │  Brain (新增)            │    │
   │   │  Unix socket server   │                    │  ─ wake loop (cron+ev)   │    │
   │   │  ~/.makro/hooks.sock  │   capture          │  ─ read memory (:8765)   │    │
   │   │  ← claude hooks fire  │ ◀────────────────  │  ─ research (in-proc web)│    │
   │   │  onChat/onAgentStop…  │                    │  ─ propose (LLM + 门槛)   │    │
   │   └───────────┬───────────┘                    │  ─ feedback → PATCH+write│    │
   │               │                                │  ─ inbox store (UI 缓存) │    │
   │               │ SendChatMessage / notify       └────────────┬─────────────┘    │
   │               ▼                                             │ push             │
   │        ┌────────────┐   proposal inbox view ◀───────────────┘                  │
   │        │  TUI / GUI │   accept/reject keys                                     │
   │        └─────┬──────┘                                                          │
   │              │ notify.Notify / BarkPush (现有)                                 │
   └──────────────┼─────────────────────────────────────────────────────────────────┘
                  ▼
              我 (手机/桌面通知 + 屏幕)
```

**核心边界**：orchestrator 是"我说话 → 它动作"的反应式回路；brain 是"没人说话 → 它自己醒来 → 推东西"的主动式回路。两者**共用** notifier socket、notify/Bark 推送、TUI、provider、tmux —— 但 **brain 不走 orchestrator 的 `ProcessInput`/tool-call loop**，它有自己独立的 `wake()` 循环，避免两套状态机打架。

---

## 2. 大脑住在哪（问题 1）

**结论：独立 subcommand `makro brain --daemon`，复用现有 provider/notify/tmux，但不跑 orchestrator。**

### 2.1 为什么不是别的选项

| 选项 | 否决理由 |
|------|----------|
| 复用 orchestrator 加一个 "brain mode" | orchestrator 的状态机是"一条输入 → 一串 tool call → done"，是请求/响应模型。brain 是"无输入、自驱动、长期常驻"，硬塞进去会污染 `messages[]` 上下文和 cancel 逻辑，且 TUI 没开时 orchestrator 根本没初始化。 |
| TUI 内的常驻 goroutine | **致命**：要求"TUI 没开也自己醒来"。TUI 是前台进程，关了就没了。brain 必须能独立存活。 |

### 2.2 `makro brain` 的形态

- **新 subcommand**，`main.go` 顶部 switch 加 `case "brain":`，跟 `notify`/`chat`/`send` 平级。
- `--daemon`：前台常驻（开发用）；`--once`：跑一轮就退出（给 cron/launchd 用）；`--test-wake`：不调 LLM，只 dry-run 读 memory 打印。
- 它**自己**起 tmux client + provider + notifier socket 连接（作为**客户端**连 `~/.makro/hooks.sock`，而不是当 server —— server 永远是正在跑的那个 makro 实例，无论 TUI 还是 GUI）。
- 不跑 orchestrator、不跑 Bubbletea。
- 写一个 `internal/brain/` 包，`brain.Run(ctx, deps)` 是主循环。

### 2.3 进程拓扑（落地后）

```
launchd  ──KeepAlive──▶  makro brain --daemon          (主动式大脑, 后台常驻)
                        ├─ 连 ~/.makro/hooks.sock (客户端)
                        ├─ 连 memory REST :8765
                        ├─ 内部 ticker (cron)
                        └─ 通过 socket 把 proposal 推给"任意正在跑的 makro 实例"

[TUI 或 GUI 任一在跑时] makro (orchestrator + TUI/GUI + socket server)
                        └─ AgentNotifier 是 socket server, 收到 brain 发的
                           "proposal" 消息 → SendChatMessage / 弹 inbox
```

**关键**：brain 是 socket **客户端**，TUI/GUI 是 **server**。brain 不需要 TUI 在跑就能"醒来干活"（读 memory、调研、生成 proposal 写回 memory + 写 inbox 缓存）；只有"推到我眼前"这一步需要某个 makro 实例在跑。如果都不在跑，proposal 就躺在 memory 里，下次任何 makro 启动时从 memory 重建 inbox（见 §6.3）—— 这是可接受的降级。

### 2.4 组件清单

```
internal/brain/
├── brain.go          Run(ctx, deps) — 主循环, 装两个 trigger (cron ticker + event poller)
├── trigger.go        CronTrigger (内部 time.Ticker)  +  EventTrigger (轮询 memory)
├── wake.go           wake() — 一轮完整大脑循环 (read→research→propose→rate-limit→push→noop)
├── memory_client.go  封装 REST :8765 (POST/GET/PATCH) + CLI ~/bin/memory 兜底 + 死信
├── reader.go         R1-R6 查询 memory (GET /memories + /recall)
├── research.go       in-process web fetch (见 §5)
├── propose.go        LLM 生成 proposal + confidence gate + daily cap
├── ratelimit.go      滑动窗口: 每日/每周 proposal 上限
├── inbox.go          proposal UI 缓存 (SQLite, 状态从 memory 重建)
├── feedback.go       采集 accept/reject/ignore → PATCH proposal + 写 feedback 记忆
└── capture.go        capture sink (用户消息 → POST /memories)
```

---

## 3. 捕获管道怎么接（问题 2 —— 地基，到能动手的颗粒度）

### 3.1 现状 & 目标

- **现状**：`memory-stop.sh` 是 **Stop hook**（批处理）。每次 agent turn 结束才扫 JSONL，offset 去重，写 memory。缺点：延迟一个 turn；不覆盖 makro 自己的 chat；脚本难维护。它走 `:8090/a2a`。
- **目标**：**push 模型**，在用户消息**发出的当下**就截进 memory，且**只截用户这一侧**。brain capture 走 `:8765` REST（与 stop-hook.sh 走不同 API —— 双写期去重见 §11 R-rec-2）。

### 3.2 区分用户 vs agent 消息（已确认可行）

在 makro 这里有**两个**可区分的来源，都已经现成：

1. **coding agent 的用户消息**：Claude Code 的 hook 点 `UserPromptSubmit`（用户提交 prompt 时触发，stdin 收到含用户消息的 JSON）。**这正是区分点** —— 这个 hook 只在用户侧触发，agent 回复走的是 `Stop`/`PostToolUse`，不会进 `UserPromptSubmit`。现有 settings.json 里 `UserPromptSubmit` 已经挂了一个 makro hook（`makro-serve notify ... start`），但那个只发 `agent_start` 信号，**没带消息内容**。
2. **makro 自己 chat pane 里我打字的消息**：`internal/agent/orchestrator.go:386` `handleLLM` 第一行 `o.appendMessage(llm.Message{Role: llm.RoleUser, Content: input})` —— 这就是"我主动发起"的确切位置。在 `processOrchestratorInput`（`app.go:374`）入口截更干净（能同时拦到斜杠命令前的原始文本）。

### 3.3 接线方案 —— 两条捕获路径，一个 sink

#### 路径 A：coding agent 用户消息（改 Claude hook + 新增 socket 消息类型）

**Step 1**：新增 hook 配置函数 `EnsureUserPromptCaptureHook(claudeDir, executablePath)`（在 `internal/agent/hook_config.go`，跟 `EnsureStartHook` 同款，idempotent）。生成的 hook 命令把 stdin JSON 里的用户 prompt 提取出来，发 socket：

```bash
# 生成出来的 hook 命令（参考现有 fsNotifyHookSuffix 的写法）
# Claude Code 的 UserPromptSubmit hook stdin 是 {"prompt":"...","cwd":"..."}
'<exe>' capture "$(tmux display-message -p '#{session_name}')" "$(cat)"
```

**Step 2**：`main.go` 顶部 switch 加 `case "capture":`，调 `runSocketCommand("capture")`（复用现有 socket 客户端），payload：
```json
{"type":"capture","session":"auth-service","payload":"{\"prompt\":\"用户原话\",\"cwd\":\"...\"}"}
```

**Step 3**：`AgentNotifier`（`internal/agent/notifier.go`）的 `hookPayload` 加 `Payload string` 字段；`handleConn` 的 switch 加：
```go
case "capture":
    // 解析 payload JSON 取 prompt + cwd，调 sink
    if onCapture != nil { onCapture(msg.Session, prompt, cwd) }
```
新增 `OnCapture(fn func(session, prompt, cwd string))` 回调（跟 `OnChat`/`OnAgentStop` 同款）。

**Step 4**：`main.go` 里注册回调 → 调 capture sink（见 §3.5）。

#### 路径 B：makro 自己 chat 的用户消息（一行 hook 点）

在 `internal/tui/app.go:374` `processOrchestratorInput(text string)` **入口**加一行：
```go
func (a *AppModel) processOrchestratorInput(text string) {
    if a.captureFn != nil { a.captureFn("makro", text, "") }  // ← 新增
    ...
}
```
`captureFn` 由 main.go 注入（跟 `SetSendFn` 同款），指向同一个 capture sink。GUI 侧同理，在 `chatHandler`（`cmd/gui/server.go:425`）`go chatSvc.SendMessage(...)` 之前加 capture。

**为什么放入口而不是 `handleLLM`**：入口能拿到斜杠命令/`@mention` 的原始文本；且 GUI/CLI/TUI 三条路径都能统一在 `processOrchestratorInput`/`SendMessage` 这一层截。

### 3.4 写入契约（capture 对象的形状 —— 对齐后）

> **关键变化（A1+A2+A3）**：不再塞 `knowledge`，用专用 `capture` category；走 `:8765`；带全字段。

```json
POST http://127.0.0.1:8765/memories
Authorization: Bearer <key>
{
  "content":   "<用户原话, 已过滤噪声/截断>",
  "category":  "capture",
  "scope":     "agent:claude",
  "tags":      ["chat-capture", "auth-service"],
  "source":    "claude",
  "project":   "makro",
  "role":      "user",
  "metadata":  { "session": "auth-service", "prompt_hash": "..." }
}
```
- `tags` 必含 `chat-capture` —— R1 读取的锚点。
- `source` ∈ {`claude`, `copilot`, `makro`}，`role` capture 永远 `user`（只截用户侧）。
- `project` 从 cwd basename 取。
- **前置依赖（memory-cli 侧 P0）**：`POST /memories` 必须扩展接受 `project/role/metadata` 全字段（RECONCILE §3.2）。

### 3.5 Capture sink（统一写 memory）

新建 `internal/brain/capture.go`：
```go
// Capture 写一条 user-message 记忆到 memory。
// 失败永远不阻塞聊天主路径（聊天 > 捕获）。
func (s *CaptureSink) Capture(ctx context.Context, source, session, prompt, cwd string) {
    // 1. 过滤：<local-command-caveat>/<bash-input>/<command-name> 等噪声（照搬 memory-stop.sh 的过滤表）
    // 2. 去重：内存里 LRU (prompt 前64字 hash)，避免连续重复
    // 3. 截断：>500 字截断
    // 4. 写 memory：走 memory_client.go 的 WriteCapture() → POST :8765/memories (§3.4 形状)
}
```

### 3.6 失败/重试策略（关键：别拖垮聊天）

- **同步发 socket，异步写 memory**：capture 回调里只做"发到 in-process channel"，channel 消费者 goroutine 负责调 memory。channel 满了（>256）就**丢弃最旧**的（聊天实时性 >> 捕获完整性，丢一条 idea 不会死）。
- **memory 写失败**：指数退避重试 3 次（1s/4s/16s），全失败 → 落 `~/.makro/brain/capture-failed.jsonl` 死信队列，下次 wake 先冲死信。**永远不向聊天报错**。
- **memory daemon 没起**：`:8765` 连不上 → 降级到 `~/bin/memory write` CLI 子进程（直打 SQLite，不依赖 daemon）→ 仍失败走死信。
- **超时**：每次 memory 写 5s 超时。
- **幂等**：用 `source + session + prompt_hash + date` 作为 dedup，memory 侧 content_hash 去重（RECONCILE §3.4 确认已有），双保险。

### 3.7 与现有 memory-stop.sh 的关系

**先共存，后替换**：
- 阶段 1：新 capture hook 上线（走 `:8765`），stop-hook.sh（走 `:8090`）不动 —— 双写一段时间。
- 阶段 2：观察覆盖率后，把 stop-hook 的 makro 段注释掉（保留 car-agent 段）。
- **双写期去重要点（RECONCILE R-rec-2）**：两侧 `source`/`scope` 必须一致（都 `source=claude`、`scope=agent:claude`），content_hash 才能生效。切换前用一条测试消息验证两侧写出同 hash。

---

## 4. 两种触发器（问题 3）

### 4.1 cron 定时 —— 用内部 ticker，不碰系统 cron / launchd

**结论**：brain 进程**自己**常驻（`makro brain --daemon` 由 launchd `KeepAlive` 保活），内部 `time.Ticker` 驱动。**不用** 系统 crontab / launchd `StartCalendarInterval`。

**理由**：
- launchd `StartCalendarInterval` 每次起一个新进程，cold start 慢（要起 tmux client、provider、连 socket），且状态（rate-limit 窗口、上轮 inbox）丢失。
- 内部 ticker 在常驻进程里，状态连续，能做"上次 wake 时间 / 今日已推几条"的滑动窗口。
- launchd 照抄 memory daemon 的 plist 套路，`com.cybernagle.makro-brain`，`KeepAlive=true` 让它崩了自动起。

**实现**（`internal/brain/trigger.go`）：
```go
type CronTrigger struct {
    spec string        // "08:00" 或 cron 片段，先支持 "每天 HH:MM"
    next time.Time
}
func (c *CronTrigger) Wait(ctx) (<-chan struct{})  // 到点 close channel
```
默认 `08:00`（早上醒来），配置可改。避免凌晨推（人性）。

### 4.2 事件触发（memory 有新 idea 写入）—— 轮询，并对 memory 提一个软需求

**现状调研**：memory-cli **没有事件推送能力**。`memory serve` 是 pull 模型（socket transport + 定时 pipeline），`memory notify` 只查 reminders（到点提醒），不是"新写入"事件。没有 `subscribe`/`watch` 接口。

**可行方案（按推荐度）**：

**方案 A（推荐，零对 memory 的依赖）：brain 自己轮询 memory**
- `EventTrigger` 每 N 分钟 `GET /memories?category=capture&tags=chat-capture&from=<cursor>&limit=50`。
- 拿到新 capture → 喂给 `propose` 的"候选源"，**不直接生成 proposal**（要攒够信号，见 §7）。
- 去重靠 `from=<cursor>` 游标，存 `~/.makro/brain/cursor.json`。
- **优点**：今天就能做，不改 memory。**缺点**：N 分钟延迟（N=5 可接受，idea 不需要秒级响应）。
- **前置依赖（memory-cli 侧 P0）**：`GET /memories` 必须支持 `tags=` 和 `from=` 参数（RECONCILE §3.3，当前只有 `recent=true` 24h）。

**方案 B（更干净，需 memory 加接口）：向 memory 提软需求 —— socket 加 subscribe 推送**
- 最小需求已被方案 A 满足（`from=` 参数），**所以对 memory 的硬需求 = 无**。
- 锦上添花：`memory serve` 在 socket 加 `subscribe` 推送（新 write 时推 `{category,tags,preview}`），brain 监听 → 纯事件驱动。**MVP 不做，不阻塞**。

### 4.3 两种 trigger 怎么和 wake 衔接 —— 职责分离（关键约束）

> **event trigger 的 wake 不直接推 proposal，只攒候选；真正推送只走 cron。**（RECONCILE §6.3 强制采纳）

这是防"5min 轮询 → 高频打扰"的架构保证：

```go
func Run(ctx, deps) {
    cronT := NewCronTrigger(cfg.CronTime)             // 08:00
    eventT := NewEventTrigger(deps.Mem, 5*time.Minute) // 轮询
    for {
        select {
        case <-ctx.Done(): return
        case <-cronT.Wait(ctx):    go wake(ctx, deps, TriggerCron)   // 读候选+R1-R6 → 生成 proposal → 推送
        case <-eventT.Wait(ctx):   go wake(ctx, deps, TriggerEvent)  // 读 capture → 更新 domain-stats → 标记候选. 不 notify/不写 proposal
        }
    }
}
```

- **event wake**：读 capture → 更新 domain-stats → 标记候选。**不调 notify/Bark，不写 proposal。**
- **cron wake**：读候选 + R1-R6 → 生成 proposal → 推送。
- 两 trigger 都可能同时 fire → 用 `sync.Mutex` 保证 **wake 串行**。

---

## 5. 调研怎么做（问题 4）

**结论：进程内直接 web fetch（MVP），不 spawn tmux 子 agent。**

### 5.1 选型对比

| 方案 | 选 / 否 | 理由 |
|------|---------|------|
| **进程内 web fetch**（net/http + 一点点 HTML→text） | ✅ MVP | brain 是常驻后台进程，每轮调研 1-3 个 URL，量很小。in-process 最简单、最便宜、不依赖 tmux/TUI 状态。调研是"读"，没有副作用，不需要 coding agent 的工具链。 |
| 复用 tmux agent 调度 spawn 一个 research 子 agent | ❌ | spawn 一个 Claude Code/Copilot 来"搜个网页"是大炮打蚊子：起一个 agent 要数秒、占一个 tmux pane、烧 token。且 brain 在 TUI 没开时也要调研。 |
| chrome-cdp / gstack-browse（你 skill 里有） | 🟡 后续 | 适合"需要登录态/JS 渲染"的深度调研。MVP 先用纯 fetch，遇到 JS 站再升级。 |

### 5.2 实现要点

- `internal/brain/research.go`，一个 `Research(ctx, query) ([]Source, error)`：
  1. 先 `GET /recall?q=<topic>&max_tokens=2000` —— **调研前先查 memory**（R6），很多"我感兴趣的领域"我自己之前聊过，避免重复搜网页。
  2. 再 web fetch：用 DuckDuckGo HTML 或 Brave Search API（需 key）拿 3-5 个结果链接 → fetch 各自页面 → 抽正文。
  3. 每条 source 记 `{url, title, snippet, fetched_at}`，写进 proposal 的 evidence 字段。
- **限流**：每轮 wake 最多 fetch 5 个 URL，每 URL 8s 超时，总 30s budget。
- **复用 provider**：fetch 回来的原始文本喂给 LLM 做"提炼成可用 evidence"，复用 `internal/llm` provider（同一个 `cfg.LLMModel`），但走 `Complete` 单轮、无 tools（不走 orchestrator）。

### 5.3 一个反向需求

调研决定了"brain 能看到的外部世界"。MVP 只能看公开网页。若你的 idea 多在"需要登录的平台"，chrome-cdp（带 cookie 的浏览器）是必须的 —— v2 再上。

---

## 6. Proposal 生命周期 + UI（问题 5）

### 6.1 状态机（对齐后：状态存 metadata，不存 tag）

> **关键变化（A4+A5）**：status 进 metadata（PATCH 更新），不进 tag；inbox.db 是 UI 缓存从 memory 重建。

```
                    ┌─────────┐
   wake() 生成 ───▶ │  draft  │  (只在内存 + LLM 里, 还没落盘)
                    └────┬────┘
                         │ 通过 confidence gate + daily cap
                         ▼
                    ┌─────────┐  POST :8765/memories (category=proposals, metadata.status=open)
                    │  open   │  写 inbox.db 缓存 (status=open) + 推送 (notify/Bark + TUI)
                    └────┬────┘
              ┌──────────┼──────────┐
        accept│     reject│       ignore(72h 无反应)
              ▼          ▼          ▼
         ┌───────┐  ┌───────┐  ┌────────┐
         │accepted│  │reject │  │expired │   PATCH proposal.metadata.status (原子)
         └───┬───┘  └───────┘  └────────┘   + 写一条 feedback 记忆 (各司其职, 非重复)
             │
             │ (可选, 配置开关 auto_spawn, 默认 OFF)
             ▼
        ┌──────────┐
        │ spawned  │  自动 create_session + send_to_session 把 proposal 当 task 发过去
        └──────────┘
```

### 6.2 UI 呈现

**结论：新 inbox 视图，不混进 chat。**

- **TUI**：`Ctrl+B` 切到第三个 pane / overlay —— "Brain Inbox"。列表式：每行一条 proposal（标题 + 置信度 + age）。选中后右侧展开 body + evidence 来源。这跟 chat 的"对话流"是两种东西，混进 chat 会污染 chat 上下文（chat 的 `messages[]` 是给 orchestrator LLM 用的）。
- **GUI**（`cmd/gui`）：新建一个 view（照 `5b0297c` 把 usage 拆成独立 Cost view 的套路，再拆一个 Inbox view）。前端 tab 加 "Inbox"。
- **降级**（没有 makro 实例在跑）：proposal 只落在 memory，下次任意 makro 启动时 `GET /memories?category=proposals` 拉取重建 inbox.db，显示未读数 badge。

### 6.3 inbox.db 是 UI 缓存，不是真相源（关键变化 A5）

- accept/reject 时 makro **只 PATCH memory**（`PATCH :8765/memories/{id}` 改 metadata.status）。
- inbox.db 的状态**从 memory 重建**：启动时 `GET /memories?category=proposals` 拉取，按 `metadata.status` 填充本地缓存。
- **proposal 自身的 status（状态）和 feedback 记忆（历史快照）是两回事**：accept 时既 PATCH proposal.metadata.status=accepted，又写一条 `category=feedback` 记忆（供 R4 读"完成率/兴趣"）。两者各司其职，不是重复。

### 6.4 accept/reject 怎么操作

- **TUI inbox view**：`y`/`Enter`=accept，`n`=reject，`d`=defer（推迟，status 回到 open 但降权），`/` = 展开斜杠操作。
- **chat 里斜杠命令**：`/inbox`（列出 open），`/inbox accept <id>`，`/inbox reject <id> <reason>`。走现有 `CommandRegistry`（`internal/agent/commands.go`）。
- **回复关键词**（给手机/通知回调用）：通知里带 proposal id，回 `makro inbox accept <id>`（新 subcommand，socket 客户端 → PATCH memory）。
- **GUI**：按钮。

### 6.5 accept 之后

**默认停在 accepted，不自动 spawn。** 自动 spawn coding agent = 自动烧 token + 占 pane，是有副作用的动作，不该在"我点了一下 accept"之后无人值守发生。

配置开关 `brain.auto_spawn: false`（默认）。打开后：accepted → `create_session(name=<slug>)` + `send_to_session` 把 proposal body 当首个 task。即便开了，也只在"有 makro 实例在跑"时 spawn。

---

## 7. 主动性防过载（问题 7 —— 产品成败关键）

> "ignore 一旦成习惯，拟合就废了。" —— 整个 brain 的第一设计约束。

### 7.1 三道闸（串联，任一不过就不推）

**闸 1：每日/每周硬上限（ratelimit.go）**
```yaml
brain:
  daily_proposal_cap: 2        # 每天最多推 2 条
  weekly_proposal_cap: 8       # 每周最多 8 条
  min_interval: 2h             # 两条之间至少隔 2h
```
滑动窗口，状态存 `~/.makro/brain/ratelimit.json`。

**闸 2：置信度门槛（propose.go）**

LLM 生成 proposal 时，prompt 里要求它同时输出 `confidence ∈ [0,1]`（"这个 idea 用户大概率会真动手做的程度"）。confidence 计算的**输入**就是 R1-R6：
- R2 画像命中（领域是否在我历史兴趣里）→ +权重
- R4 反馈命中（同领域历史 accept 率）→ +权重（§9 的核心）
- R3 长期 open 同类 proposal（=我说了要做但没做）→ −权重
```yaml
brain:
  confidence_threshold: 0.6    # 低于 0.6 直接丢, 不进 inbox
```

**闸 3：单 idea 冷却（dedup）**
同一 domain/相似 idea 7 天内不重复推（靠 R5 去重快照 + 标题相似度）。

### 7.2 旋钮放哪 / 怎么调

全部在 `~/.makro/config.json` 的 `brain` 段（扩展 `internal/config/config.go`）。**关键设计：旋钮可被 brain 自己调整**（自适应）——
- 若连续 7 天 ignore 率 > 60% → brain 自动把 `daily_proposal_cap` 减 1（下限 1）、`confidence_threshold` +0.05（上限 0.85）。
- 若连续 7 天 accept 率 > 50% → 反向放松（上限保护）。
- 这个自适应本身也写一条 `category=feedback, tags=brain-self-tune` 的 memory，可追溯。

旋钮调整记录在 `~/.makro/brain/tuning.json` + 一条 memory，保证可解释。

---

## 8. 完成率拟合（问题 8）

> 光拟合兴趣不够，要拟合**完成率**。"连续 ignore / accept 了但从没真做完"的领域要少推。

### 8.1 怎么知道"没真做完"（信号采集）

**问题本质**：accept 是瞬时信号，"做完"是长期信号，中间没有显式 "done" 事件。靠三个间接信号拼：

| 信号 | 怎么采 | 写法 |
|------|--------|------|
| **S1: accept → 后续 capture 出现该领域工作** | accept 后 N 天内，R1 capture 里出现同 domain/session 的用户消息（说明我真开干了） | PATCH proposal.metadata.outcome + feedback 记忆 `acted_within=<天数>` |
| **S2: accept → 后续 memory 出现 project 类** | `GET /memories?category=project&from=<accept时间>` 命中同 domain → 做成了 | project 类记忆是 memory 自己整理的，brain 只读 |
| **S3: accept 后长期静默** | accept 后 14 天既没 capture 也没 project 记忆 → "accept 了但没做" | PATCH proposal.metadata.outcome=stalled |

S1 是 makro **自己能采**的主信号（capture 管道在 makro 手里）。S2 依赖 memory 的 project 类整理质量。S3 是 S1/S2 的反证。

**采集时机**：不在 accept 当下采，在**每次 cron wake 时回扫**最近 30 天的 accepted proposal，更新它们的 `metadata.outcome`（stalled / in-progress / done）。

### 8.2 怎么写回 memory 让下一轮读到

每次回扫更新后：
1. **PATCH proposal.metadata.outcome**（proposal 自身的状态）。
2. **写/更新一条 feedback 记忆**（供下一轮 R4 读）：
```json
POST :8765/memories
{
  "category": "feedback",
  "tags": ["brain-feedback", "outcome", "<domain>"],
  "source": "makro-brain",
  "metadata": {
    "proposal_id": "<原 proposal id>",
    "outcome": "stalled | in-progress | done",
    "acted_within_days": 5
  },
  "content": "proposal: <标题>\noutcome: stalled\nacted_within_days: ..."
}
```
下一轮 wake 的 R4 读到这些 → 喂给 LLM 算 confidence 时，prompt 明确："该领域历史 5 条 accepted，3 条 stalled → 大幅降低 confidence"。**拟合就靠这条涌现** —— 不训练模型，靠把 outcome 写进 feedback，让下一轮 LLM 读到自然降权。

### 8.3 一个领域级的滚动统计（索引优化）

`~/.makro/brain/domain-stats.json` 维护 `{domain: {proposed, accepted, completed, ignored}}` 滚动 90 天。confidence 计算时直接查这个表算完成率，不必每次让 LLM 从 feedback 文本里数。这是"索引"，feedback 记忆仍是真相源。

---

## 9. 与 memory 的读写契约（问题 6 —— 接缝，最重要）

> 这一节是 makro 与 memory-cli 的**最终对齐契约**（吸收 RECONCILE.md 全部裁决）。memory-cli 侧的配套改动见 §10 改动清单 [M] 项。

### 9.1 端点总表（统一走 `:8765` REST）

| 操作 | 方法 & 路径 | 备注 |
|------|------------|------|
| 写 capture/proposal/feedback | `POST :8765/memories` | 需扩展全字段（A3），memory-cli P0 |
| 读 R1-R6 | `GET :8765/memories?...` + `GET :8765/recall` | 需加 `tags=`/`from=`（A6），memory-cli P0 |
| proposal status 状态机 | `PATCH :8765/memories/{id}` | 改 metadata 局部更新（A4），memory-cli P1 |
| 兜底（daemon 没起） | `~/bin/memory write/list` CLI 子进程 | 直打 SQLite |

所有请求带 `Authorization: Bearer <key>`（memory-cli P0 修 auth，A7）。`<key>` 用真实 key（**`mk-car-agent-abc123` 是占位符，部署时换**）。

### 9.2 Read 清单（brain 醒来后从 memory 读什么）

每次 cron `wake()` 读以下 6 组（**全部依赖 §9.1 的 `tags=`/`from=` 参数补全**）：

| # | 读什么 | 可执行查询 | 量 | 用途 |
|---|--------|-----------|----|------|
| R1 | 最近 7 天 capture | `GET /memories?category=capture&tags=chat-capture&from=-7d&limit=100` | ≤100 | idea 原始矿 |
| R2 | 当前画像 | `GET /memories?category=preferences&limit=20` + `?category=soul&limit=10` + `?category=character&limit=1`（最新画像） | ≤31 | 拟合"兴趣" |
| R3 | 所有 status=open 的 proposal | `GET /memories?category=proposals`（全量，brain 侧按 `metadata.status==open` 过滤，RECONCILE §5.2 方案1） | 全量（应很少） | 不重复提、看哪些长期 open |
| R4 | 最近 30 天反馈 | `GET /memories?category=feedback&tags=brain-feedback&from=-30d&limit=20` | ≤20 | 拟合"完成率/兴趣"（§8 命脉） |
| R5 | 去重快照 | `GET /memories?category=proposals&from=-30d` 取标题 | ≤30 | 不重复推同类 |
| R6 | 上下文注入 | `GET /recall?q=<topic>&max_tokens=2000`（已有） | 按预算 | 调研前先查历史 |

**预算**：6 组读出来总 token 量硬上限 ~8k（在 `propose.go` 里裁剪/摘要），超了就按 R4>R2>R1 优先级丢。**R4（feedback）永远最后丢 —— 它是拟合命脉。**

### 9.3 Write 清单（三种写入对象的最终形状）

#### (a) 聊天捕获（capture）—— 见 §3.4

```json
{
  "category": "capture",  "scope": "agent:claude",
  "tags": ["chat-capture", "<session>"],  "source": "claude",
  "project": "<cwd basename>",  "role": "user",
  "metadata": { "session": "<session>", "prompt_hash": "..." },
  "content": "<用户原话>"
}
```

#### (b) proposal

```json
POST :8765/memories
{
  "category": "proposals",
  "scope": "global",
  "tags": ["brain-proposal", "<domain>"],
  "source": "makro-brain",
  "project": "<相关项目或空>",
  "metadata": {
    "status": "open",            // ← status 进 metadata, 不进 tag (A4)
    "confidence": 0.72,
    "trigger": "cron",
    "evidence": [{"url":"...","title":"...","snippet":"..."}]
  },
  "content": "## <标题>\nconfidence: 0.72\ntrigger: cron\ndomain: <域>\n\n<建议做的事, 带理由>\n\n### 来源\n- ..."
}
```
- status 切换走 `PATCH`（见 §9.4），不走 tag add/remove。
- `domain` 同时进 metadata 和 tags（tags 便于按域聚合）。

#### (c) 反馈（feedback）—— 命脉

```json
POST :8765/memories
{
  "category": "feedback",
  "scope": "global",
  "tags": ["brain-feedback", "<verdict>", "<domain>"],  // verdict 进 tag 便于聚合算 accept 率
  "source": "human",
  "metadata": {
    "proposal_id": "<原 proposal id>",
    "verdict": "accept | reject | ignore",
    "reason": "<拒绝理由或 accept 备注>",
    "acted_within_days": null,   // P3 完成率回扫时回填
    "outcome": null              // P3: stalled|in-progress|done
  },
  "content": "verdict: <v>\nproposal: <标题>\nreason: <理由>"
}
```
- `verdict` **既进 tag 又进 metadata**：tag 便于 `GET /memories?tags=brain-feedback,accept` 快速聚合算率；metadata 便于 PATCH 更新 outcome（outcome 不值得占 tag）。
- `category=feedback` 是 R4 的锚点。**这一条写得越规整，下一轮拟合越准。**

### 9.4 状态机回写（PATCH）

```
PATCH http://127.0.0.1:8765/memories/{id}
Authorization: Bearer <key>
{"metadata": {"status": "accepted", "accepted_at": "2026-06-19T08:01:00Z", "reject_reason": null}}
```
- memory-cli 侧 `handleMemoryDetail` 加 `case http.MethodPatch`，**合并** metadata（不是覆盖）。
- 局部更新，原子，一次调用。比 tag add/remove 两步非原子强。

### 9.5 写入降级链（A2 + RECONCILE §3.4 确认）

1. **主**：`POST :8765/memories`（HTTP，需 auth key）
2. **备**：`~/bin/memory write` CLI 子进程（daemon 没起时直打 SQLite）
3. **死信**：都失败 → `~/.makro/brain/write-failed.jsonl`，wake 时先冲
- **幂等**：memory 侧 content_hash 去重（RECONCILE 确认已有）+ brain 侧 `source+session+prompt_hash+date` dedup，双保险。

---

## 10. 改动清单（按优先级，标注类型 + 侧）

> 标 [M] = memory-cli 做，[K] = makro 做。P0 不通则闭环空转。依赖 RECONCILE §8 的统一清单。

### P0 —— 地基（阻塞一切）

| # | 改动 | 侧 | 类型 | 文件 / 位置 | 依赖 |
|---|------|----|------|------------|------|
| M1 | `POST /memories` 扩展全字段（project/role/metadata）+ `store.WriteFull` + `metadata` 列 | M | memory-cli | `internal/api/api.go:114`, `internal/store/memory.go` | — |
| M2 | `GET /memories` 加 `tags=` 和 `from=`/`to=` 参数 | M | memory-cli | `internal/api/api.go:154 listMemories`, `store.ListOptions` | — |
| M3 | 修复 `:8765` auth（所有 handler 包 auth 中间件） | M | memory-cli | `internal/api/api.go` 各 HandleFunc | — |
| M4 | 补 `CategoryCapture`/`CategoryProposals` 常量 + 入 AllCategories | M | memory-cli | `internal/store/memory.go:18` | — |
| M5 | 确认 daemon 常驻（launchctl load `com.cybernagle.memory-daemon`，KeepAlive=true，`:8765` 真监听） | M | memory-cli 部署 | `~/Library/LaunchAgents/` | — |
| K1 | capture sink `internal/brain/capture.go`（写 memory，去重，死信） | K | **新模块** | `internal/brain/` | M1,M3 |
| K2 | `AgentNotifier` 加 `capture` 消息类型 + `OnCapture` 回调 | K | **hook 改动** | `internal/agent/notifier.go` | — |
| K3 | `makro capture` subcommand（socket 客户端） | K | **新** | `main.go` switch + buildSocketPayload | K2 |
| K4 | `EnsureUserPromptCaptureHook`（Claude UserPromptSubmit hook 写入） | K | **hook 改动** | `internal/agent/hook_config.go` + `main.go` | K3 |
| K5 | TUI/GUI 入口截 chat → capture sink | K | **TUI/GUI 改动** | `internal/tui/app.go:374`, `cmd/gui/server.go:425` | K1 |
| K6 | `internal/brain/memory_client.go`（封装 `:8765` REST + CLI 兜底 + 死信） | K | **新模块** | `internal/brain/` | M1,M3 |
| K7 | `config.Config` 加 `Brain` 段（toggle, caps, threshold, memory key） | K | **配置变更** | `internal/config/config.go` | — |

### P1 —— 闭环（能跑起来）

| # | 改动 | 侧 | 类型 | 文件 / 位置 | 依赖 |
|---|------|----|------|------------|------|
| M6 | `PATCH /memories/{id}`（metadata 局部更新，status 状态机） | M | memory-cli | `internal/api/api.go:202 handleMemoryDetail` | M1 |
| M7 | `character` category 重定义为"画像" + ProfileTask 模块（**先查存量再迁移，R-rec-3**） | M | memory-cli | `internal/profile/` 新建 | M6 |
| K8 | `brain.go` + `wake.go`（cron 走推、event 只攒候选）+ `reader.go`(R1-R6) + `propose.go`(confidence gate) | K | **新模块** | `internal/brain/` | M2,K6 |
| K9 | `inbox.go`（SQLite UI 缓存，**状态从 memory 重建**）+ proposal 写 memory + 推送（socket `proposal` 消息） | K | **新 + hook 改动** | `internal/brain/`, notifier 加 proposal 类型 | M6 |
| K10 | `feedback.go`（accept/reject → PATCH proposal + 写 feedback 记忆） | K | **新模块** | `internal/brain/` | M6 |

### P2 —— 体验

| # | 改动 | 侧 | 类型 | 依赖 |
|---|------|----|------|------|
| K11 | TUI Brain Inbox view（`Ctrl+B`） | K | **TUI 改动** | K9 |
| K12 | GUI Inbox view（新 tab） | K | **GUI 改动** | K9 |
| K13 | `/inbox` 斜杠命令 + `makro inbox accept/reject` subcommand | K | **hook 改动** | K9 |
| K14 | `research.go`（进程内 web fetch + recall 先查） | K | **新模块** | K8 |
| K15 | cron trigger（内部 ticker）+ event trigger（轮询 memory） | K | **新模块** | K8,M2 |
| K16 | launchd plist `com.cybernagle.makro-brain` | K | **部署变更** | K8 |

### P3 —— 自适应

| # | 改动 | 侧 | 类型 | 依赖 |
|---|------|----|------|------|
| K17 | 完成率回扫（S1/S2/S3 → PATCH proposal.metadata.outcome + 写 feedback） | K | **新模块** | K10 |
| K18 | 自适应旋钮（ignore 率 → 调 caps/threshold） | K | **新模块** | K17 |
| K19 | domain-stats 滚动统计（索引） | K | **新模块** | K10 |

### 对 memory 的需求汇总

- **硬需求（P0 阻塞）**：M1（全字段写）、M2（tags/from 查询）、M3（auth）、M5（daemon 常驻）。
- **P1**：M6（PATCH）、M7（画像）。
- **软需求（不阻塞，roadmap）**：socket 加 subscribe 推送（让 brain 纯事件驱动）；FileStore 明确不实现 metadata（SQLite-only，RECONCILE R-rec-4）。

---

## 11. 风险

### R1. 主动式 brain 和反应式 orchestrator 共处 —— 已被架构消解
最大的风险**已经被架构消解**：brain 是独立 subcommand + 独立进程（launchd 保活），**不**塞进 orchestrator。两者只通过两个无副作用通道通信：① memory（共享真相源）；② socket（brain→notifier 单向推 proposal）。两者**不共享** `Orchestrator.messages[]`、cancel 逻辑、tmux 状态机。
仍需注意：
- **socket 协议扩展**：notifier 的 `hookPayload` 加 `capture`/`proposal` 类型时，保持向后兼容（现有 `agent_stop`/`chat`/`session` 不动）。
- **memory 写并发**：brain 和 capture sink 和 stop-hook.sh 可能并发写 memory。memory-cli 是 SQLite + daemon 应能抗，但 capture sink 要做好 client 侧去重（prompt hash）。

### R2. capture 把聊天搞卡（地基失败模式）
capture 在聊天热路径上。**任何**同步阻塞（memory daemon 没起、网络抖）都会让我觉得"聊天变慢了"→ 我会关掉 capture → 地基断。
**缓解**：§3.6 的"同步发 channel、异步写 memory、永不报错给聊天、死信兜底、CLI 降级"。这条必须最先验，P0 阶段就压测"memory daemon 杀掉时聊天是否完全无感"。

### R3. ignore 惯性化（产品失败模式）
如果 brain 推的不对路，我会从"看 proposal"滑向"全 ignore"，一旦习惯化，feedback 信号全是 ignore，拟合彻底废。
**缓解**：§7 三道闸 + 自适应（ignore 率高 → 自动收紧到每天 1 条甚至 0 条）。但**真正的缓解是 capture 质量** —— 若 R1 矿里全是噪声，画像就是歪的。**P0 capture 的过滤质量 > P1 的所有 fancy 逻辑**。

### R4. LLM confidence 不可靠
让 LLM 自己打 confidence 分，它倾向乐观。单一 LLM 打分会被偏见带歪。
**缓解**：confidence 不是 LLM 一家说了算 —— P3 的 domain-stats（硬统计完成率）是 ground truth，LLM 打分只是 prior，最终门控用加权或 `max`。先 P3，但留接口。

### R5. memory read 把 brain 上下文撑爆
R1-R6 全读出来可能几万 token。
**缓解**：§9.2 总量硬上限 8k + 优先级裁剪。R4（feedback）永远最后丢。

### R6. daemon 未常驻导致地基悬空（RECONCILE R-rec-1 升级）
**实测发现**：launchd 的 `com.cybernagle.memory-daemon` plist 之前未 load，`:8765`/`:8090`/`memory.sock` 三套 API 全靠 `memory serve` 手动起。若 daemon 不常驻，brain 的 HTTP 客户端全程连不上。
**缓解**：M5 是 P0 前置 —— `launchctl load` plist + 确认 `:8765` 真监听。capture sink 的 CLI 降级（`~/bin/memory write` 直打 SQLite）是 daemon 挂时的兜底，但要明确"daemon 挂 = 轮询/event 触发失效（读不到），只剩 capture 能 CLI 写"。

### R7. 双写期去重（RECONCILE R-rec-2）
capture（走 `:8765`）和 stop-hook.sh（走 `:8090`）并行期，同一条用户消息可能被写两次。即使 content_hash 相同，若两侧 `source`/`scope` 不同会被当两条。
**缓解**：双写期内两侧 `source=claude`、`scope=agent:claude` 必须一致。切换前用一条测试消息验证两侧写出同 hash。

### R8. accept 自动 spawn 的副作用
默认 OFF。即便打开，也加约束：只在"有 makro 实例在跑 + 09:00-22:00 + 当日 spawned<2"时 spawn。

---

## 附：落地后第一周验收标准（对齐后可测）

1. **P0 通**：
   - `curl -X POST :8765/memories -H "Authorization: Bearer <key>" -d '{"content":"test","category":"capture","project":"x","metadata":{"k":"v"}}'` 返回 201 且 memory 里有 project/metadata。
   - `curl -H "Authorization: Bearer <key>" ":8765/memories?tags=chat-capture&from=-7d"` 能查到。
   - 不带 key 的请求被 401。
   - 我跟 Claude Code 聊一天后，能查到当天 capture，且聊天体感零延迟（memory daemon 杀掉也无感，CLI 降级生效）。
2. **P1 闭环**：某天早上 08:00，手机收到 1 条 Bark 推送，内容是我前天聊过但没做的 idea，带来源。accept 后 `GET /memories?category=feedback&tags=brain-feedback,accept` 出现一条；同 proposal 的 `metadata.status` 变 accepted。
3. **接缝不撕裂**：`GET /memories?category=proposals` 拿到的 `metadata.status` 和 inbox.db 显示一致（因为 inbox.db 从 memory 重建）。
4. **P3 自洽**：连续 3 天 ignore 后，第 4 天 brain 不推了（`tuning.json` 里 cap 降到 1 / threshold 升到 0.7），并写了一条 `brain-self-tune` memory 记录原因。
