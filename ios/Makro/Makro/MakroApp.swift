import SwiftUI

@main
struct MakroApp: App {
    @Environment(\.scenePhase) private var scenePhase
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @StateObject private var bonjourTrigger = LocalNetworkTrigger()
    @State private var selectedTab = 0

    var body: some Scene {
        WindowGroup {
            TabView(selection: $selectedTab) {
                ChatView()
                    .tabItem { Label("Chat", systemImage: "bubble.left") }
                    .tag(0)

                SessionsView()
                    .tabItem { Label("Terminal", systemImage: "terminal") }
                    .tag(1)

                KanbanView()
                    .tabItem { Label("Tasks", systemImage: "checklist") }
                    .tag(2)

                ArtifactsView()
                    .tabItem { Label("Artifacts", systemImage: "doc.richtext") }
                    .tag(3)
            }
            .onReceive(NotificationCenter.default.publisher(for: .makroOpenSession)) { note in
                // Push tap → jump to the Terminal tab. The tapped session name is
                // on the notification; SessionsView may observe it to switch.
                selectedTab = 1
                if let session = note.userInfo?["session"] as? String {
                    NotificationCenter.default.post(
                        name: .makroSelectSession,
                        object: nil,
                        userInfo: ["session": session]
                    )
                }
            }
            .onReceive(CallRouter.shared.$pendingStart) { wantsCall in
                // Siri/Shortcuts "Start a Makro call" → make sure the Chat
                // tab (which owns CallView presentation) is active before
                // ChatView reacts.
                if wantsCall { selectedTab = 0 }
            }
        }
        .onChange(of: scenePhase) { newPhase in
            if newPhase == .active {
                NotificationCenter.default.post(name: .makroReconnect, object: nil)
            }
        }
    }
}

private class LocalNetworkTrigger: NSObject, ObservableObject, NetServiceBrowserDelegate {
    private var browser: NetServiceBrowser?
    private var foundServices: [NetService] = []

    override init() {
        super.init()
        browser = NetServiceBrowser()
        browser?.delegate = self
        browser?.searchForServices(ofType: "_http._tcp.", inDomain: "local.")
    }
}

extension Notification.Name {
    static let makroReconnect = Notification.Name("makroReconnect")
    static let makroOpenSession = Notification.Name("makroOpenSession")
    static let makroSelectSession = Notification.Name("makroSelectSession")
    /// Posted by `CallRouter.requestEnd()` so `ChatViewModel` can stop STT /
    /// TTS / audio at the model layer even when `CallView` isn't presenting
    /// or its `.onReceive` is suspended (app backgrounded, etc.).
    static let makroEndCall = Notification.Name("makroEndCall")
}
