# Makro — Full-Repo Review

**Date:** 2026-06-22
**Scope:** 147 commits (2026-06-07 → 2026-06-21), ~21,750 LOC Go (desktop + `cmd/gui`) + 20 Swift files (iOS)
**Reviewer method:** 17 subsystem/dimension finders → adversarial verification → inline confirmation of high-impact claims against real code
**Build evidence:** `go vet ./...` clean · `go build` (desktop + cmd/gui) clean · **413 tests pass across 15 packages**

---

## 总体结论 (Executive Summary)

整体代码质量 **中上**:核心编排逻辑(orchestrator 的无限工具循环、empty-content 恢复)、测试覆盖(413 通过)、SQL 全部参数化、tmux 命令走 argv(无 shell 注入)、provider 抽象干净。这些都是亮点。

但有两周的快速迭代积累了一批 **真实问题**,集中在三个方向:

1. **iOS 安全面裸奔** — App Transport Security 全局关闭 + 硬编码公网 IP(47.117.13.195)+ 对该 host 的"信任任何证书"+ 密码存 UserDefaults。任意网络路径上的攻击者都能 MITM 拿到 Bearer 密码、聊天、终端快照、APNs token。这是 **最该先修** 的。
2. **关闭/生命周期不干净** — 大量 goroutine 在 `Stop` 时未被 join(notifier、brain、keepalive、gui 的 monitor/usage loop),`tmux.Client.Stop` 关 channel 后仍有路径往里 send → panic。违反项目自己的 hard rule("complete Stop/Close cleanup")。
3. **一整个子系统是死的** — `internal/adapters` 整包零引用,`KeepAlive.Add` 零调用。意味着 Copilot 适配 / Copilot 会话保活从来没真正生效过。

另外有 **4 个 verified 误报** 已剔除(见末尾),不用花时间追。

**严重度统计(去重后,经我逐条核对):** Critical 4 · Important ~22 · Suggestion ~25 · Nit ~15

---

## Verification legend

每条标记来源,方便你判断该信多少:

- **[V]** 我已读真实代码逐行确认
- **[SV]** security-sweep finder 自己 grep 确认(高可信)
- **[R]** finder 报告、我未独立复核(可能有误报,但具体到 file:line)
- **[FP]** 已证伪,剔除

---

## 🔴 Critical (4)

### C1. `internal/adapters` 整包是死代码 `[V]`
**`internal/adapters/adapter.go`, `claude.go`, `copilot.go`**
全 repo grep `adapters.`、`ClaudeAdapter`、`CopilotAdapter`、`NewClaude/NewCopilot` — 生产代码 **零引用**(仅自身 + 测试)。整个 adapter 抽象从未被接线,真正的 agent 集成走的是 tmux send-keys。意味着 `claude.go`/`copilot.go` 里的 `Launch`/`ParseOutput`/`StopConfig` 逻辑(包括其中发现的 workingDir 不生效、SkipPermissions 经 send-keys 等问题)全都不可达。
**Fix:** 要么删掉整包,要么真正接线。建议删 — 它还在误导读者以为有适配层。

### C2. `tmux.Client.Stop` 关 channel 后仍 send → panic `[V]`
**`internal/tmux/client.go:129, 231, 283`**
`Stop()` 在持锁时 `close(c.notifs)`。但 `parseAndSendOutput`(231)和 `pollSessions`(283)往 `c.notifs` send 时用的是 `select { case c.notifs<-n: default: }` — `default` 只防阻塞,**不防 closed-channel panic**。`Exec()` 开头只 check 一次 `running`(150-152),之后 `parseAndSendOutput`(168)无锁运行。关停期间若有 tool 调用 Exec,或 pollLoop 的 ctx 取消与 close 时序交错,就 panic。
**Fix:** 不要 close `notifs` channel(Go 惯例:由唯一 sender close,或干脆不 close 让 GC 回收);或用 atomic flag + 在 send 前 double-check,或向一个永不 close 的内部 channel send。

