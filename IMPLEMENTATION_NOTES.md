# Implementation Notes — Brain P0 (K1–K7)

> 日期：2026-06-19
> 范围：BRAIN_DESIGN.md §10 P0 表的 makro 侧（K1–K7）。memory-cli 的 M1–M5 由另一项目并行做，本轮不碰。
> 状态：`go build ./...` / `go vet ./...` / `go test ./...` 全绿。

---

## 1. 每项 K 做了什么 + 改了哪些文件

| K | 做了什么 | 文件 |
|---|---------|------|
| **K7** | `config.Config` 加 `Brain BrainConfig` 段（toggle、daily/weekly cap、confidence_threshold、cron time、memory endpoint/key/cli）；`DefaultBrainConfig()` 给默认值；`applyEnvOverrides()` 加 `MAKRO_BRAIN_*` 一族 env 覆盖；`normalizeBrain()` 在 file unmarshal 后补回被 partial JSON 抹掉的默认值。 | `internal/config/config.go` |
| **K6** | 新包 `internal/brain/`：`memory_client.go`。封装 REST `:8765`（`POST /memories`，带 `Authorization: Bearer <key>`）→ CLI `~/bin/memory write` 兜底 → 死信队列 `~/.makro/brain/write-failed.jsonl` 三级降级。`WriteCapture` 是 P0 唯一完整实现的写路径；`List/PatchMetadata/WriteProposal/WriteFeedback` 为 P1 声明接口、实现留 `ErrNotImplemented`。`ReplayDeadLetter` 在 brain wake 时冲死信。 | `internal/brain/memory_client.go`（新） |
| **K1** | `capture.go`：异步 capture sink。`Capture()` 入口永不阻塞（发 in-process channel，channel 满 >256 丢最旧），worker goroutine 异步写 memory + 指数退避重试（0/1s/4s/16s）+ 死信。**噪声过滤表照搬 `~/.claude/hooks/memory-stop.sh`**（地雷 2）。LRU 去重（自写 `dedupCache`，不引入 `hashicorp/golang-lru` 依赖）。 | `internal/brain/capture.go`（新） |
| **K2** | `AgentNotifier` 加 `capture` socket 消息类型 + `OnCapture(session, prompt, cwd)` 回调。`hookPayload` 加 `Payload` 字段。`handleConn` switch 加 `case "capture"`，用 `parseCapturePayload` 解析转发来的 `UserPromptSubmit` stdin JSON。现有 `agent_stop/chat/session/permission/claude_session_start` 全部不动（向后兼容）。 | `internal/agent/notifier.go` |
| **K3** | `makro capture <session>` subcommand：读 stdin（hook 的 `{"prompt","cwd"}` JSON）→ 经 Unix socket 转发 `{type:"capture", session, payload}`。makro 没跑时静默退出（best-effort，绝不卡 hook）。 | `main.go` |
| **K4** | `EnsureUserPromptCaptureHook(claudeDir, exePath)`：跟现有 `EnsureStartHook` 同款 idempotent 注入 `UserPromptSubmit` hook。命令 = `<exe> capture "$(tmux display-message -p '#{session_name}')"`。**coexist 而非覆盖**：作为新 group 追加，绝不删现有 `notify ... start` 条目。`captureHookExists`/`isMakroCaptureHook`/`buildCaptureHookCommand` 辅助函数。 | `internal/agent/hook_config.go` |
| **K5** | TUI：`AppModel` 加 `captureFn` + `SetCaptureFn`，`processOrchestratorInput` 入口截获用户消息 → sink（source=`makro`）。GUI：`ChatService` 加 `capture` 字段，`SendMessage` 入口截获（source=`makro`）。两边都注册 `OnCapture` 回调（source=`claude`，来自 hook）。两边都在启动时 `EnsureUserPromptCaptureHook`。 | `internal/tui/app.go`、`cmd/gui/chat_service.go`、`main.go` |

### 新增测试

