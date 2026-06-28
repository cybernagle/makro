# Makro iOS — Siri 唤起通话 集成计划

> 状态：**已实现**（`xcodebuild` BUILD SUCCEEDED，零 error；真机 Siri 验证待用户做）。前置已完成：VAD + 结束语提交（commit `0add94e`，已上线 origin/main）。
> 范围：纯 iOS 客户端，**零后端改动**。

## 目标

"嘿 Siri，用 Makro 开始通话" → app 打开到 Chat 标签 → present `CallView` → 现有的 Azure STT/TTS + VAD 那套接管。

## 现状（已确认）

- 无任何 Siri / App Intents 集成（build 日志 `No AppIntents.framework dependency found` / `No AppShortcuts found`）。
- 根导航：`MakroApp`（`@main`）→ `TabView` 3 标签：Chat(0) / Sessions(1) / Kanban(2)。
- `CallView` 由 `ChatView` 内 `.fullScreenCover(isPresented: $showCall)` present（`ChatView.swift:90-92`）；电话按钮在 toolbar（`:74`）。`CallView.onAppear { vm.startCall() }`（`CallView.swift:31`）。
- 跨组件路由已有模式：`NotificationCenter`（`.makroOpenSession` → `MakroApp` 设 `selectedTab = 1`，`MakroApp.swift:25-36`）。**镜像这个模式**。
- `Info.plist` 已有 background modes：`remote-notification`、`audio`（够用，无需改）。无 URL scheme。

## 要加的东西

### 1. 新文件 `StartCallIntent.swift`（`ios/Makro/Makro/`）

```swift
import AppIntents
import Foundation

struct StartCallIntent: AppIntent {
    static var title: LocalizedStringResource = "开始通话"
    static var description = IntentDescription("用语音开始和 Makro 的通话。")
    static var openAppWhenRun: Bool { true }   // 把 app 带到前台（通话需要 mic+TTS+UI）

    @MainActor
    func perform() async throws -> some IntentResult {
        CallRouter.shared.requestStart()        // 设共享标志（见下）
        return .result()
    }
}

struct MakroShortcuts: AppShortcutsProvider {
    static var appShortcuts: [AppShortcut] {
        AppShortcut(
            intent: StartCallIntent(),
            phrases: [
                "用 \(.applicationName) 开始通话",
                "\(.applicationName) 通话",
                "start \(.applicationName) call"
            ],
            shortTitle: "开始通话",
            systemImageName: "phone.fill"
        )
    }
}
```

`\(.applicationName)` 解析为 app 显示名 "Makro"。AppIntents 是 iOS 16+ 系统 framework，无需 Pod。

### 2. 共享路由（抗冷启动时序）—— 新增小单例，放 `MakroApp.swift` 或独立文件

冷启动时 `NotificationCenter` 可能在 `ChatView` 实例化前就 post，丢消息。用 `ObservableObject` 单例更稳：

```swift
final class CallRouter: ObservableObject {
    static let shared = CallRouter()
    @Published var pendingStart = false
    func requestStart() { pendingStart = true }
}
```

`StartCallIntent.perform` 调 `CallRouter.shared.requestStart()`。

### 3. `MakroApp.swift` —— 切到 Chat 标签

```swift
.onReceive(CallRouter.shared.$pendingStart) { req in
    if req { selectedTab = 0 }     // 确保 Chat 标签激活（ChatView 实例化）
}
```

### 4. `ChatView.swift` —— 响应并 present CallView

在 `NavigationStack` 里加：
```swift
.onReceive(CallRouter.shared.$pendingStart) { req in
    guard req else { return }
    CallRouter.shared.pendingStart = false
    showCall = true                // 触发 .fullScreenCover → CallView → vm.startCall()
}
```

### 5. `project.pbxproj` —— 把 `StartCallIntent.swift` 加进 target

参照 `VoiceActivityDetector.swift` 的加法（4 处）：`PBXBuildFile` / `PBXFileReference` / `PBXGroup` / `PBXSourcesBuildPhase`，用两个新 24 位 hex UUID。

## 备选（可选，先不做）

- URL scheme `makro://call` + `Info.plist` 的 `CFBundleURLSchemes` + `onOpenURL`：作为非 Siri 的"捷径打开"兜底入口。AppShortcut 是主路径，先不加。

## 注意点

- `openAppWhenRun = true` → 带前台。锁屏用 Siri 唤起通常需先解锁（看用户的 Siri 锁屏设置）；唤起后通话有 `.audio` 后台模式 + 锁屏 Now Playing 控制，能继续。
- 装新 build 后 Siri 自动发现 AppShortcut（iOS 16+ 无需显式 donate）。
- "Makro" 这个词 Siri 可能听不准 → 用户可在 设置→Siri 改短语兜底。
- `perform` 标 `@MainActor`，避免跨 actor 警告。

## 验证（真机）

1. `xcodebuild` 通过（AppIntents 编译 + 注册无错）。
2. 装上后 设置 → Siri 与搜索 里能看到 Makro 的"开始通话"快捷指令。
3. 喊 "嘿 Siri，用 Makro 开始通话" → app 打开 → Chat 标签 → `CallView` 全屏 → 通话开始（VAD 聆听，相位"正在聆听… 说『请发送』结束"）。
4. Shortcuts app 里也能看到并手动跑。

## 实现顺序

1. `StartCallIntent.swift`（intent + provider）。
2. `CallRouter` 单例。
3. `MakroApp` / `ChatView` 各加一个 `onReceive`。
4. `project.pbxproj` 加文件。
5. build + 真机验证。

## 实现记录（2026-06-21）

已实现，`xcodebuild -workspace Makro.xcworkspace -scheme Makro -destination 'generic/platform=iOS Simulator' CODE_SIGNING_ALLOWED=NO build` → **BUILD SUCCEEDED**（零 error）。与上面计划的差异：

- **`AppShortcut` 用 `shortTitle:`，非 `shortCutTitle:`**（iOS 26 SDK 改名；上面代码块里那行已更正为 `shortTitle`，写 `shortCutTitle` 会编译报 "Incorrect argument label"）。
- **新文件入 target 走 `xcodegen generate` + `pod install`，不是手改 pbxproj**。`xcodegen generate` 会删除共享 scheme——需从备份恢复（见 CLAUDE.md 的 build-was-grey trap）。**`pod install` 必须在 `xcodegen generate` 之后跑**，否则 `MicrosoftCognitiveServicesSpeech` 模块无法 resolve（xcodegen 重新生成的 project 丢了 Pods 注入的 build settings）。
- **`ChatView` 额外在 `.task` 里查 `CallRouter.shared.pendingStart` 当前值**：`.onReceive` 只响应订阅后的变化，冷启动（Siri 在 `ChatView` 实例化前就设了标志）会漏掉。