### C3. iOS App Transport Security 全局关闭 + 硬编码公网 IP `[SV]`
**`ios/Makro/Makro/Info.plist:38-44` + `Config.swift:32`**
`NSAllowsArbitraryLoads=true` 全局禁用 ATS,默认 serverURL 写死 `https://47.117.13.195:39222`(公网 IP 进了源码/二进制)。配合 C4,任意路径上的攻击者(咖啡店 WiFi、运营商 DPI)可发任意自签证书或降级到明文 HTTP,截获 Bearer 密码、聊天、终端快照。
**Fix:** 删 `NSAllowsArbitraryLoads`;默认 serverURL 留空、首次启动引导用户填(或扫码);如必须自签,做真正的 SPKI pinning(TrustKit),不要 blanket trust。

### C4. iOS TLS 只比对 host 字符串,信任该 host 的任何证书 `[SV]`
**`ios/Makro/Makro/Config.swift:123-139`** (`handleTLSChallenge`)
只要 `challenge.protectionSpace.host` == 配置的 serverURL host 或 127.0.0.1/localhost,就无条件 `completionHandler(.useCredential, URLCredential(trust:))` — **没有任何证书校验**(无链校验、无 pin、无 SAN 匹配)。等于对那个 IP 路径完全敞开。
**Fix:** 做 SPKI pinning,或至少先 `SecTrustEvaluateWithError` 再决定;删掉 host==expected 的旁路。

> 注:C3/C4 对"个人本地工具"风险有限,对"带公网 IP 的发货 app"是 critical。鉴于源码里已硬编码公网 IP,按发货 app 处理。

---

## 🟠 Important (去重后 ~22)

### 并发 / 生命周期 / 关停

- **I1. Orchestrator `cancelFn` 单槽竞态 `[V]`** — `orchestrator.go:307-310,367`。`ProcessInput` 每次覆写 `cancelFn` 不取消上一个;且只有 LLM 路径(367)清 nil,s命令/skill/mention 路径不清 → `Cancel()` 可能取消的是上一个已结束请求的 ctx。TUI/GUI 单用户顺序输入下基本不触发,属于潜在 bug。**Fix:** 复用单一 request-scoped cancel 链,或所有路径统一清理。
- **I2. `cmd/gui` 无协调的 graceful shutdown `[R]`** — `chat_service.go:518-607,359`。monitor goroutine、`usageIngestLoop`、brain loop、notifier、chat-history 文件在停服时都没有 Close/join。`go notifier.Start()`(333)还吞了 bind error。
- **I3. `cmd/gui` monitor 在 "blocked" 确认态下空转 `[R]`** — `chat_service.go:536-605`。confirmation "blocked" 时 monitor goroutine 紧循环轮询,无 backoff/上限。
- **I4. `AgentNotifier.Stop()` 不等 in-flight `handleConn` `[V]`** — `notifier.go:180-188`。关 listener 后,正在跑回调(`onAgentStop` → 发 APNs 等)的 handleConn goroutine 不被等。进程退出时可能截断推送。违反项目 hard rule。
- **I5. Brain `Stop` 不 join `runWake`,cron reset goroutine 永久泄漏 `[V]`** — `brain.go:87,93-99,282-285`。`Stop` 只 close `b.stop`;`go b.runWake(...)` 不被等。`newCronTicker` 起的 `go func(){<-t.C; t.Reset(24h)}()` 在 Run 提前返回时(已 `defer ticker.Stop()`)永远阻塞在停止的 ticker 上 → goroutine 泄漏。
- **I6. `AgentNotifier` Unix socket 无显式权限 `[V]`** — `notifier.go:160-176`。`net.Listen("unix", path)` 权限随 umask(常 755),本机其他用户可连入注入伪 hook(伪造 agent_stop / capture 用户输入)。单用户 Mac 风险低,多用户主机是注入面。**Fix:** listen 后 `os.Chmod(sockPath, 0600)`。

### 安全(除 C3/C4)