- `internal/brain/brain_test.go`：21 个测试。覆盖噪声过滤表、dedup LRU（含 FIFO 驱逐）、`buildCaptureMemory` 全字段形状（验收 #1）、REST 成功、**REST 挂 → CLI 兜底 → 死信**（验收 #2）、死信 replay、**capture 永不阻塞**（500 次 capture < 500ms，验收 #2 压测点）、disabled no-op、P1 stub 返回 `ErrNotImplemented`、source 常量唯一性。
- `internal/agent/hook_config_test.go`：+3 个测试（`TestEnsureUserPromptCaptureHookAdds` / `Idempotent` / `PreservesExistingStartHook`）。最后一个验证新 hook **不破坏现有 `notify ... start` 条目**（验收 #4、#5）。

---

## 2. 地雷 1 核实结果（stop-hook.sh 真实路径 + source/scope）

**事实（读 `~/.claude/hooks/memory-stop.sh` 源码）**：BRAIN_DESIGN §0.1 的 #1 和 #3 都对，是**两个不同阶段**，不矛盾：

- **去重检查阶段**（line 98–103）：`curl -X POST http://localhost:8090/a2a`，`method: memory_list`，按 `tags=claude-session,<project>` 查最近 5 条做 content_hash 比对。走 `:8090/a2a` JSON-RPC（transport HTTP）。
- **写入阶段**（line 129–131）：`~/bin/memory write body --category knowledge --tags claude-session,<project> --source claude`。走 **CLI 子进程直打 SQLite**（不经 daemon）。

**关键字段差异（决定去重能否生效）**：

| 字段 | stop-hook.sh | 我的新 capture | 能否 content_hash 去重 |
|------|-------------|---------------|----------------------|
| category | `knowledge` | `capture` | ❌ 不同 |
| source | `claude` | `claude` | ✅ 相同 |
| scope | （不传 = 默认 global） | `agent:claude` | ❌ 不同 |
| role | （不传） | `user` | ❌ 新字段 |
| project | （不传，只在 tag 里） | `<cwd basename>` | ❌ 不同 |
| tags | `claude-session,<project>` | `chat-capture,<session>` | ❌ 不同 |

**结论：双写期内跨管道 content_hash 去重不会生效**（category/tags/scope 全不同，memory 的 content_hash 去重若考虑这些字段会当两条；即便只看 content body，两管道对同一条消息的 body 处理——stop-hook 截断到 300 字、capture 截断到 500 字——也可能不同）。

**对设计的影响 / 我的判断**：
1. 双写期（BRAIN_DESIGN §3.7）会出现**重复记忆**，但这是可接受的过渡态——memory 的 dedup pipeline 会最终合并相似内容。
2. **不强行对齐 source/scope**：capture 用 `scope=agent:claude` + `category=capture` 是为 brain 的 R1 查询锚点（`tags=chat-capture`）服务，是有意为之的结构化标记；stop-hook 的 `knowledge/claude-session,<project>` 是旧结构。对齐会牺牲 capture 的查询能力，不值得。
3. **切换策略不变**：观察覆盖率后，把 stop-hook.sh 的 makro 段注释掉（保留 car-agent 段），从根上消除双写。切换前不需要"验证两侧同 hash"那条（RECONCILE R-rec-2 的建议）——因为本就不同 hash，靠切换而非对齐去重。

---

## 3. 与 memory 联调的待办点（等 M1/M3 起来后验证）

