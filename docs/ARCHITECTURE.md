# Makro 架构说明 (ARCHITECTURE)

> 配套分层图：[`architecture.svg`](./architecture.svg)

这份文档回答一个问题：**"我能否在心里建立一张可靠的地图，把任何一行代码归位？"**
目标不是把架构说得漂亮，而是建立一个清晰的分层契约，让你（和未来的贡献者）对
"这行代码该放哪、这个依赖合不合法" 有掌控感。

---

## 1. 一句话定位

Makro 是一个 **AI 编码 Agent 编排器**：它管理多个后台编码 Agent（Claude Code、
Copilot 等）在 tmux 会话里并行干活，外层用 **反应式编排器**（Orchestrator）+
**主动式第二大脑**（Brain）两层智能驱动，对外提供 TUI / GUI / iOS 三种形态。

```
                ┌─────────────────────────────────────┐
                │   反应式 (Reactive)    主动式 (Proactive) │
                │   Orchestrator          Brain        │
                │   "用户说什么 → 调工具"   "记忆 → 主动提议"  │
                └─────────────────────────────────────┘
```

两种智能共用同一套基础设施（tmux / LLM / memory），但它们的**触发源不同**：编排器
由用户输入驱动，大脑由定时/唤醒驱动。这是整个系统的核心二元结构。

---

## 2. 分层模型

严格 6 层，依赖**只能自上而下**。同一层包之间**禁止互相依赖**（除非通过接口）。

| 层 | 职责 | 包 | 颜色 |
|----|------|-----|------|
| **L1 Presentation / Entry** | 进程入口、UI、人机交互 | `main.go`, `internal/tui`, `cmd/gui`, `ios/Makro`, `gui/frontend` | slate |
| **L2 Application** | 业务流程编排、智能决策 | `internal/agent` (Orchestrator), `internal/brain` | blue |
| **L3 Domain Tools** | 可调用的能力单元、技能、钩子 | `internal/agent/tools`, `internal/agent/skills`, hooks (agent 内) | green |
| **L4 Providers / Adapters** | 外部系统适配器 | `internal/llm`, `internal/tmux`, `internal/notify`, `internal/apns` | violet |
| **L5 Infrastructure** | 配置、持久化、外部服务客户端 | `internal/config`, `internal/usage`, `brain/memory_client` | amber |
| **L6 Shared Kernel** | 无依赖的通用工具 | `internal/util` | gray |

### 分层铁律

1. **依赖单向**：L_N 只能依赖 L_{>N}。`util` 谁都能用，但它不用任何人。
2. **同层不互依**：L2 的 `agent` 和 `brain` 不应直接 import 对方的具体类型；需要协作时
   在更下层（L5/L4）定义共享接口，或由 L1 装配根做胶水。
3. **接口归消费方**（DIP）：抽象定义在**调用方**包，实现在**提供方**包。例如 `llm.Provider`
   定义在 `internal/llm`，被 `agent`/`brain` 消费，而非反过来。
4. **装配只在 L1**：`main.go` 和 `cmd/gui` 是唯二允许"知道所有包"的地方。`NewOrchestrator(...)`
   这种构造调用只出现在入口，不应渗透到 L2/L3。

---

## 3. 各层详解

### L1 · Presentation / Entry

| 单元 | 角色 | 备注 |
|------|------|------|
| `main.go` | CLI 入口 + **装配根**（composition root）。负责把 config/provider/tmux/orchestrator/brain/tui 全部 wire 起来 | ~20k LOC，偏大（见 §6） |
| `internal/tui` | Bubbletea 分屏 TUI：左 chat + 右 viewer | 依赖 agent + tools + tmux（合理，它是 L1） |
| `cmd/gui` | Wails 桌面壳 + 内嵌 HTTP/WS server，供 iOS / Web 调用 | **⚠ 自行 fork tmux CLI，绕过 `internal/tmux`**（见 §6.1） |
| `ios/Makro` | SwiftUI 客户端，经 HTTPS/WS 调 `cmd/gui` | 非 Go 依赖，纯运行时 |
| `gui/frontend` | Vue3 + xterm.js 前端 | 同上 |

### L2 · Application

**`internal/agent` — Orchestrator（反应式）**
- 输入路由：`/slash` 命令 → `@mention` → 自然语言 LLM。
- Tool-call loop：拿到 LLM 返回的工具调用 → 执行 → 回填 → 循环，直到 `EventDone`。
- 子组件：`HookManager`（跨 agent 中继）、`Guardian`（自动处理确认弹窗）、
  `AgentNotifier`（追踪 agent 启停）、`CommandRegistry`（命令表）。

**`internal/brain` — Brain（主动式）**
- Wake-loop：定时 / `/brain wake` → 读 memory → LLM 提议 → 置信度/配额闸门 → 写 inbox + push。
- `Capture` 管道：无论 TUI/GUI，只要 `brain.enabled` 就捕获对话上下文。
- **UI 无关**：通过 `Pusher` 接口把提议交给 L1 决定如何展示（chat 消息 + Bark/APNs）。