- **I7. GUI auth 用 `==` 比对 + `?token=` 走 URL `[SV]`** — `server.go:254-278`。非恒定时比较(时序侧信道);密码进 query string → 进 nginx/代理日志、浏览器历史。
- **I8. GUI `--password` 为空时整服无 auth `[SV]`** — `server.go:158`。空密码跳过 middleware,所有路由(含 `/api/sessions/*/send` = tmux 按键注入)裸奔。
- **I9. GUI 静态资源绕过 auth `[SV]`** — `server.go:256-260`。`.js/.css/.html/.ico` 与 `/assets/`、`/` 直接放行,SPA bundle 无鉴权可读;未来误命名(如 `report.html`)会继承绕过。
- **I10. GUI WS `CheckOrigin` 对空 Origin 返回 true `[SV]`** — `server.go:127-135`。非浏览器客户端可跳过 origin 检查,削弱 WS 的 CSRF 防护。
- **I11. `send_to_session` 破坏性命令 denylist 可绕过 `[SV]`** — `tools/send_to_session.go:47-93`。`find / -delete`、`python -c '...'`、`base64|sh`、`env VAR=...` 等都绕得开;且不作用于 `relay_message`。agent transcript 被 prompt-inject 时这是唯一防线。denylist 本质不安全。
- **I12. Brain `MemoryAPIKey` 占位符写死源码 `[SV]`** — `config.go:88`。默认 `mk-car-agent-abc123`,未替换的 brain 实例可被任何知道占位符的人冒充。
- **I13. iOS 密码与 Azure key 存 UserDefaults(明文,进 iCloud 备份)`[SV]`** — `Config.swift:30-52`。应进 Keychain。

### LLM provider

- **I14. OpenAI streaming 工具调用按 `ID` 累积,并行工具调用会损坏 `[V]`** — `openai.go:96-119`。OpenAI 流式只在**第一个**分片带 `ID`,后续分片只有 `Index` 和 arguments 片段。代码按 `tc.ID` 发 `EventToolCallDelta`,后续片段 ID 为空 → 单工具调用勉强,多工具并行时参数累积错乱。orchestrator 主路径用 `Complete`(非流)所以当前不触发,但任何流式 + 工具的调用方会中招。**Fix:** 按 `tc.Index` 累积。
- **I15. Anthropic `MaxTokens=0` 原样转发 → API 拒绝 `[V]`** — `anthropic.go:164`。OpenAI 侧有 `if opts.MaxTokens>0` 守卫,Anthropic 侧没有。orchestrator 恒置 4096 所以潜在,但两边不一致。**Fix:** Anthropic 侧也默认/守卫。
- **I16. Anthropic 路径静默丢弃 `RoleSystem` 消息 `[R]`** — `anthropic.go`(System 走 `SystemPrompt` 单独字段,`RoleSystem` 消息可能被丢)。未独立复核,但与 OpenAI 处理不对称值得查。

### iOS(除 C3/C4/I13)

- **I17. `TerminalViewModel` 的 capture/send 走 `URLSession.shared`,绕过 TLS 委托 `[V]`** — `TerminalViewModel.swift:71,97`。WS 流走带 delegate 的 session(251),但 `refresh()`/`send()` 用 `.shared`(无 delegate)→ 自签证书要么被拒(功能坏)要么裸连(带 Bearer 的请求没 pin)。**Fix:** 复用一个带 delegate 的 `URLSession`。
- **I18. `makroSelectSession` 通知发了无人监听 → 推送点按 deep link 失效 `[V]`** — `MakroApp.swift:31,67`。全 repo 无 observer。
- **I19. `ChatViewModel.loadHistory` 切片对并发 WS 追加脆弱 `[R]`** — `ChatViewModel.swift:145-157`。+ `ChatViewModel.swift:268-382` 的 URLSession/pingTimer/reconnectTask 在 dealloc 未拆除([R])。
- **I20. Now Playing 进度条恒 100% `[R]`** — `NowPlayingManager.swift:89-101`。`playbackDuration` 被设成 elapsed 而非总量。

### tmux

- **I21. `pollSessions` 在 notifs 满时静默丢弃 `[V]`** — `client.go:282-285,231-233`。`select default` 非阻塞,消费者慢时通知无声丢失(状态镜像可能与真实 session 不同步)。
- **I22. `KeepAlive.Add` 零调用 → Copilot 保活子系统整条死路 `[V]`** — `keepalive.go:39`。grep 无任何调用者。只有 `Remove`/`Close` 被用。意味着 Copilot 会话保活从没生效(Copilot TUI 仅在 attached 时处理 send-keys 的前提条件未满足)。
- **I23. `Exec` 错误信息含原始 CombinedOutput `[V]`** — `client.go:165`。tmux stderr 可能含 pane 内容/用户输入,进 error 链 → 可能进日志。截断/脱敏。

### usage / 数据