| 等 memory 的什么 | 验证什么 | 怎么验 |
|-----------------|---------|--------|
| **M1**（`POST /memories` 全字段：project/role/metadata） | capture 能写出完整 §9.3(a) JSON | `curl -X POST :8765/memories -d '{"content":"t","category":"capture","project":"x","role":"user","metadata":{"k":"v"}}'` 返回 2xx，且 `memory list` 能看到 project/metadata |
| **M3**（`:8765` auth 中间件） | 不带 key 被拒，带 key 通过 | 不带 `Authorization` → 401；带 `Bearer <key>` → 201 |
| **M3** | 配置里的 key 生效 | 把 `cfg.Brain.MemoryAPIKey` 从占位符 `mk-car-agent-abc123` 换成真实 key 后，capture 写入成功 |
| **M5**（daemon launchctl load + `:8765` 常驻） | REST 主通道可达，否则全程走 CLI 降级 | `lsof -iTCP:8765` 有监听；杀掉 daemon 后 capture 走 CLI（但 CLI 写是 lossy 的，丢 project/role/metadata） |
| **M2**（`GET /memories?tags=&from=`） | P1 的 R1/R3/R4/R5 查询能跑（本轮 P1 方法已 stub，但接口形状已定） | 联调时把 `Client.List` 从 `ErrNotImplemented` 填实现 |
| **M6**（`PATCH /memories/{id}`） | P1 proposal 状态机能用 | 联调时填 `PatchMetadata` 实现 |

**注意**：本轮 capture 写入在 memory M1 未就位时**也能工作**（降级链保证），但写出的记录在 CLI 降级路径下是 **lossy** 的（`memory write` CLI 当前不支持 `--project/--role/--metadata`，只有 REST 主通道才能写全字段）。所以 M1 之前 capture 实际只有 content/category/source/scope/tags 存得全，project/role/metadata 在 REST 不通时丢失。这是已接受的过渡态。

---

## 4. 设计文档没覆盖处，我做的判断

### 4.1 `capture` 消息的 payload 传递方式
设计文档 §3.3 只说"hook 把 stdin JSON 提取出来发 socket"，没说**整段 JSON 透传还是解析后逐字段发**。我选了**整段透传**（`msg.Payload` = 原始 stdin JSON），在 server 侧 `parseCapturePayload` 解析。理由：
- socket payload 是 `map[string]string`（现有约定），嵌套 JSON 当字符串传最省事；
- server 侧解析 = 单一解析点，避免 client/server 两边各解析一次的漂移；
- makro 自己 chat 的 capture 路径（不经 hook）直接把文本放 `msg.Content`、cwd 放 `msg.Cwd`，server 侧 `parseCapturePayload` 返回空时回退到这俩字段——两条路径统一到一个 `onCapture(session, prompt, cwd)` 回调。

### 4.2 dedup 用自写 LRU 而非引入依赖
设计文档 §3.5 只说"LRU 去重"，没指定库。`hashicorp/golang-lru/v2` 是 Go 生态标准，但加它是个新依赖（SPEC "Ask First"）。512-key 的热路径检查，自写一个 FIFO `dedupCache`（map + 切片）零成本且零依赖。FIFO 而非真 LRU 的取舍已在代码注释里写明（用户重发通常紧跟首次发送，recency ≈ 插入序）。

### 4.3 `BrainConfig.Enabled` / `CaptureEnabled` 的默认值歧义
`json.Unmarshal` 把 partial `{"brain":{}}` 里的 bool 字段置零（false），无法和"显式 false"区分。我的处理：
- `DefaultConfig()` 设两者为 `true`（新装默认开 capture）；
- `normalizeBrain()` **不**触碰这两个 bool（显式零保持零）；
- 唯一边界：config.json 里写 `"brain": {}`（存在但空）会禁用。这在 IMPLEMENTATION_NOTES 里点明，且 opt-out 是显式行为，可接受。

### 4.4 `source` 语义的代码注释
任务约束强调"source = 写入者，不是被描述对象"。我在 `memory_client.go` 顶部用常量块 + 注释固化这个语义：
- `claude/copilot/makro` = 消息流经该 agent 的 session（用户打的，但通过该 agent）；
- `makro-brain` = brain 自己写的（proposal/self-tune/outcome）；
- `human` = 用户在 makro 里直接操作（accept/reject），写的是 makro 进程代用户行事。
K5 里 `OnCapture` 回调显式传 `brain.SourceClaude`（来自 hook）vs `brain.SourceMakro`（来自 makro 自己 chat），不混。

