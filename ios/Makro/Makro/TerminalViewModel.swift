import Foundation

@MainActor
final class TerminalViewModel: NSObject, ObservableObject {

    @Published private(set) var isConnected = false
    @Published private(set) var content = ""

    private var task: URLSessionWebSocketTask?
    private var urlSession: URLSession?
    private var config: Config
    private var generation: UInt64 = 0

    init(config: Config = .shared) {
        self.config = config
        super.init()
    }

    func connect(sessionName: String) {
        guard task == nil else { return }
        generation &+= 1
        let gen = generation
        let url = config.terminalWSURL(for: sessionName)
        urlSession = URLSession(configuration: .default, delegate: self, delegateQueue: nil)
        let wsTask = urlSession!.webSocketTask(with: url)
        self.task = wsTask
        wsTask.resume()
        scheduleReceive(gen: gen)
    }

    func send(text: String) {
        guard let task, let data = text.data(using: .utf8) else { return }
        task.send(.data(data)) { _ in }
    }

    func sendResize(cols: Int, rows: Int) {
        guard let task else { return }
        let msg = try? JSONSerialization.data(withJSONObject: ["type": "resize", "cols": cols, "rows": rows])
        guard let msg else { return }
        task.send(.string(String(data: msg, encoding: .utf8) ?? "")) { _ in }
    }

    func disconnect() {
        generation &+= 1
        task?.cancel(with: .normalClosure, reason: nil)
        task = nil
        urlSession = nil
        isConnected = false
    }

    func reconnect(sessionName: String) {
        disconnect()
        connect(sessionName: sessionName)
    }

    private func scheduleReceive(gen: UInt64) {
        task?.receive { [weak self] result in
            Task { @MainActor [weak self] in
                guard let self, self.generation == gen else { return }
                switch result {
                case .success(let message):
                    self.handleMessage(message)
                    self.scheduleReceive(gen: gen)
                case .failure:
                    self.disconnect()
                }
            }
        }
    }

    private func handleMessage(_ message: URLSessionWebSocketTask.Message) {
        isConnected = true
        var chunk: String
        switch message {
        case .data(let data):
            chunk = String(data: data, encoding: .utf8) ?? ""
        case .string(let text):
            chunk = text
        @unknown default:
            return
        }

        var merged = content
        for char in chunk {
            if char == "\r" {
                if let nl = merged.lastIndex(of: "\n") {
                    merged = String(merged[...nl])
                } else {
                    merged = ""
                }
            } else {
                merged.append(char)
            }
        }

        if merged.count > 20_000 {
            merged = String(merged.suffix(20_000))
        }
        content = merged
    }
}

extension TerminalViewModel: URLSessionWebSocketDelegate {
    nonisolated func urlSession(_ session: URLSession, webSocketTask: URLSessionWebSocketTask, didOpenWithProtocol protocol: String?) {
        Task { @MainActor [weak self] in
            guard let self, self.task === webSocketTask else { return }
            self.isConnected = true
        }
    }

    nonisolated func urlSession(_ session: URLSession, webSocketTask: URLSessionWebSocketTask, didCloseWith closeCode: URLSessionWebSocketTask.CloseCode, reason: Data?) {
        Task { @MainActor [weak self] in
            guard let self, self.task === webSocketTask else { return }
            self.task = nil
            self.urlSession = nil
            self.isConnected = false
        }
    }

    nonisolated func urlSession(_ session: URLSession, didReceive challenge: URLAuthenticationChallenge, completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        Config.handleTLSChallenge(challenge, completionHandler: completionHandler)
    }
}
