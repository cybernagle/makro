import Foundation
import Combine

@MainActor
final class ChatViewModel: NSObject, ObservableObject {

    @Published private(set) var messages: [ChatMessage] = []
    @Published private(set) var connectionState: ConnectionState = .disconnected
    @Published private(set) var isStreaming = false
    @Published private(set) var thinkingText: String?

    // Voice conversation state.
    // expectSpokenReply is a one-shot flag: set true only when the user's
    // message was produced by speech recognition, and cleared right after the
    // reply is spoken (or interrupted). This keeps typed messages silent.
    @Published private(set) var partialTranscript: String?
    @Published private(set) var isListening = false
    @Published private(set) var isSpeaking = false
    private var expectSpokenReply = false

    private var cancellables: Set<AnyCancellable> = []

    private var task: URLSessionWebSocketTask?
    private var urlSession: URLSession?
    private var pingTimer: Timer?
    private var reconnectTask: Task<Void, Never>?
    private var reconnectDelay: TimeInterval = 1
    private let config: Config
    private let api: APIClient
    private let speech: AzureSpeechManager

    init(config: Config = .shared, api: APIClient = .shared) {
        self.config = config
        self.api = api
        self.speech = AzureSpeechManager(config: config)
        super.init()
        wireSpeech()
    }

    private func wireSpeech() {
        // STT result → send as a normal chat message (reuses existing path)
        // and arm the one-shot flag so the reply gets read aloud.
        speech.onRecognized = { [weak self] text in
            guard let self else { return }
            self.partialTranscript = nil
            self.isListening = false
            self.expectSpokenReply = true
            self.send(text: text)
        }
        // Surface partial recognition so the UI can show "正在听…".
        speech.$listenState.sink { [weak self] state in
            guard let self else { return }
            switch state {
            case .listening(let partial):
                self.partialTranscript = partial
                self.isListening = true
            case .error:
                self.partialTranscript = nil
                self.isListening = false
            case .idle:
                // Cleared by onRecognized; nothing to do here.
                break
            }
        }.store(in: &cancellables)
        // Speaking state → drives the waveform animation + button affordance.
        // Clear the one-shot flag once playback ends so the NEXT message only
        // speaks if it was itself triggered by voice.
        speech.$speakState.sink { [weak self] state in
            guard let self else { return }
            self.isSpeaking = (state == .speaking)
            if state != .speaking {
                self.expectSpokenReply = false
            }
        }.store(in: &cancellables)
        // Quota wall → tell the user and stop active listening/speaking.
        speech.onQuotaExhausted = { [weak self] msg in
            guard let self else { return }
            self.messages.append(ChatMessage(role: .system, text: msg))
            self.expectSpokenReply = false
            self.stopListening()
            self.stopSpeaking()
        }
    }

    func connect() {
        guard connectionState == .disconnected else { return }
        connectionState = .connecting
        openConnection()
    }

    func disconnect() {
        reconnectTask?.cancel()
        stopPing()
        task?.cancel(with: .normalClosure, reason: nil)
        task = nil
        urlSession = nil
        connectionState = .disconnected
    }

    func reconnectIfNeeded() {
        guard connectionState == .connected else { return }
        stopPing()
        task?.cancel(with: .normalClosure, reason: nil)
        task = nil
        connectionState = .disconnected
        connect()
    }

    func loadHistory() async {
        let beforeCount = messages.count
        do {
            let history = try await api.fetchChatHistory()
            var loaded: [ChatMessage] = []
            for m in history {
                guard let role = ChatMessage.Role(rawValue: m.role) else { continue }
                loaded.append(ChatMessage(role: role, text: m.content))
            }
            let recent = beforeCount < messages.count ? Array(messages[beforeCount...]) : []
            messages = loaded + recent
        } catch {}
    }