### 4.5 capture 截获点放 `processOrchestratorInput` 入口而非 `handleLLM`
设计文档 §3.3 已论证（入口拿原始文本、统一三路径）。实现里 TUI 的 `processOrchestratorInput` 和 GUI 的 `SendMessage` 各加一行，**都在 voice-prefix 之前**截（capture 拿用户原话，不拿 TTS 指令前缀）。

### 4.6 GUI 的 capture 字段 vs captureFn 函数
TUI 用 `captureFn func(...)`（注入闭包），GUI 用 `capture *brain.CaptureSink`（直接持有）。不一致是因为 GUI 的 `ChatService.init()` 里能直接构造 sink 并存为字段，无需函数注入；TUI 的 `AppModel` 是值类型且 sink 生命周期跟 main.go 走，函数注入更干净。两者都 nil-safe。

---

## 5. 验收对照

| 验收标准 | 状态 | 证据 |
|---------|------|------|
| **#1 全字段写入就绪** | ✅ 代码层 | `TestBuildCaptureMemoryShape` 断言 category/source/role/scope/project/tags/metadata 全字段；`TestWriteCaptureRESTSuccess` 断言 REST 实际收到这些字段；`TestWriteCaptureFallsBackToCLIWhenRESTDown` 验证 CLI 降级；`TestWriteCaptureDeadLettersWhenAllPathsFail` 验证死信 |
| **#2 capture 不卡聊天** | ✅ 单元层 | `TestCaptureNeverBlocks`：500 次 capture（client 全程 down）在 < 500ms 返回；`TestCaptureDisabledIsNoOp` 验证 disabled 时不写不报错。**实测联调**：等 memory daemon 就位后，手杀 daemon + 在 makro 聊天，确认体感零延迟、死信有堆积（这条要真人验） |
| **#3 只截用户侧** | ✅ 架构层 | hook 点 = `UserPromptSubmit`（Claude 只在用户提交时触发，agent 回复走 Stop/PostToolUse，不进此 hook）；TUI/GUI 截 `processOrchestratorInput`/`SendMessage`（用户输入入口）。**实测联调**：和 Claude 聊一轮，`memory list --tags chat-capture` 应只见用户消息 |
| **#4 hook 注入幂等** | ✅ | `TestEnsureUserPromptCaptureHookIdempotent`：跑 3 次只 1 条 capture hook |
| **#5 向后兼容** | ✅ | `TestEnsureUserPromptCaptureHookPreservesExistingStartHook`：现有 `notify ... start` 条目不被删；notifier 的 `agent_stop/chat/session/permission/claude_session_start` 全部不动，`go test ./internal/agent/` 全绿 |

---

## 6. 下一轮（memory M2/M6 就位后）做什么

1. 填 `Client.List`（依赖 M2 的 `tags=`/`from=`）和 `PatchMetadata`（依赖 M6）的实现。
2. P1 闭环：`brain.go`/`wake.go`/`reader.go`(R1–R6)/`propose.go`/`inbox.go`/`feedback.go`（改动清单 §10 P1 的 K8–K10）。
3. 把 `cfg.Brain.MemoryAPIKey` 占位符换成 memory M3 发的真实 key。
4. 联调实测验收 #2、#3 的真人部分。

---

## 7. 配置联调结果（2026-06-19，memory M1–M7 全部就位后）

memory-cli 侧的 P0/P1（M1–M7）已全部完成（见 memory-cli PROGRESS.md 的 `1f90138` + `7419920`）。本轮把 makro 接上去并完成端到端真人验证。

### 7.1 配置动作

