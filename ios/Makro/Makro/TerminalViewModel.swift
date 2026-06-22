import Foundation

@MainActor
final class TerminalViewModel: NSObject, ObservableObject {

    @Published private(set) var isConnected = false
    @Published private(set) var content = ""
    @Published private(set) var isRefreshing = false
    @Published private(set) var lastError: String?

    private var task: URLSessionWebSocketTask?
    private var urlSession: URLSession?
    private var config: Config
    private var generation: UInt64 = 0
    private var sessionName: String = ""

    // Reconnect + heartbeat state.
    private var pingTimer: Timer?
    private var reconnectDelay: TimeInterval = 1
    private var reconnectTask: Task<Void, Never>?
    private var didExplicitlyDisconnect = false
    private var reconnectObserver: NSObjectProtocol?

    /// HTTP session that shares the app's TLS trust delegate (TOFU cert
    /// pinning). refresh()/send() must NOT use URLSession.shared — that
    /// bypasses handleTLSChallenge and would either reject the self-signed
    /// server cert (once ATS is re-enabled) or send the bearer password over
    /// an unpinned channel.
    private lazy var httpSession: URLSession = {
        URLSession(configuration: .default, delegate: self, delegateQueue: nil)
    }()

    init(config: Config = .shared) {
        self.config = config
        super.init()
        reconnectObserver = NotificationCenter.default.addObserver(
            forName: .makroReconnect,
            object: nil,
            queue: .main
        ) { [weak self] _ in
            Task { @MainActor [weak self] in self?.reconnectIfNeeded() }
        }
    }

    deinit {
        if let observer = reconnectObserver {
            NotificationCenter.default.removeObserver(observer)
        }
        pingTimer?.invalidate()
    }

    func connect(sessionName: String) {
        guard task == nil else { return }
        self.sessionName = sessionName
        didExplicitlyDisconnect = false
        generation &+= 1
        let gen = generation
        let url = config.snapshotWSURL(for: sessionName)
        urlSession = URLSession(configuration: .default, delegate: self, delegateQueue: nil)
        let wsTask = urlSession!.webSocketTask(with: url)
        self.task = wsTask
        wsTask.resume()
        scheduleReceive(gen: gen)
        startPing()
        // Pull an immediate snapshot so the user sees content right away,
        // before the WS frame timing kicks in.
        Task { await refresh() }
    }

    /// Force-pull the current pane content via HTTP GET /capture. Used as the
    /// initial fill on connect, the manual refresh button, and the recovery
    /// path when the WS has gone silent.
    func refresh() async {
        guard !sessionName.isEmpty else { return }
        isRefreshing = true
        defer { isRefreshing = false }
        var request = URLRequest(url: config.sessionCaptureURL(for: sessionName))
        request.setValue(config.bearerToken, forHTTPHeaderField: "Authorization")
        do {
            let (data, response) = try await httpSession.data(for: request)
            guard let http = response as? HTTPURLResponse, http.statusCode == 200 else {
                lastError = "capture failed"
                return
            }
            if let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
               let content = json["content"] as? String {
                self.content = content
                lastError = nil
            }
        } catch {
            lastError = error.localizedDescription
        }
    }

    /// Send text to the tmux session via HTTP POST /api/sessions/<name>/send.
    /// The server uses `tmux send-keys`, so it handles raw-mode CR semantics
    /// correctly — no need to convert \n to \r client-side.
    func send(text: String) {
        guard !sessionName.isEmpty else { return }
        var request = URLRequest(url: config.sessionSendURL(for: sessionName))
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.setValue(config.bearerToken, forHTTPHeaderField: "Authorization")
        let body = try? JSONSerialization.data(withJSONObject: ["text": text])
        request.httpBody = body
        httpSession.dataTask(with: request).resume()
    }

    /// No-op in snapshot mode. The server controls pane size via tmux config
    /// (window-size: latest). Kept for API compatibility with the caller.
    func sendResize(cols: Int, rows: Int) { _ = (cols, rows) }

