import SwiftUI

@main
struct MakroApp: App {
    @Environment(\.scenePhase) private var scenePhase

    @StateObject private var bonjourTrigger = LocalNetworkTrigger()

    var body: some Scene {
        WindowGroup {
            TabView {
                ChatView()
                    .tabItem {
                        Label("Chat", systemImage: "bubble.left")
                    }

                SessionsView()
                    .tabItem {
                        Label("Terminal", systemImage: "terminal")
                    }

                KanbanView()
                    .tabItem {
                        Label("Tasks", systemImage: "checklist")
                    }
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
}