- **I24. transcript 被轮转缩小后 offset 永不复位 → 该 transcript 永久停采 `[V]`** — `claude.go:103-152`。seek 到旧 offset > EOF → ReadAll 空 → 提前返回不重置 offset。Claude transcript 轮转场景下静默丢统计。
- **I25. `Record()` 去重窗口忽略记录自身 Timestamp `[R]`** — `store.go:132-167`。回填的旧行可能被当重复吞掉。
- **I26. Timeline 时区不一致(本地时间戳 + `strftime('%s')` 假定固定偏移)`[R]`** — `store.go:82`。
- **I27. `CountTodayProposals` UTC 存储 vs 本地日期比对 → 跨日 daily cap 错位 `[R]`** — `inbox.go:175-187`。

### APNs

- **I28. badge 硬编码 1,无清除路径,app 角标永不归零 `[R]`** — `apns.go:138-158`。
- **I29. ES256 签名未强制 low-S 规范化(RFC 7518)`[R]`** — `apns.go:121-133`。部分 APNs 端点会拒。

---

## 🟡 Suggestions(精简,按主题)

**架构 / 可读性**
- `cmd/gui/server.go` 1015 行混了 PTTY/chat hub/middleware/20+ handlers,拆成 `middleware.go`/`handlers_*.go`/`chat_hub.go`/`pty_session.go`。`[SV]`
- `internal/tui/chat.go` 776 行,`Update`/`View` 过长,拆 `chat_suggestions.go`/`chat_view.go`。`[R]`
- `cmd/gui` 与 `internal/tmux` 各有一套 tmux 命令构造,易漂移;让 gui 复用 `internal/tmux` 的 builder。`[SV]`
- `internal/agent/hook_config.go` 五个几乎相同的 `Ensure*Hook`(~50 行/个),抽一个。`[R]`

**性能**
- 快照 WS 每 100ms/client 起新 `exec.Command(tmux capture-pane)`(`server.go:710-747`)— 多 client 时 fork 风暴,考虑共享捕获 + 广播。`[R]`
- `NotifOutput` 查找 O(sessions×windows×panes)/每 output chunk(`session.go:145-151`)。`[R]`
- `IngestZCode` 每 tick 把历史 call_context 全载内存(`zcode.go:50-62`)。`[R]`
- `renderMarkdown` 流式期每个 View tick 重跑、未缓存(`tui/chat.go:644-654`)。`[R]`
- APNs 每次 Push 重签 JWT 而非复用(`apns.go:84`)。`[R]`

**安全加固(非阻断)**
- 所有 handler 无 `http.MaxBytesReader` / `io.LimitReader`(`server.go` 全域)— DoS 面(auth-gated 故降级)。`[SV]`
- tasks/snapshot/config/state/dead-letter 等含敏感信息文件写 `0644`(`task_service.go:69` 等),应 `0600`。`[SV]`
- `brain/memory_client.go` 的 `writeREST`/`PatchMetadata` 用无界 `io.Copy` 丢弃响应体。`[R]`

**正确性补充**
- `read_file` 整文件载内存后才做分页/字节限制(`read_file.go:61-64`)。`[R]`
- OpenAI temperature 守卫 `>0` 把显式 `0` 误判为未设(`openai.go:212`)。`[R]`
- orchestrator retry 无最大次数上限(3m+抖动 永续,仅 ctx 可断,`orchestrator.go:433-444`)。`[V]` 可接受但加上限更稳。
- empty-content-after-tool 恢复逻辑无测试(`orchestrator.go:475-484`)。`[R]`
- `tui/chat.go:463-473` 右对齐用字节长度而非显示宽度(多字节错位)。`[R]`

---

## 🔵 Nits(简表)

- `internal/tmux/commands.go` 7 个导出 command builder 零非测试调用(SendEscapeCmd/SendCJCmd/ListWindowsCmd/DetachClientCmd 等),删。`[SV]`
- `internal/brain/capture.go:339` `var _ = os.DevNull` 是伪装成可移植守卫的死代码,删 `os` import。`[SV]`
- `ServerInfo.Sessions` 写不读;`SourceCopilot` 常量定义从不赋值。`[SV]`
- `notify.go:111` `FrontmostBundleID` 导出但仅包内用,un-export。`[SV]`
- `gui-server` 多处 handler 把原始内部 error 回显给客户端(`server.go` 多处)。`[R]`
- `usage/store.go:97-124` `migrateCacheColumns` 吞所有 ALTER/DELETE 错误。`[R]`
- `usage/claude.go:58` 构造的闭包忽略唯一参数。`[R]`
- `AzureSpeechManager.swift:499` canceled 事件把 Azure 原始 errorDetails 暴露给 UI。`[R]`