    func disconnect() {
        didExplicitlyDisconnect = true
        reconnectTask?.cancel()
        reconnectTask = nil
        stopPing()
        generation &+= 1
        task?.cancel(with: .normalClosure, reason: nil)
        task = nil
        urlSession = nil
        isConnected = false
    }

    func reconnect(sessionName: String) {
        disconnect()
        didExplicitlyDisconnect = false
        connect(sessionName: sessionName)
    }

    /// Called when the app returns to foreground. iOS often kills background
    /// WebSockets silently, so force a fresh connection to pick up missed
    /// snapshots. Pattern borrowed from CarAgent's WebSocketClient.
    func reconnectIfNeeded() {
        guard !sessionName.isEmpty, task != nil else { return }
        stopPing()
        generation &+= 1
        task?.cancel(with: .normalClosure, reason: nil)
        task = nil
        urlSession = nil
        isConnected = false
        let name = sessionName
        connect(sessionName: name)
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
                    self.handleDisconnect()
                }
            }
        }
    }

    /// Snapshot-mode handler: server sends JSON frames
    /// {type: "snapshot", content: "...", ts: ...}. Replace content directly —
    /// no client-side terminal state machine, so cursor residue cannot occur.
    private func handleMessage(_ message: URLSessionWebSocketTask.Message) {
        isConnected = true
        reconnectDelay = 1
        let raw: String
        switch message {
        case .data(let data):
            raw = String(data: data, encoding: .utf8) ?? ""
        case .string(let text):
            raw = text
        @unknown default:
            return
        }

        guard let data = raw.data(using: .utf8),
              let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let type = json["type"] as? String else { return }

        switch type {
        case "snapshot":
            if let content = json["content"] as? String {
                self.content = content
            }
        case "error":
            // Server couldn't capture-pane (session died?) — keep last view.
            isConnected = false
        default:
            break
        }
    }

    /// Tear down current connection and schedule an exponential-backoff
    /// reconnect. Skipped when the user explicitly disconnected.
    private func handleDisconnect() {
        stopPing()
        generation &+= 1
        task?.cancel(with: .normalClosure, reason: nil)
        task = nil
        urlSession = nil
        isConnected = false
        guard !didExplicitlyDisconnect, !sessionName.isEmpty else { return }
        scheduleReconnect()
    }

    private func scheduleReconnect() {
        let delay = reconnectDelay
        reconnectDelay = min(reconnectDelay * 2, 60)
        let name = sessionName
        reconnectTask = Task { [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(delay * 1_000_000_000))
            guard !Task.isCancelled else { return }
            await MainActor.run {
                guard let self, !self.didExplicitlyDisconnect else { return }
                self.connect(sessionName: name)
            }
        }
    }

    // MARK: - Heartbeat

    private func startPing() {
        stopPing()
        pingTimer = Timer.scheduledTimer(withTimeInterval: 30, repeats: true) { [weak self] _ in
            Task { @MainActor [weak self] in
                self?.task?.sendPing { [weak self] err in
                    Task { @MainActor [weak self] in
                        if err != nil {
                            self?.handleDisconnect()
                        }
                    }
                }
            }
        }
    }

    private func stopPing() {
        pingTimer?.invalidate()
        pingTimer = nil
    }
}

extension TerminalViewModel: URLSessionWebSocketDelegate {
    nonisolated func urlSession(_ session: URLSession, webSocketTask: URLSessionWebSocketTask, didOpenWithProtocol protocol: String?) {
        Task { @MainActor [weak self] in
            guard let self, self.task === webSocketTask else { return }
            self.isConnected = true
            self.reconnectDelay = 1
        }
    }

    nonisolated func urlSession(_ session: URLSession, webSocketTask: URLSessionWebSocketTask, didCloseWith closeCode: URLSessionWebSocketTask.CloseCode, reason: Data?) {
        Task { @MainActor [weak self] in
            guard let self, self.task === webSocketTask else { return }
            self.handleDisconnect()
        }
    }

    nonisolated func urlSession(_ session: URLSession, didReceive challenge: URLAuthenticationChallenge, completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        Config.handleTLSChallenge(challenge, completionHandler: completionHandler)
    }
}
