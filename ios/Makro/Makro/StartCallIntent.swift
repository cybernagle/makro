import AppIntents
import Foundation
import SwiftUI

// MARK: - Shared router

/// Single source of truth for Siri / Shortcuts call control.
///
/// The intents run out-of-process and set these flags before the app UI is
/// necessarily in the right state. `@Published` flags on a singleton survive
/// cold-launch ordering better than a `NotificationCenter` post (which can
/// fire before the target view exists and be lost).
///
/// `@MainActor` guarantees the `@Published` mutators (`pendingStart` /
/// `pendingEnd`) are always serialized on the main run loop, regardless of
/// where the caller runs. Today every caller is already main (intent
/// `perform` is `@MainActor`, SwiftUI view callbacks are main), but the
/// annotation prevents a future off-main caller from racing the mutation.
@MainActor
final class CallRouter: ObservableObject {
    static let shared = CallRouter()

    /// Set by `StartCallIntent`; cleared by `ChatView` once it has presented.
    @Published var pendingStart = false
    /// Set by `EndCallIntent`; cleared by `CallView` once it has dismissed.
    @Published var pendingEnd = false

    func requestStart() { pendingStart = true }

    /// Set the view-level flag AND post a model-level notification. The view
    /// flag drives `CallView` dismissal; the notification lets `ChatViewModel`
    /// call `endCall()` directly, so hang-up still works if `CallView` is not
    /// presenting or its `.onReceive` is suspended (app backgrounded, etc.).
    func requestEnd() {
        pendingEnd = true
        NotificationCenter.default.post(name: .makroEndCall, object: nil)
    }
}

// MARK: - "Start call" intent

/// "Hey Siri, call Makro" → foregrounds the app, switches to the Chat tab,
/// and presents the full-screen `CallView`. The existing Azure STT/TTS + VAD
/// pipeline takes over from there.
struct StartCallIntent: AppIntent {
    static var title: LocalizedStringResource = "Start Call"
    static var description = IntentDescription("Start a voice call with Makro.")

    /// A voice call needs the mic, TTS audio, and the call UI — bring the app
    /// to the foreground rather than running in the background.
    static var openAppWhenRun: Bool { true }

    @MainActor
    func perform() async throws -> some IntentResult {
        CallRouter.shared.requestStart()
        return .result()
    }
}

// MARK: - "End call" intent (hang up)

/// "Hey Siri, hang up Makro" → dismisses `CallView`, whose `onDisappear`
/// stops STT/TTS via `vm.endCall()`. Runs in place (no forced foreground) so
/// it works from the lock screen while a call is active in the background.
struct EndCallIntent: AppIntent {
    static var title: LocalizedStringResource = "End Call"
    static var description = IntentDescription("End the active Makro voice call.")

    /// End in place — don't yank the app to the foreground.
    static var openAppWhenRun: Bool { false }

    @MainActor
    func perform() async throws -> some IntentResult {
        CallRouter.shared.requestEnd()
        return .result()
    }
}

// MARK: - Siri / Shortcuts registration

struct MakroShortcuts: AppShortcutsProvider {
    static var appShortcuts: [AppShortcut] {
        // Start call — English + Chinese phrases.
        AppShortcut(
            intent: StartCallIntent(),
            phrases: [
                // Avoid bare "Call <app>" — it collides with Siri's built-in
                // phone/contact/business-call intent and opens Maps instead.
                "Start a \(.applicationName) call",
                "Start \(.applicationName) call",
                "Start a call with \(.applicationName)",
                // 中文短语 — CAVEAT: the app's dev region is `en` and there
                // is no zh-Hans string catalog yet, so these phrases train
                // under the `en` SSU corpus (not `zh`). They are NOT
                // voice-matchable on 中文 Siri — they only surface in the
                // Shortcuts app as runnable phrases. Add a zh-Hans
                // Localizable.strings to make them voice-trained.
                // 避开"打给"（系统拨号）。
                "用 \(.applicationName) 开始通话",
                "开始 \(.applicationName) 通话",
                "\(.applicationName) 通话"
            ],
            shortTitle: "Start Call",
            systemImageName: "phone.fill"
        )

        // End call (hang up) — English + Chinese phrases.
        AppShortcut(
            intent: EndCallIntent(),
            phrases: [
                // Avoid bare "Hang up" — it's a system telephony command.
                "End \(.applicationName) call",
                "End the \(.applicationName) call",
                "Stop \(.applicationName) call",
                // 中文短语 — same caveat as the start-call zh phrases: they
                // train under `en` (no zh-Hans catalog yet), so 中文 Siri
                // won't voice-match them; Shortcuts-app-visible only until a
                // zh-Hans Localizable.strings is added.
                // 避开"挂断"（系统挂断词）。
                "结束 \(.applicationName) 通话",
                "停止 \(.applicationName) 通话"
            ],
            shortTitle: "End Call",
            systemImageName: "phone.down.fill"
        )
    }
}