> ⚠ 当前 `brain` 通过 `brain.RegisterCommands(cr *agent.CommandRegistry, ...)` 反向依赖
> `agent`，违反了同层不互依赖。详见 §6.2。

### L3 · Domain Tools

- **`internal/agent/tools`**：17 个工具（`all.go` 统一注册），是 Orchestrator 的"手脚"。
  - 关键设计：包内**自带** `TmuxClient` / `Notifier` / `Assessor` 三个接口，由 L2/L4 注入实现。
  - 这让 tools 不直接持有 `*tmux.Client`，是依赖反转的好例子——**但** `TmuxClient.State()`
    返回 `*tmux.StateMirror` 又把 tmux 类型漏了进来（见 §6.3）。
- **`internal/agent/skills`**：Skill 的 loader + parser（独立子包，干净）。
- **hooks / guardian**：目前内嵌在 `agent` 包，未来若膨胀可下沉为 `agent/hooks` 子包。

### L4 · Providers / Adapters

| 包 | 接口 | 实现 | 评价 |
|----|------|------|------|
| `internal/llm` | `Provider.Stream/Complete` | anthropic, openai | ✅ 教科书式干净 |
| `internal/tmux` | `tmux.TmuxClient` | `Client` (CLI 封装) | ✅ 干净，但被 GUI 绕过 |
| `internal/notify` | 函数式 API | macOS + Bark | ✅ 简单可靠 |
| `internal/apns` | `Client.Push` | ES256 + JWT | ✅ 独立 |

### L5 · Infrastructure

- `internal/config`：config.json + env + `.claude/settings` 合并。纯数据，被 L1/L2 消费。
- `internal/usage`：SQLite 用量统计（claude / zcode 双源）。查询返回值类型（`Stats` 等）
  被 L1 直接消费——合理。
- `brain/memory_client`：memory MCP 服务的 HTTP 客户端，目前归在 brain 包内。

### L6 · Shared Kernel

`internal/util` — 仅 73 LOC 的 string 辅助。刻意保持极小，避免沦为"什么都往里塞"的垃圾桶。

---

## 4. 依赖关系矩阵（编译期）

行依赖列。✅ 合规，🔌 经接口解耦。**2026-06 重构后：L2 同层零互依赖，tools 接口无 tmux 类型泄漏。**

| 依赖方 ↓ \ 被依赖方 → | agent | brain | tools | skills | llm | tmux | notify | config | usage | util |
|---|---|---|---|---|---|---|---|---|---|---|
| **main.go** | ✅ | ✅ | ✅ | | ✅ | ✅ | ✅ | ✅ | ✅ | |
| **tui** | ✅ | | ✅ | | | ✅ | | | | ✅ |
| **cmd/gui** | ✅ | ✅ | ✅ | | ✅ | ✅† | ✅ | ✅ | ✅ | |
| **agent** | — | (无) | ✅ | ✅ | ✅ | via-iface | ✅ | | ✅ | ✅ |
| **brain** | (无) | — | | | ✅ | | | ✅ | | |
| **tools** | via-iface | | — | | | via-iface | | | | ✅ |

† `cmd/gui` 的 `getTmuxBin()`/`tmuxArgs()` 现委托 `internal/tmux.FindTmuxBin()`/`DefaultArgs()`
（单一真相源）；无状态的一次性 `exec tmux` 调用风格保留，但 helper 来源已统一。

---

## 5. 做得好的地方（给你掌控感的部分）

1. **接口驱动 + 依赖反转**：`tools` 包定义 `TmuxClient`/`Notifier`/`Assessor` 接口并消费它们，
   具体实现在 `agent`/`tmux` 包注入。这让 tools 可单测、可替换。✨
2. **`llm.Provider` 是教科书设计**：L2 完全不知道 anthropic/openai 的存在，新增 provider
   只动 L4。
3. **brain 的 UI 无关性**：`Pusher` 接口让主动大脑不碰 TUI/Bark，换 UI 是接线变化。
4. **`util` 克制**：没有膨胀成 god-package。
5. **测试覆盖分布合理**：tools / hooks / brain / tmux / usage 都有对应 `*_test.go`。
6. **两个智能层职责正交**：Orchestrator（反应）和 Brain（主动）各有入口、各有生命周期，
   不会互相阻塞。
7. **L2 同层零互依赖（2026-06 重构后）**：brain 与 agent 之间不再有任何编译期依赖，
   `brain.CommandRegistrar` 接口 + L1 适配器完成了依赖反转。

**结论：核心骨架（L2–L4）干净且可推理，三条历史耦合已全部消除。** 剩余的只有 L1 的
`main.go` 偏重（§6.4），属可接受的胶水层体量。

---

## 6. 已偿还的债（2026-06 外科手术式重构）

这一轮重构只动依赖边界，不改包的物理位置、不改公共 API 语义。三条历史耦合
全部消除，全量 `go build ./...` + `go test ./...` 通过。

