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

    // Call mode (phone-call style): continuous listening + auto TTS loop.
    @Published var isInCall = false
    @Published var isMuted = false

    // Voice-call phase (discuss → proposed → dispatched). pendingPlan is
    // non-nil while a plan is awaiting the user's confirmation; CallView shows
    // confirm/deny buttons while set.
    @Published private(set) var pendingPlan: PendingPlan?
    @Published private(set) var callPhase: String = "discuss"
    // Voice-call interaction mode (闲聊/落实/查询), picked from the CallView
    // dropdown. Persisted across launches; default 落实 (plan).
    @Published var callMode: CallMode = {
        let raw = UserDefaults.standard.string(forKey: "makro.callMode") ?? CallMode.plan.rawValue
        return CallMode(rawValue: raw) ?? .plan
    }() {
        didSet { UserDefaults.standard.set(callMode.rawValue, forKey: "makro.callMode") }
    }

    private var cancellables: Set<AnyCancellable> = []

    private var task: URLSessionWebSocketTask?
    private var urlSession: URLSession?
    private var pingTimer: Timer?
    private var reconnectTask: Task<Void, Never>?
    private var reconnectDelay: TimeInterval = 1
    private var streamingWatchdog: Task<Void, Never>?
    private let config: Config
    private let api: APIClient
    private let speech: AzureSpeechManager

    init(config: Config = .shared, api: APIClient = .shared) {
        self.config = config
        self.api = api
        self.speech = AzureSpeechManager(config: config)
        super.init()
        wireSpeech()
        // Model-level hang-up: `EndCallIntent` posts `.makroEndCall` so the
        // call stops (STT/TTS/audio torn down) even when `CallView` isn't
        // presenting or its `.onReceive` is suspended. The subscription is
        // stored in `cancellables`, which is released on deinit → cleanup is
        // symmetric without an explicit removeObserver.
        NotificationCenter.default.publisher(for: .makroEndCall)
            .sink { [weak self] _ in
                Task { @MainActor in self?.endCall() }
            }
            .store(in: &cancellables)
    }

    private func wireSpeech() {
        // STT result → send as a normal chat message (reuses existing path)
        // and arm the one-shot flag so the reply gets read aloud.
        speech.onRecognized = { [weak self] text in
            guard let self else { return }
            self.partialTranscript = nil
            self.isListening = false
            self.expectSpokenReply = true
            // Voice turns tag the message so the server uses a spoken-friendly
            // prompt (conversational, no tables/code). isInCall gates this so
            // a one-shot voice send outside a call stays unstyled.
            self.send(text: text, voice: self.isInCall)
        }
        // First partial of a new utterance → if the assistant is still
        // speaking, stop it. This is the interruption path: the user talks
        // over the TTS and we cut it off. .voiceChat echo cancellation keeps
        // the assistant's own audio from falsely triggering this.
        speech.onPartialSpeech = { [weak self] in
            guard let self else { return }
            if self.isSpeaking {
                self.stopSpeaking()
            }
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
        // We do NOT suspend the mic while speaking — instead we keep listening
        // so the user can interrupt the assistant. The .voiceChat session mode
        // provides system echo cancellation to suppress the TTS leaking back
        // in. When real user speech is detected mid-playback, the recognizing
        // handler stops TTS (see onPartialSpeech below).
        speech.$speakState.sink { [weak self] state in
            guard let self else { return }
            self.isSpeaking = (state == .speaking)
            if state != .speaking, !self.isInCall {
                self.expectSpokenReply = false
            }
        }.store(in: &cancellables)
        // Quota wall → tell the user and stop active listening/speaking.
        speech.onQuotaExhausted = { [weak self] msg in
            guard let self else { return }
            self.messages.append(ChatMessage(role: .system, text: msg))
            self.expectSpokenReply = false
            self.isInCall = false
            self.stopListening()
            self.stopSpeaking()
        }
        // Commit-mode nudge when the user has spoken a long time without a
        // commit phrase. Surfaces on the lock-screen Now Playing card.
        speech.onMaxDurationHint = {
            NowPlayingManager.shared.updatePhase("说话有点久 — 说『请发送』结束")
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

    deinit {
        // Belt-and-suspenders cleanup. onDisappear calls disconnect(), but a
        // stuck URLSession delegate or an in-flight reconnect sleep could
        // otherwise extend this VM's lifetime. Stored-property access only —
        // safe in a nonisolated deinit.
        streamingWatchdog?.cancel()
        reconnectTask?.cancel()
        pingTimer?.invalidate()
        task?.cancel(with: .goingAway, reason: nil)
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

    /// Send a chat message. `voice` flags the message as coming from a voice
    /// call so the server uses a spoken-friendly prompt (conversational, no
    /// tables/code). STT turns set voice = isInCall; typed messages omit it.
    func send(text: String, voice: Bool = false) {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }
        // Reentry guard: if a previous turn's `done` was lost (WS dropout,
        // parse miss), isStreaming would still be true. Close it out before
        // opening a new turn so the indicator can never get stuck.
        if isStreaming { endStreaming() }
        messages.append(ChatMessage(role: .user, text: trimmed))
        isStreaming = true
        startStreamingWatchdog()
        Task {
            do {
                try await api.sendChat(text: trimmed, voice: voice)
            } catch {
                messages.append(ChatMessage(role: .system, text: "[error: \(error.localizedDescription)]"))
                endStreaming()
            }
        }
    }

    /// Central reset point. Every path that ends a turn goes through here so
    /// the watchdog is always cancelled and isStreaming always flips together.
    private func endStreaming() {
        streamingWatchdog?.cancel()
        streamingWatchdog = nil
        isStreaming = false
    }

    /// Failsafe: if no `done`/`error` arrives within 60s (WS dropped the
    /// broadcast, or a server path skipped it), force the turn closed so the
    /// indicator never sticks. 60s is well past normal agent turn latency.
    private func startStreamingWatchdog() {
        streamingWatchdog?.cancel()
        streamingWatchdog = Task { [weak self] in
            try? await Task.sleep(nanoseconds: 60 * 1_000_000_000)
            guard !Task.isCancelled else { return }
            await MainActor.run {
                guard let self, self.isStreaming else { return }
                self.messages.append(ChatMessage(role: .system, text: "[响应超时,已自动结束]"))
                self.endStreaming()
            }
        }
    }

    func cancel() {
        // User-initiated stop: don't wait for the server's done (that may be
        // exactly what's stuck). Reset locally right away.
        endStreaming()
        Task { try? await api.cancelChat() }
    }

    /// Confirm the staged voice-call plan → server dispatches it to its session.
    func confirmPlan() {
        guard pendingPlan != nil else { return }
        pendingPlan = nil
        Task { try? await api.confirmPlan() }
    }

    /// Deny the staged plan → server returns to discussion.
    func denyPlan() {
        pendingPlan = nil
        Task { try? await api.denyPlan() }
    }

    /// Switch the voice-call interaction mode (from the CallView dropdown).
    /// Resets any pending plan and tells the backend to apply the new mode's
    /// behavior (prefix + tool gate + plan staging).
    func setCallMode(_ mode: CallMode) {
        callMode = mode
        pendingPlan = nil
        callPhase = "discuss"
        if isInCall {
            Task { await api.setCallActive(true, mode: mode) }
        }
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
        // Stopping playback also cancels any pending spoken reply (outside
        // of an active call; in a call, keep the loop armed).
        if !isInCall {
            expectSpokenReply = false
        }
    }

    // MARK: - Call mode (phone-call style)

    /// Start a continuous voice call: the mic stays open, every recognized
    /// utterance is sent, and every reply is read aloud (with the mic briefly
    /// suspended during playback to avoid echo).
    func startCall() {
        guard speech.isConfigured else {
            messages.append(ChatMessage(role: .system, text: "请先在设置里填写 Azure Speech key 和 region"))
            return
        }
        isInCall = true
        isMuted = false
        // Reset voice-call phase state and tell the backend a call started
        // (it clears any staged plan + flips on the dispatch gate).
        pendingPlan = nil
        callPhase = "discuss"
        Task { await api.setCallActive(true, mode: callMode) }
        // Arm spoken replies for the whole call; the done→TTS path checks isInCall.
        stopSpeaking()
        // Commit mode (VAD-gated push stream) is gated by the VAD setting; when
        // off, call mode falls back to the legacy always-on recognizer.
        speech.startListening(continuous: true, commit: config.vadEnabled)
        // Wire lock-screen controls.
        NowPlayingManager.shared.onHangUp = { [weak self] in
            Task { @MainActor in self?.endCall() }
        }
        NowPlayingManager.shared.onToggleMute = { [weak self] in
            Task { @MainActor in self?.toggleMute() }
        }
        NowPlayingManager.shared.startCall()
    }

    /// End the call: stop the mic and any playback.
    func endCall() {
        isInCall = false
        isMuted = false
        expectSpokenReply = false
        pendingPlan = nil
        callPhase = "discuss"
        Task { await api.setCallActive(false, mode: callMode) }
        NowPlayingManager.shared.endCall()
        // Cancel any pending post-Siri auto-resume so ending a call (button or
        // Siri "hang up") isn't immediately undone when the audio interruption
        // from Siri itself ends.
        speech.suppressAudioResume()
        stopListening()
        stopSpeaking()
    }

    /// Mute/unmute the mic during a call (mapped to the lock-screen play/pause button).
    func toggleMute() {
        guard isInCall else { return }
        isMuted.toggle()
        if isMuted {
            speech.suspendListening()
            NowPlayingManager.shared.updatePhase("已静音")
        } else {
            speech.resumeListening()
            NowPlayingManager.shared.updatePhase("正在聆听…")
        }
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
            endStreaming()
            // Read aloud when this turn was triggered by voice, or whenever we
            // are in an active call (every reply is spoken in call mode).
            // Typed messages outside a call never set either flag → silent.
            if (expectSpokenReply || isInCall),
               let last = messages.last,
               last.role == .assistant {
                // Strip the ```plan block (if any) so TTS reads only the
                // conversational summary, not the raw JSON execution target.
                let spoken = stripPlanBlocks(last.text)
                if !spoken.isEmpty { speech.speak(spoken) }
            }
        case "error":
            let msg = json["data"] as? String ?? "Unknown error"
            messages.append(ChatMessage(role: .system, text: "[error: \(msg)]"))
            endStreaming()
        case "system":
            let msg = json["data"] as? String ?? ""
            messages.append(ChatMessage(role: .system, text: msg))
        case "plan":
            // Assistant proposed a plan during a voice call → surface the
            // confirm affordance. The prose summary is already in the last
            // assistant message; this carries the structured execution target.
            if let dataStr = json["data"] as? String,
               let d = dataStr.data(using: .utf8) {
                pendingPlan = try? JSONDecoder().decode(PendingPlan.self, from: d)
            }
        case "phase":
            let p = json["data"] as? String ?? "discuss"
            callPhase = p
            // Leaving "proposed" (confirm / deny / dispatch) clears the card.
            if p != "proposed" { pendingPlan = nil }
        case "dispatched":
            pendingPlan = nil
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
        // WS dropped mid-turn: the backend's `done` broadcast has no buffer
        // and no replay, so it's already lost. Close the turn now rather than
        // leaving the indicator pinned until the watchdog times out.
        if isStreaming {
            messages.append(ChatMessage(role: .system, text: "[连接中断]"))
            endStreaming()
        }
        scheduleReconnect()
    }

    private func scheduleReconnect() {
        let delay = reconnectDelay
        reconnectDelay = min(reconnectDelay * 2, 60)
        reconnectTask = Task { [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(delay * 1_000_000_000))
            guard !Task.isCancelled else { return }
            guard let self else { return }
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

// stripPlanBlocks removes ```plan fenced blocks from assistant text so TTS and
// the call transcript surface only the conversational summary, not the raw JSON
// execution target (which is delivered separately via the `plan` WS event).
private func stripPlanBlocks(_ text: String) -> String {
    guard let re = try? NSRegularExpression(pattern: "```plan[\\s\\S]*?```\\s*", options: []) else {
        return text
    }
    let range = NSRange(text.startIndex..., in: text)
    return re.stringByReplacingMatches(in: text, options: [], range: range, withTemplate: "")
}