    func send(text: String) {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }
        messages.append(ChatMessage(role: .user, text: trimmed))
        isStreaming = true
        Task {
            do {
                try await api.sendChat(text: trimmed)
            } catch {
                messages.append(ChatMessage(role: .system, text: "[error: \(error.localizedDescription)]"))
                isStreaming = false
            }
        }
    }

    func cancel() {
        Task { try? await api.cancelChat() }
    }

    // MARK: - Voice conversation

    /// Toggle mic listening. Tap to start, tap again (or trailing silence) to stop.
    func toggleListening() {
        if isListening {
            stopListening()
        } else {
            // Stop any ongoing playback before listening.
            stopSpeaking()
            speech.startListening()
        }
    }

    func stopListening() {
        speech.stopListening()
        isListening = false
        partialTranscript = nil
        // If the user stops before any recognized text fires, there's nothing
        // to reply to — make sure a stray done event won't trigger TTS.
        // (expectSpokenReply is only armed by onRecognized, so this is just
        // defensive cleanup of the case where STT was cancelled entirely.)
    }

    func stopSpeaking() {
        speech.stopSpeaking()
        isSpeaking = false
        // Stopping playback also cancels any pending spoken reply.
        expectSpokenReply = false
    }

    private func openConnection() {
        let url = config.chatWSURL
        urlSession = URLSession(configuration: .default, delegate: self, delegateQueue: nil)
        let wsTask = urlSession!.webSocketTask(with: url)
        self.task = wsTask
        wsTask.resume()
        scheduleReceive()
        startPing()
    }

    private func scheduleReceive() {
        task?.receive { [weak self] result in
            Task { @MainActor [weak self] in
                guard let self else { return }
                switch result {
                case .success(let message):
                    self.handleMessage(message)
                    self.scheduleReceive()
                case .failure:
                    self.handleDisconnect()
                }
            }
        }
    }

    private func handleMessage(_ message: URLSessionWebSocketTask.Message) {
        guard case .string(let text) = message,
              let data = text.data(using: .utf8),
              let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let type = json["type"] as? String else { return }

        switch type {
        case "ping":
            return
        case "thinking":
            let chunk = json["data"] as? String ?? ""
            if thinkingText == nil { thinkingText = "" }
            thinkingText! += chunk
        case "assistant":
            thinkingText = nil
            let chunk = json["data"] as? String ?? ""
            if !messages.isEmpty && messages.last?.role == .assistant && isStreaming {
                messages[messages.count - 1].text += chunk
            } else {
                messages.append(ChatMessage(role: .assistant, text: chunk))
            }
        case "done":
            thinkingText = nil
            isStreaming = false
            // Only read aloud replies that were triggered by voice. Typed
            // messages never set expectSpokenReply, so they stay silent.
            if expectSpokenReply,
               let last = messages.last,
               last.role == .assistant,
               !last.text.isEmpty {
                speech.speak(last.text)
            }
        case "error":
            let msg = json["data"] as? String ?? "Unknown error"
            messages.append(ChatMessage(role: .system, text: "[error: \(msg)]"))
            isStreaming = false
        case "system":
            let msg = json["data"] as? String ?? ""
            messages.append(ChatMessage(role: .system, text: msg))
        case "session_state":
            // Per-session working/unread snapshot; re-broadcast to the
            // Sessions list via NotificationCenter (it doesn't own this WS).
            if let dataStr = json["data"] as? String,
               let d = dataStr.data(using: .utf8),
               let payload = try? JSONSerialization.jsonObject(with: d) as? [String: Any],
               let session = payload["session"] as? String {
                let info: [String: Any] = [
                    "session": session,
                    "working": payload["working"] as? Bool ?? false,
                    "unread": payload["unread"] as? Int ?? 0,
                ]
                NotificationCenter.default.post(name: .sessionStateChanged, object: nil, userInfo: info)
            }
        default:
            break
        }
    }

    private func handleDisconnect() {
        guard connectionState != .disconnected else { return }
        stopPing()
        task = nil
        connectionState = .disconnected
        scheduleReconnect()
    }

    private func scheduleReconnect() {
        let delay = reconnectDelay
        reconnectDelay = min(reconnectDelay * 2, 60)
        reconnectTask = Task {
            try? await Task.sleep(nanoseconds: UInt64(delay * 1_000_000_000))
            guard !Task.isCancelled else { return }
            self.connectionState = .connecting
            self.openConnection()
        }
    }

    private func startPing() {
        stopPing()
        pingTimer = Timer.scheduledTimer(withTimeInterval: 30, repeats: true) { [weak self] _ in
            Task { @MainActor [weak self] in self?.task?.sendPing { _ in } }
        }
    }

    private func stopPing() {
        pingTimer?.invalidate()
        pingTimer = nil
    }
}

extension ChatViewModel: URLSessionWebSocketDelegate {
    nonisolated func urlSession(_ session: URLSession, webSocketTask: URLSessionWebSocketTask, didOpenWithProtocol protocol: String?) {
        Task { @MainActor in
            self.connectionState = .connected
            self.reconnectDelay = 1
        }
    }

    nonisolated func urlSession(_ session: URLSession, webSocketTask: URLSessionWebSocketTask, didCloseWith closeCode: URLSessionWebSocketTask.CloseCode, reason: Data?) {
        Task { @MainActor in
            self.stopPing()
            self.task = nil
            self.connectionState = .disconnected
            if closeCode != .normalClosure { self.scheduleReconnect() }
        }
    }

    nonisolated func urlSession(_ session: URLSession, didReceive challenge: URLAuthenticationChallenge, completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        Config.handleTLSChallenge(challenge, completionHandler: completionHandler)
    }
}
