import Foundation

class Config: ObservableObject {
    static let shared = Config()

    private enum Key {
        static let serverURL = "makro_server_url"
        static let password = "makro_password"
    }

    @Published var serverURL: String
    @Published var password: String

    private init() {
        serverURL = UserDefaults.standard.string(forKey: Key.serverURL)
            ?? "https://47.117.13.195:39222"
        password = UserDefaults.standard.string(forKey: Key.password) ?? ""
    }

    func save() {
        UserDefaults.standard.set(serverURL, forKey: Key.serverURL)
        UserDefaults.standard.set(password, forKey: Key.password)
    }

    var httpBaseURL: URL {
        let urlString = serverURL
            .replacingOccurrences(of: "wss://", with: "https://")
            .replacingOccurrences(of: "ws://", with: "http://")
        return URL(string: urlString)!
    }

    var chatWSURL: URL {
        let ws = serverURL
            .replacingOccurrences(of: "https://", with: "wss://")
            .replacingOccurrences(of: "http://", with: "ws://")
        var url = URL(string: "\(ws)/ws/chat")!
        if !password.isEmpty {
            url = URL(string: "\(url.absoluteString)?token=\(password.addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed) ?? password)")!
        }
        return url
    }

    func terminalWSURL(for sessionName: String, cols: Int = 80, rows: Int = 24) -> URL {
        let ws = serverURL
            .replacingOccurrences(of: "https://", with: "wss://")
            .replacingOccurrences(of: "http://", with: "ws://")
        let encoded = sessionName.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? sessionName
        var url = URL(string: "\(ws)/ws/xterm/\(encoded)?cols=\(cols)&rows=\(rows)")!
        if !password.isEmpty {
            url = URL(string: "\(url.absoluteString)&token=\(password.addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed) ?? password)")!
        }
        return url
    }

    /// Snapshot-mode terminal WS. Server pushes periodic tmux capture-pane
    /// output as JSON frames. Used by iOS for cursor-residue-free rendering.
    func snapshotWSURL(for sessionName: String) -> URL {
        let ws = serverURL
            .replacingOccurrences(of: "https://", with: "wss://")
            .replacingOccurrences(of: "http://", with: "ws://")
        let encoded = sessionName.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? sessionName
        var url = URL(string: "\(ws)/ws/snapshot/\(encoded)")!
        if !password.isEmpty {
            url = URL(string: "\(url.absoluteString)?token=\(password.addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed) ?? password)")!
        }
        return url
    }

    /// HTTP endpoint for sending keystrokes/text to a tmux session.
    func sessionSendURL(for sessionName: String) -> URL {
        let encoded = sessionName.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? sessionName
        return URL(string: "\(httpBaseURL.absoluteString)/api/sessions/\(encoded)/send")!
    }

    /// HTTP endpoint for one-shot pane capture (alternative to snapshot WS).
    func sessionCaptureURL(for sessionName: String) -> URL {
        let encoded = sessionName.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? sessionName
        return URL(string: "\(httpBaseURL.absoluteString)/api/sessions/\(encoded)/capture")!
    }

    /// Value for the `Authorization: Bearer` header.
    var bearerToken: String { "Bearer \(password)" }

    static func handleTLSChallenge(
        _ challenge: URLAuthenticationChallenge,
        completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void
    ) {
        if challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust,
           let serverTrust = challenge.protectionSpace.serverTrust {
            completionHandler(.useCredential, URLCredential(trust: serverTrust))
        } else {
            completionHandler(.performDefaultHandling, nil)
        }
    }
}