| 动作 | 内容 |
|------|------|
| **memory daemon 常驻** | 重建 `memory-cli/memory` binary 到 HEAD（含 M1–M7）；`launchctl load com.cybernagle.memory-daemon.plist`（之前 plist 在但没 load，是 BRAIN_DESIGN §11 R6 担心的"地基悬空"）。daemon PID 持续运行，`:8765` REST + `:8090` transport + `~/.memory/memory.sock` 三套都 LISTEN。 |
| **makro config** | `~/.makro/config.json` 加 `brain` 段：endpoint=`http://127.0.0.1:8765`、key=`mk-car-agent-abc123`、cli=`/Users/naglezhang/Desktop/Code/memory-cli/memory`、caps/threshold/cron 默认值。 |
| **Makro.app 重新打包** | Go binary → Vite → electron-builder `--dir`（遵守 makro-build skill 的三个 trap：`--dir` 不打 DMG、`cd && ./node_modules/.bin/`、`rm -rf release/` 避免旧 binary 残留）。deploy 到 `/Applications/Makro.app`，hash 验证一致（Trap 3 ✓）。 |

### 7.2 端契约实测（全部通过）

- **auth（M3）**：无 key → 401，错 key → 401，对 key → 201。
- **全字段写入（M1）**：POST `:8765/memories` 带 project/role/metadata，读回确认全部持久化（`project=makro`、`role=user`、`metadata={prompt_hash, session}` 都在）。
- **查询（M2）**：`GET /memories?category=capture&tags=chat-capture` 正常过滤。
- **死信队列**：联调期间全程为空（capture 从未失败）。

### 7.3 两条 capture 路径真人验证（验收 #2、#3）

| 路径 | 验证方式 | 结果 |
|------|---------|------|
| **makro app chat** | 在 Makro.app 聊天框发"你好呀" | ✅ memory 出现 `source=makro`、`scope=agent:makro`、`role=user` 的 capture 记录 |
| **Claude Code** | 在 claude tmux session 发"this is only a test msg..." | ✅ memory 出现 `source=claude`、`scope=agent:claude`、`role=user`、`session=review` 的 capture 记录，**agent 回复未被截** |

**验收 #3 铁证**：Claude Code 路径的记录只有一条（用户消息），没有 assistant 记录——`UserPromptSubmit` hook 只在用户侧触发的设计成立。

### 7.4 联调中发现并修复的 bug

**bug：`makro-serve`（GUI 二进制）缺 `capture` 子命令。**

- 现象：第一轮 Claude Code 路径 capture 失败，hook 调 `makro-serve capture` 报 `makro-serve must be run with the 'serve' subcommand`，16ms 返回错误。
- 根因：我在 K3 只给根 `makro`（仓库 main.go）加了 `capture` 子命令，但 Claude hook 调的是 `makro-serve`（`cmd/gui/main.go`，**独立的 main**），它的 subcommand switch 只有 `notify/permission/claude-start/serve`，没有 `capture`。所以 makro app chat 路径能 capture（走 `SendMessage` → `s.capture.Capture`，不经子命令），但 Claude Code 路径不行（走 hook → `makro-serve capture`）。
- 修复：`cmd/gui/main.go` 加 `case "capture"`，读 stdin JSON + session 参数，经 `notify.SendHook` 转发 `{type:"capture", session, payload}`。重新打包部署后 Claude Code 路径通过。
- 教训：仓库有两个 main（`main.go` 根 makro + `cmd/gui/main.go` makro-serve），hook 转发类子命令（notify/permission/capture/claude-start）两边都要有。**两个 main 的 subcommand 必须同步**。

### 7.5 验收最终对照

| 标准 | 状态 | 证据 |
|------|------|------|
| #1 全字段写入 | ✅ 实测 | §7.2 + 单元测试 |
| #2 capture 不卡聊天 | ✅ 实测 | 用户确认"速度没啥问题"；capture hook 实测 16ms；死信队列空 |
| #3 只截用户侧 | ✅ 实测 | §7.3 Claude Code 路径只有 user 记录，无 assistant |
| #4 hook 注入幂等 | ✅ | 单元测试 + 实际 settings.json 只有 1 条 capture hook |
| #5 向后兼容 | ✅ | 现有 agent_stop/chat/session/permission 不动；claude-start 保留 |

### 7.6 "变慢"排查结论

