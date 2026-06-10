import Foundation

@MainActor
final class SessionsViewModel: ObservableObject {
    @Published private(set) var sessions: [Session] = []
    @Published private(set) var isLoading = false

    private let api = APIClient.shared

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
