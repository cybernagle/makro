import Foundation

@MainActor
final class APIClient: NSObject {
    static let shared = APIClient()
    private let config = Config.shared

    private lazy var urlSession: URLSession = {
        URLSession(configuration: .default, delegate: self, delegateQueue: nil)
    }()

    private func authedRequest(url: URL) -> URLRequest {
        var request = URLRequest(url: url)
        if !config.password.isEmpty {
            request.setValue("Bearer \(config.password)", forHTTPHeaderField: "Authorization")
        }
        return request
    }

    func fetchSessions() async throws -> [Session] {
        let url = config.httpBaseURL.appendingPathComponent("api/sessions")
        var request = authedRequest(url: url)
        let (data, response) = try await urlSession.data(for: request)
        try checkAuth(response)
        return try JSONDecoder().decode([Session].self, from: data)
    }

    func createSession(name: String, workingDir: String? = nil) async throws {
        let url = config.httpBaseURL.appendingPathComponent("api/sessions")
        var request = authedRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        var body: [String: String] = ["name": name]
        if let dir = workingDir { body["working_dir"] = dir }
        request.httpBody = try JSONSerialization.data(withJSONObject: body)
        let (_, response) = try await urlSession.data(for: request)
        try checkAuth(response)
    }

    func deleteSession(name: String) async throws {
        let encoded = name.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? name
        let url = config.httpBaseURL.appendingPathComponent("api/sessions/\(encoded)")
        var request = authedRequest(url: url)
        request.httpMethod = "DELETE"
        let (_, response) = try await urlSession.data(for: request)
        try checkAuth(response)
    }

    func fetchTasks() async throws -> [KanbanTask] {
        let url = config.httpBaseURL.appendingPathComponent("api/tasks")
        var request = authedRequest(url: url)
        let (data, response) = try await urlSession.data(for: request)
        try checkAuth(response)
        return try JSONDecoder().decode([KanbanTask].self, from: data)
    }

    func createTask(title: String, content: String, column: String = "todo") async throws -> KanbanTask {
        let url = config.httpBaseURL.appendingPathComponent("api/tasks")
        var request = authedRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        let body: [String: String] = ["title": title, "content": content, "column": column]
        request.httpBody = try JSONSerialization.data(withJSONObject: body)
        let (data, _) = try await urlSession.data(for: request)
        return try JSONDecoder().decode(KanbanTask.self, from: data)
    }

    func updateTask(id: String, patch: [String: Any]) async throws -> KanbanTask {
        let encoded = id.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? id
        let url = config.httpBaseURL.appendingPathComponent("api/tasks/\(encoded)")
        var request = authedRequest(url: url)
        request.httpMethod = "PUT"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try JSONSerialization.data(withJSONObject: patch)
        let (data, _) = try await urlSession.data(for: request)
        return try JSONDecoder().decode(KanbanTask.self, from: data)
    }

    func deleteTask(id: String) async throws {
        let encoded = id.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? id
        let url = config.httpBaseURL.appendingPathComponent("api/tasks/\(encoded)")
        var request = authedRequest(url: url)
        request.httpMethod = "DELETE"
        let (_, response) = try await urlSession.data(for: request)
        try checkAuth(response)
    }

    func sendTask(id: String, session: String) async throws {
        let encoded = id.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? id
        let url = config.httpBaseURL.appendingPathComponent("api/tasks/\(encoded)/send")
        var request = authedRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try JSONSerialization.data(withJSONObject: ["session": session])
        let (_, response) = try await urlSession.data(for: request)
        try checkAuth(response)
    }

    func fetchChatHistory() async throws -> [HistoryMessage] {
        let url = config.httpBaseURL.appendingPathComponent("api/chat/history")
        var request = authedRequest(url: url)
        let (data, response) = try await urlSession.data(for: request)
        try checkAuth(response)
        return try JSONDecoder().decode([HistoryMessage].self, from: data)
    }

    func sendChat(text: String) async throws {
        let url = config.httpBaseURL.appendingPathComponent("api/chat")
        var request = authedRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try JSONSerialization.data(withJSONObject: ["text": text])
        let (_, response) = try await urlSession.data(for: request)
        guard let http = response as? HTTPURLResponse, (200...299).contains(http.statusCode) else {
            if let http = response as? HTTPURLResponse, http.statusCode == 401 {
                throw APIClientError.unauthorized
            }
            throw APIClientError.badResponse
        }
    }

    func cancelChat() async throws {
        let url = config.httpBaseURL.appendingPathComponent("api/chat/cancel")
        var request = authedRequest(url: url)
        request.httpMethod = "POST"
        _ = try await urlSession.data(for: request)
    }

    /// Registers this device's APNs push token with the makro backend so the
    /// Mac can send it push notifications when an agent finishes.
    func registerDeviceToken(deviceID: String, token: String) async throws {
        let url = config.httpBaseURL.appendingPathComponent("api/device-token")
        var request = authedRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try JSONSerialization.data(withJSONObject: [
            "device_id": deviceID,
            "token": token,
        ])
        let (_, response) = try await urlSession.data(for: request)
        try checkAuth(response)
    }

    private func checkAuth(_ response: URLResponse) throws {
        guard let http = response as? HTTPURLResponse else { return }
        if http.statusCode == 401 {
            throw APIClientError.unauthorized
        }
        guard (200...299).contains(http.statusCode) else {
            throw APIClientError.badResponse
        }
    }
}

extension APIClient: URLSessionDelegate {
    nonisolated func urlSession(_ session: URLSession, didReceive challenge: URLAuthenticationChallenge, completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        Config.handleTLSChallenge(challenge, completionHandler: completionHandler)
    }
}

struct HistoryMessage: Codable {
    let role: String
    let content: String
    let timestamp: String?
}

enum APIClientError: LocalizedError {
    case badResponse
    case unauthorized

    var errorDescription: String? {
        switch self {
        case .badResponse: return "Server error"
        case .unauthorized: return "Wrong password"
        }
    }
}