联调中一度报告 makro 回复变慢，命中地雷 3 担心。排查结论：
- capture 写 memory 全程无死信、无错误，异步设计正确；
- capture hook 执行时间实测 16ms（不卡 Claude prompt 提交）；
- `SendMessage` 路径的 `s.capture.Capture(...)` 是纯异步（filter→dedup→channel send，全内存，微秒级）；
- 用户后续确认"速度没啥问题" → 是 glm-4.7 的正常网络/推理波动，与 capture 无关。

### 7.7 部署清单（再次部署时照做）

```bash
# 1. memory daemon（若没跑）
cd /Users/naglezhang/Desktop/Code/memory-cli && go build -o memory .
launchctl load ~/Library/LaunchAgents/com.cybernagle.memory-daemon.plist

# 2. makro Makro.app（三个 trap）
cd /Users/naglezhang/Desktop/Code/Makro
go build -o cmd/gui/bin/makro-serve ./cmd/gui/
cd cmd/gui/frontend && npm run build && cd ..
rm -rf release/ && ./node_modules/.bin/electron-builder --dir
md5 release/mac-arm64/Makro.app/Contents/Resources/bin/makro-serve bin/makro-serve  # 必须一致

# 3. deploy（orphan makro-serve 会存活, 必须 pkill -9）
pkill -9 -f makro-serve; pkill -9 -f Makro; sleep 1
rm -rf /Applications/Makro.app
cp -R release/mac-arm64/Makro.app /Applications/
open /Applications/Makro.app
```

---

## 8. P1 实现 + 联调结果（2026-06-19，brain 提案闭环）

P1 = brain 真正"当老板推 proposal"。范围严格对齐 BRAIN_DESIGN §10 P1（K8/K9/K10）：brain 读 memory → LLM 生成 proposal → 推 Bark + chat → `/inbox accept|reject` 闭环 → feedback 写回 memory。**P1 不含** web 调研（K14）、TUI inbox view（K11）、独立 launchd daemon（K16）——这些是 P2。

### 8.1 P1 做了什么 + 文件

| 模块 | 文件（新建）| 做什么 |
|------|-----------|--------|
| memory_client P1 方法 | `memory_client.go`（改）| 实现 List（GET tags/from/limit）、PatchMetadata（PATCH merge）、WriteProposal、WriteFeedback；去掉 P0 的 ErrNotImplemented。新增 writeAndReturnID（POST 返回 memory UUID，供 feedback 回链）|
| reader | `reader.go`（新）| R1-R5：RecentCaptures（capture+knowledge 合并）/ Profile（character+preferences+soul）/ OpenProposals（按 metadata.status 过滤）/ RecentFeedback / RecentProposalTitles |
| propose | `propose.go`（新）| 镜像 guardian.go：单次 provider.Complete + brace-scrape JSON。Proposal{Title,Body,Confidence,Domain,Reason}。confidence clamp [0,1] |
| inbox | `inbox.go`（新）| SQLite 缓存（镜像 usage/store.go：WAL+MaxOpenConns(1)）。proposals 表 + brain_state 表。Add/MarkStatus/Get/ListOpen/RecentDomains/CountToday/StateGet/Set。FormatProposalForChat |
| feedback | `feedback.go`（新）| ApplyFeedback：PATCH proposal metadata.status + 写 category=feedback 记忆（verdict 进 tag+metadata）+ 更新 inbox 缓存。返回确认消息 |
| brain | `brain.go`（新）| Brain struct + Run（cron ticker 08:00）+ WakeNow（手动触发）+ runWake（串行：读死信→R1-R5→冷启动守卫→daily cap→propose→confidence 门控→domain 去重→写 memory+inbox→推送）。TryLock 串行化 |
| commands | `commands.go`（新）| `/brain wake`（异步触发）、`/inbox`（列 open）、`/inbox accept <id> [reason]`、`/inbox reject <id> [reason]`。RegisterCommands(registry, brain) |
| wiring | `main.go` + `chat_service.go`（改）| 启动时构造 Brain、注册命令、起 cron goroutine。pusher 实现：TUI 经 sendMsg→ExternalChatMsg；GUI 经 s.emit("chat:system")；两边都 BarkPush |