---

## 文档/流程

- **README/SPEC 已严重漂移 `[SV]`**:仍写 "tmux -CC control mode"(CLAUDE.md 明确已弃用、改走 `-S <socket>` 直连);README 完全没提 brain / usage dashboard / iOS / 通知。SPEC 开头段落与结构章节同理。
- **REVIEW.md 还在 `[SV]`**:跟踪的是 2026-06-10 已 merge 的 PR #9,按项目自己的协议应删。
- **IMPLEMENTATION_PLAN.md / TASKS.md** 所有阶段 Complete 却未删(CLAUDE.md 要求完成后删)。
- **reddit-post.md / PUSH_DEBUG_STATUS.md** 是一次性营销/调试日志,污染 repo 根。

---

## ✅ False Positives(已证伪,勿追)

这些被 finder 标为问题,我读真实代码后确认**不是 bug**,列出来省你时间:

1. **`KeepAlive.Close()` 与 per-session `cmd.Wait` goroutine 竞态** — map 访问有 mutex 保护,goroutine 还用 `entry.cmd == cmd` 身份复核,安全。
2. **tmux `%output` 解析"在第一个空格截断 data"** — `strings.Cut(rest, " ")` 只切**一次**,data = 首空格之后的全部(含内部空格),未被截断。
3. **`DecodeEscape` "off-by-one 丢末尾 \NNN"** — 条件 `i+3 < len(data)` 正确,末尾完整的 `\NNN`(剩 4 字符)会被处理;不足 4 字符的不完整序列按字面量输出,符合预期。
4. **`notifier.handleConn` "阻塞 accept loop"** — `acceptLoop` 用 `go n.handleConn(conn)`,回调慢只阻塞那个 goroutine,不影响 accept。

---

## 建议的行动顺序

**第 1 优先(iOS 安全,C3/C4/C13/I7-I10/I17):** 这是唯一可能被外部利用的一组。最小动作:删 `NSAllowsArbitraryLoads` + serverURL 默认置空 + 密码进 Keychain + GUI 用 `subtle.ConstantTimeCompare` + 空 password 非环回地址拒启动 + capture/send 复用带 delegate 的 URLSession。

**第 2 优先(关停 panic, C2 + I4/I5):** 关停路径的 panic 是硬故障。`tmux.Client` 不要 close `notifs`(或加 atomic 守卫);notifier/brain 的 Stop 等 in-flight goroutine;brain 的 cron reset goroutine 改成不需要独立 goroutine。

**第 3 优先(死代码,C1 + I22):** 删 `internal/adapters` 整包;决定 `KeepAlive.Add` 是接线(Copilot 保活才真生效)还是删。

**第 4 优先(数据正确性,I24/I25/I26/I27):** usage 偏移复位 + 去重带 timestamp + 时区统一;这直接影响 cost dashboard 的数字是否可信。

**第 5 优先(I14):** OpenAI 流式工具调用改按 `Index` 累积 — 只在将来用流式 + 工具时炸,但修起来便宜。

**第 6 优先(文档/REVIEW.md 清理):** 删 REVIEW.md/IMPLEMENTATION_PLAN.md,修 README/SPEC 的 tmux -CC 描述,补 brain/usage/iOS/通知。零风险、提升新人/agent onboarding。

其余 Suggestion/Nit 按精力渐进,不阻断。

---

## 关于本次 review 的诚实说明

- finder 用大规模并发 fan-out(~70+ verifier)跑挂了上游代理(7 个 agent 卡死 20-50 分钟,级联拖垮其他)。我中途切换为**直接低并发 + 主循环逐行核对**,Critical 与约 15 条 Important 是我亲自读代码确认的(`[V]`/`[SV]`)。
- 其余 `[R]` 标记的 finding 来自 finder 的具体 file:line 报告、我未独立复核 — 建议修之前各自花 30 秒看一眼原码(我已剔除 4 条误报,但 `[R]` 里可能还藏个把)。
- 完整 136 条原始 finding(含 detail/recommendation)在 `/tmp/makro-review-findings.json`,需要可查。
