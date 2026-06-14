import Foundation

@MainActor
final class SessionsViewModel: ObservableObject {
    @Published private(set) var sessions: [Session] = []
    @Published private(set) var isLoading = false

    private let api = APIClient.shared
    // nonisolated so deinit (also nonisolated) can release it without a
    // concurrency violation under strict concurrency.
    nonisolated(unsafe) private var stateObserver: NSObjectProtocol?

    init() {
        // Real-time working/unread updates arrive as WS events routed through
        // NotificationCenter (see ChatViewModel); apply them between polls.
        stateObserver = NotificationCenter.default.addObserver(
            forName: .sessionStateChanged, object: nil, queue: .main
        ) { [weak self] note in
            guard let self,
                  let info = note.userInfo,
                  let name = info["session"] as? String else { return }
            let working = info["working"] as? Bool ?? false
            let unread = info["unread"] as? Int ?? 0
            Task { @MainActor in
                guard let idx = self.sessions.firstIndex(where: { $0.name == name }) else { return }
                self.sessions[idx].working = working
                self.sessions[idx].unread = unread
            }
        }
    }

    deinit {
        if let stateObserver { NotificationCenter.default.removeObserver(stateObserver) }
    }

    func refreshSessions() async {
        isLoading = true
        defer { isLoading = false }
        do {
            sessions = try await api.fetchSessions()
        } catch {}
    }

    func deleteSession(_ name: String) async {
        do {
            try await api.deleteSession(name: name)
            sessions.removeAll { $0.name == name }
        } catch {}
    }
}