测试：brain 包从 P0 的 21 个增到 **48 个**（propose brace-scrape、inbox SQLite CRUD、feedback 端到端、reader 过滤、capture isCaptureable）。build/vet/test 14 包全绿。

### 8.2 联调中发现并修复的 4 个 bug

**bug 1：capture 截了控制命令。**
`/brain wake`、`@mention`、`&monitor` 这类 slash/路由命令被 capture 当成 idea 写进 memory，污染 R1 矿。修复：`isCaptureable()` 在 Capture() 入口过滤 `/`、`@`、`&` 开头的控制输入。

**bug 2：R1 冷启动只读 capture。**
原 reader 只查 `category=capture`（P0 新管道，0 条历史），无视 memory 里 stop-hook+fact-processor 攒的 **17127 条 knowledge**。brain 第一次 wake 永远"信号不足"。修复：R1 同时读 capture（未来增量）+ knowledge（历史金矿），合并返回。

**bug 3：WakeNow 继承了被 cancel 的请求 ctx（最隐蔽，卡了 3 轮）。**
`/brain wake` 跑在 orchestrator 的请求级 ctx 里，命令 goroutine 一返回（发完"已触发"）就 `cancel()`。原 `WakeNow(ctx)` 把这个**已 cancel 的 ctx** 传进后台 runWake → 所有 memory 读撞击 cancelled ctx → R1 返回空 → 假"信号不足"。诊断时用独立 ctx 跑 reader 测试却能读到 50 条，一度以为是 reader bug。修复：`WakeNow` 用自己的 `context.Background()`，只绑定 brain.stop。**教训：后台任务绝不继承请求级 ctx。**

**bug 4（P0 遗留）：makro-serve 缺 capture 子命令。**
P1 期间发现 Claude Code 路径 capture 失败——根 `makro` 有 capture 子命令但 hook 调的是 `makro-serve`（cmd/gui 独立 main）。P0 已修（见 §7.4），这里重申：**两个 main 的 hook 转发子命令必须同步**。

### 8.3 真人验收（通过）

| 验收点 | 结果 |
|--------|------|
| `/brain wake` 触发 | ✅ |
| R1 读 memory（knowledge 50 条）| ✅（proposal 内容引用了真实历史"zcode adapter"）|
| LLM 生成 proposal | ✅（`🧠 [#1] 完成 zcode adapter 修复 (confidence 90%, domain: Debugging)`）|
| 推 chat 系统消息 | ✅ |
| `/inbox reject 1 原因` 闭环 | ✅（"❌ proposal #1 已 rejected，反馈已写回 memory，下一轮 wake 会读到"）|

**context-cancel bug 的诊断代价**：前 3 次 `/brain wake` 全报"信号不足"，因为反复验证 reader 代码是对的（独立测试能读 50 条），最终才发现是 ctx 生命周期问题。教训写进 §8.2 bug 3。

### 8.4 P1 遗留 / 下一步

- **`/inbox reject` 无 id 时报错不友好**（"无效的 proposal id：memory"）——把"memory"当 id 解析了。可改为提示用法。低优先。
- **调研（P2）暂不做**：brain 纯靠 memory 生成 proposal 已能跑（质量可接受）。用户决定先观察 P1 proposal 命中率几天再决定。调研方案已备好（智谱 web-search-prime + web-reader，HTTP API 直调，接口已确认）。
- **profile 说"接受率低/倾向拒绝"**：character 画像里写着 accept_rate 低，LLM 可能因此保守打分。观察几轮看 confidence 分布，必要时冷启动期临时调低 threshold（0.6）。
- **P2 候选**：TUI inbox view（Ctrl+B）、独立 launchd daemon（brain 常驻）、research.go（web 调研）、完成率回扫（K17）。