### 6.1 ✅ 已解决 — GUI 双 tmux 通路（原 P0）

**问题**：`cmd/gui` 自造了 `getTmuxBin()` / `tmuxArgs()`，与 `internal/tmux` 的
`findTmuxBin()` / `Client.tmuxArgs()` 两套实现并存，且查找顺序还不一致。

**做法**：在 `internal/tmux` 导出 `FindTmuxBin()` 和 `DefaultArgs()` 两个公共函数
（单一真相源），GUI 的 `getTmuxBin()` / `tmuxArgs()` 改为委托它们。
- GUI 仍保留**无状态的一次性 `exec tmux` 调用风格**（snapshot/terminal_service 等），
  因为这些调用点在 orchestrator 的 `*tmux.Client` 生命周期之外，强行塞进有状态的
  `tc.Exec()` 反而引入耦合。
- 真正统一的是 **helper 来源**：现在全仓只有一份 bin 查找 + socket-args 构造逻辑。

### 6.2 ✅ 已解决 — brain 反向依赖 agent（原 P1）

**问题**：`brain.RegisterCommands(cr *agent.CommandRegistry, b *Brain)` 让 `brain`
编译期 import `agent`，违反 L2 同层不互依赖。

**做法**：依赖反转。在 `brain` 包定义两个类型：
- `CommandSpec` — brain 视角的命令 DTO（只含 Name/Usage/Description/Execute 四字段，
  不含 agent 专有的 `Skill` 绑定）
- `CommandRegistrar` 接口 — `Register(spec CommandSpec)`

`brain` 不再 import `agent`。适配发生在 **L1 装配点**（`main.go` / `chat_service.go`），
各写一个 5 行的 `agentCmdRegistrar`，把 `brain.CommandSpec` 翻译成 `agent.SlashCommand`。
这与既有的 `tuiBrainPusher`（实现 `brain.Pusher`）是同款模式——胶水留在装配根，L2 互不依赖。

### 6.3 ✅ 已解决 — tools 接口泄漏 tmux.StateMirror 类型（原 P2）

**问题**：`tools.TmuxClient.State()` 返回 `*tmux.StateMirror`，迫使 `tools/types.go`
import `internal/tmux`。但全包只有 `switch_session.go` 一处用了 `State().FindSession(name)`，
且只判存在性（不读任何 Session 字段）。

**做法**：把 `State() *tmux.StateMirror` 换成 `HasSession(name string) bool`。
- `tools/types.go` 不再 import tmux（接口定义处零 tmux 类型依赖）
- `tmux.Client` 加 `HasSession` 方法（委托 `state.FindSession(name) != nil`）
- `internal/tui` 的本地 `tmuxClient` 接口补 `HasSession`（它自己仍需 `State()` 渲染会话列表）
- 两个测试 mock（`tools` / `agent` 包各一个）改用 `sessions map[string]bool`

**保留的合理耦合**：tools 里 8 个业务文件仍 import tmux，用 `tmux.SendKeysCmd()` 等纯函数
构造命令字符串。这是"消费命令字典"的正常关系（tools 知道 tmux CLI 格式），不是分层违规，
强行抽接口反而降低可读性。

### 6.4 P3 — main.go 过重（~20k LOC，未处理）

`main.go` 承担装配 + socket 命令分发 + 通知处理 + brain pusher 实现等多重职责。
建议拆出 `cmd/makro/`（TUI 子命令）或至少把 `tuiBrainPusher`、socket handler 移到
`internal/` 下的小包。本轮外科手术式重构刻意未动，留待后续。

### 6.5 P3 — 文档与代码漂移

仓库根有 12 个 `.md`（BRAIN_DESIGN / IMPLEMENTATION_NOTES / REVIEW / SPEC / TASKS ...），
部分内容已与当前代码不一致（如 §README 的架构图还是早期单 TUI 形态）。建议：
- 保留 `README.md`（用户向）+ `docs/ARCHITECTURE.md`（本文，开发向）+ `docs/CHANGELOG.md`。
- 其余历史设计文档归档到 `docs/archive/`，避免读者被过时信息误导。

---

## 7. 决策清单（改代码前问自己）

- [ ] 新代码属于哪一层？它 import 的包是否都在**更下层**？
- [ ] 如果是 L2/L3，我是否在 import 一个同层包？若是，能否改成接口？
- [ ] 我是否在 L2/L3 直接 `exec` / 直接 `http.Get` 外部服务？应该走 L4 适配器。
- [ ] 我是否在 main.go 之外做 `New*()` 装配？应该回到 L1。
- [ ] 新增的工具/钩子是否注册进了 `tools/all.go` 或对应 registry？

---

## 8. 变更本文件

这份架构说明和 `architecture.svg` 应当**随重大重构同步更新**。改依赖方向、新增/删除包、
调整分层时，请一并更新这两份文件——它们是团队（含未来的你）建立掌控感的地图。
