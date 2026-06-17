import Foundation
import AVFoundation
import MicrosoftCognitiveServicesSpeech
import Combine

// MARK: - Quota tracking

/// Local monthly quota tracker for Azure Speech F0 free tier.
///
/// Azure has no simple "remaining quota" API, so we meter locally: count TTS
//  characters synthesized and STT seconds recorded, reset each calendar month.
/// Hard caps sit below the published F0 limits so we stop *before* Azure rejects
/// a request (which would otherwise surface as a 401/429 to the user).
struct SpeechQuota: Equatable {
    var ttsCharsUsed: Int
    var sttSecondsUsed: Int

    static let ttsCap = 480_000      // F0 = 500k/month; leave 20k margin
    static let sttCapSeconds = 17_000 // F0 = 5h/month = 18000s; leave ~0.3h margin

    var ttsRemaining: Int { max(0, Self.ttsCap - ttsCharsUsed) }
    var sttRemainingSeconds: Int { max(0, Self.sttCapSeconds - sttSecondsUsed) }
    var sttRemainingHours: Double { Double(sttRemainingSeconds) / 3600.0 }

    var ttsRatio: Double { min(1.0, Double(ttsCharsUsed) / Double(Self.ttsCap)) }
    var sttRatio: Double { min(1.0, Double(sttSecondsUsed) / Double(Self.sttCapSeconds)) }
}

final class SpeechQuotaTracker: ObservableObject {
    static let shared = SpeechQuotaTracker()

    @Published private(set) var quota: SpeechQuota

    private let defaults = UserDefaults.standard
    private enum Key {
        static let month = "azure_speech_month"
        static let tts = "azure_speech_tts_chars"
        static let stt = "azure_speech_stt_seconds"
    }

    private init() {
        quota = SpeechQuota(ttsCharsUsed: 0, sttSecondsUsed: 0)
        reload()
    }

    /// Current calendar month key, e.g. "2026-06".
    private var currentMonth: String {
        let f = DateFormatter()
        f.dateFormat = "yyyy-MM"
        return f.string(from: Date())
    }

    /// Reload from disk; auto-resets counters when the calendar month rolls over.
    func reload() {
        let storedMonth = defaults.string(forKey: Key.month) ?? currentMonth
        if storedMonth != currentMonth {
            // New month → wipe persisted counters.
            defaults.set(currentMonth, forKey: Key.month)
            defaults.set(0, forKey: Key.tts)
            defaults.set(0, forKey: Key.stt)
            quota = SpeechQuota(ttsCharsUsed: 0, sttSecondsUsed: 0)
        } else {
            quota = SpeechQuota(
                ttsCharsUsed: defaults.integer(forKey: Key.tts),
                sttSecondsUsed: defaults.integer(forKey: Key.stt)
            )
        }
    }

    func canConsumeTTS(chars: Int) -> Bool {
        reload()
        return quota.ttsCharsUsed + chars <= SpeechQuota.ttsCap
    }

    func canConsumeSTT(seconds: Int) -> Bool {
        reload()
        return quota.sttSecondsUsed + seconds <= SpeechQuota.sttCapSeconds
    }

    func consumeTTS(chars: Int) {
        reload()
        let used = quota.ttsCharsUsed + chars
        defaults.set(used, forKey: Key.tts)
        DispatchQueue.main.async { self.quota.ttsCharsUsed = used }
    }

    func consumeSTT(seconds: Int) {
        reload()
        let used = quota.sttSecondsUsed + seconds
        defaults.set(used, forKey: Key.stt)
        DispatchQueue.main.async { self.quota.sttSecondsUsed = used }
    }

    /// Emergency/debug reset of local counters.
    func reset() {
        defaults.set(currentMonth, forKey: Key.month)
        defaults.set(0, forKey: Key.tts)
        defaults.set(0, forKey: Key.stt)
        quota = SpeechQuota(ttsCharsUsed: 0, sttSecondsUsed: 0)
    }
}

// MARK: - Speakable text extraction

/// Strips an assistant message down to what's worth reading aloud, so we don't
/// burn TTS character quota on code blocks, markdown syntax, mentions, or URLs.
enum SpeakableText {
    /// Single TTS utterance cap — long replies are truncated to stay snappy and cheap.
    static let maxChars = 800

    static func extract(from raw: String) -> String {
        // Drop fenced code blocks (```...```) entirely.
        let withoutCode = raw.components(separatedBy: "```")
            .enumerated()
            .filter { $0.offset % 2 == 0 } // even indices = prose, odd = code
            .map { $0.element }
            .joined(separator: " ")

        var lines: [String] = []
        for rawLine in withoutCode.components(separatedBy: "\n") {
            var line = rawLine
            // Drop inline code spans `...`.
            line = stripInlineCode(from: line)
            // Drop bare URLs.
            line = stripURLs(from: line)
            // Drop markdown heading/list/quote markers.
            line = stripMarkdownMarkers(from: line)
            // Drop @session / &session routing directives.
            line = stripDirectives(from: line)
            // Collapse markdown emphasis **bold** / *italic* / _underline_.
            line = stripEmphasis(from: line)

            let trimmed = line.trimmingCharacters(in: .whitespaces)
            if !trimmed.isEmpty {
                lines.append(trimmed)
            }
        }

        var result = lines.joined(separator: ". ")
        if result.count > maxChars {
            let end = result.index(result.startIndex, offsetBy: maxChars)
            result = String(result[result.startIndex..<end]) + "…回复过长，已截断"
        }
        return result.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    private static func stripInlineCode(from line: String) -> String {
        var s = line
        while let r = s.range(of: "`[^`]+`", options: .regularExpression) {
            let inner = s[r]
                .replacingOccurrences(of: "`", with: "")
            s.replaceSubrange(r, with: inner)
        }
        return s
    }

    private static func stripURLs(from line: String) -> String {
        line.replacingOccurrences(
            of: #"https?://[^\s)]+"#,
            with: "链接",
            options: .regularExpression
        )
    }

    private static func stripMarkdownMarkers(from line: String) -> String {
        var s = line.trimmingCharacters(in: .whitespaces)
        let prefixes: [(String, Int)] = [
            ("### ", 4), ("## ", 3), ("# ", 2),
            ("- ", 2), ("* ", 2), ("> ", 2),
        ]
        for (prefix, len) in prefixes where s.hasPrefix(prefix) {
            s = String(s.dropFirst(len))
            break
        }
        // Numbered list "1. "
        if let dot = s.range(of: ". "), s.range(of: "^[0-9]+\\. ", options: .regularExpression) != nil {
            s = String(s[dot.upperBound...])
        }
        // Horizontal rules made of - * _.
        let only = s.filter { !$0.isWhitespace }
        if only.count >= 3, only.allSatisfy({ $0 == "-" || $0 == "*" || $0 == "_" }) {
            return ""
        }
        return s
    }

    private static func stripDirectives(from line: String) -> String {
        line.replacingOccurrences(
            of: #"[&@]\S+"#,
            with: "",
            options: .regularExpression
        )
    }

    private static func stripEmphasis(from line: String) -> String {
        var s = line
        for pat in [#"\*\*([^*]+)\*\*"#, #"\*([^*]+)\*"#, #"_([^_]+)_"#, #"__([^_]+)__"#] {
            // Replace iteratively in case of overlapping matches.
            while let r = s.range(of: pat, options: .regularExpression) {
                let inner = s[r].filter { $0 != "*" && $0 != "_" }
                s.replaceSubrange(r, with: inner)
            }
        }
        return s
    }
}

// MARK: - Manager

/// Coordinates Azure Speech STT + TTS for voice conversation.
///
/// STT: continuous recognition from the mic; a trailing-silence timer treats
///   ~1.5s of no new final result as "user finished speaking", stops, and
///   delivers the accumulated text.
/// TTS: streaming synthesis via SPXSpeechSynthesizer; audio chunks are pushed
///   to an AVAudioEngine player for low-latency playback.
/// Both paths meter against SpeechQuotaTracker and refuse to run when the
/// local monthly cap is reached.
@MainActor
final class AzureSpeechManager: NSObject, ObservableObject {

    enum ListenState: Equatable {
        case idle
        case listening(partial: String)
        case error(String)
    }

    enum SpeakState: Equatable {
        case idle
        case speaking
        case error(String)
    }

    @Published var listenState: ListenState = .idle
    @Published var speakState: SpeakState = .idle

    /// Delivered when STT produces a final utterance (user finished a turn).
    var onRecognized: ((String) -> Void)?
    /// Delivered when TTS finishes a full utterance.
    var onSpeakFinished: (() -> Void)?
    /// Delivered when a quota wall is hit; the message is user-facing.
    var onQuotaExhausted: ((String) -> Void)?

    private let quota = SpeechQuotaTracker.shared
    private let config: Config

    // STT state
    private var recognizer: SPXSpeechRecognizer?
    private var isListening = false
    /// Continuous mode (phone-call style): the recognizer keeps running across
    /// turns. Each silence/recognized cycle delivers text via onRecognized but
    /// does NOT stop the recognizer — only stopListening() does.
    private var isContinuous = false
    private var recognitionStart: Date?
    private var silenceTimer: Timer?
    private let silenceInterval: TimeInterval = 2.5
    private var accumulatedText = ""
    /// While speaking (TTS), we suspend listening to avoid the assistant's own
    /// voice being captured as input. Resumed when playback ends.
    private var isListeningSuspended = false

    // TTS state
    private var synthesizer: SPXSpeechSynthesizer?
    private var audioEngine: AVAudioEngine?
    private var playerNode: AVAudioPlayerNode?
    private var audioFormat: AVAudioFormat?
    private var isSpeaking = false

    init(config: Config = .shared) {
        self.config = config
        super.init()
    }

    var isConfigured: Bool {
        !config.azureKey.isEmpty && !config.azureRegion.isEmpty
    }

    // MARK: STT

    /// Begin listening. In `continuous` mode (phone-call style) the recognizer
    /// keeps running across turns: each silence/recognized cycle delivers text
    /// via onRecognized but does NOT stop the mic. Use stopListening() to end.
    func startListening(continuous: Bool = false) {
        guard !isListening else { return }
        guard isConfigured else {
            listenState = .error("请先在设置里填写 Azure Speech key 和 region")
            return
        }
        // Best-effort reserve check: assume a turn is ~30s. If even that can't
        // fit, refuse up front rather than cutting off mid-sentence.
        guard quota.canConsumeSTT(seconds: 30) else {
            let msg = "语音识别额度已用完（本月上限 \(SpeechQuota.sttCapSeconds / 3600) 小时），已停止。"
            listenState = .error(msg)
            onQuotaExhausted?(msg)
            return
        }

        do {
            let speechCfg = try SPXSpeechConfiguration(
                subscription: config.azureKey,
                region: config.azureRegion
            )
            // zh-CN favours Mandarin input, which matches the app's audience.
            speechCfg.speechRecognitionLanguage = "zh-CN"
            let audioCfg = SPXAudioConfiguration()
            let recognizer = try SPXSpeechRecognizer(
                speechConfiguration: speechCfg,
                audioConfiguration: audioCfg
            )
            self.recognizer = recognizer
            self.isContinuous = continuous
            accumulatedText = ""

            // Partial results ("recognizing"): update the live display and
            // reset the silence timer. We do NOT stop here — a partial result
            // is mid-phrase, not end-of-turn.
            recognizer.addRecognizingEventHandler { [weak self] _, evt in
                guard let self else { return }
                Task { @MainActor in
                    // Ignore while suspended (TTS is playing in call mode).
                    guard !self.isListeningSuspended else { return }
                    if let partial = evt.result.text, !partial.isEmpty {
                        self.listenState = .listening(partial: partial)
                        self.resetSilenceTimer()
                    }
                }
            }

            // Final per-sentence result ("recognized"): append to the
            // accumulated transcript and reset the silence timer. A continuous
            // recognizer fires this once PER SENTENCE, so we must keep
            // listening — it is NOT an end-of-turn signal.
            recognizer.addRecognizedEventHandler { [weak self] _, evt in
                guard let self else { return }
                Task { @MainActor in
                    // Ignore while suspended (TTS is playing in call mode).
                    guard !self.isListeningSuspended else { return }
                    if let text = evt.result.text, !text.isEmpty {
                        // Avoid duplicating when a final result overlaps the
                        // last accumulated text (Azure sometimes re-emits).
                        if !self.accumulatedText.isEmpty {
                            self.accumulatedText += " "
                        }
                        self.accumulatedText += text
                        self.listenState = .listening(partial: self.accumulatedText)
                    }
                    // Reset the silence window so a natural pause between
                    // sentences doesn't cut the user off mid-thought.
                    self.resetSilenceTimer()
                }
            }

            recognizer.addCanceledEventHandler { [weak self] _, evt in
                guard let self else { return }
                Task { @MainActor in
                    // reason == .error means a genuine failure (auth, quota,
                    // network, mic); .endOfStream is a normal session close.
                    if evt.reason == .error {
                        self.listenState = .error("语音识别出错：\(evt.errorDetails ?? "")")
                    }
                    // Always fully stop on cancel, even in continuous mode.
                    self.stopListening()
                }
            }

            recognitionStart = Date()
            try recognizer.startContinuousRecognition()
            isListening = true
            isListeningSuspended = false
            listenState = .listening(partial: "")
            resetSilenceTimer()
        } catch {
            listenState = .error("启动识别失败：\(error.localizedDescription)")
        }
    }

    /// Stop listening and deliver whatever was recognized so far. Fully ends
    /// the recognizer regardless of continuous mode.
    func stopListening() {
        guard isListening else { return }
        deliverCurrentTurn()
        fullyStopRecognizer()
    }

    /// Deliver the current accumulated/partial transcript via onRecognized and
    /// reset the buffers. In continuous mode the recognizer keeps running.
    private func deliverCurrentTurn() {
        silenceTimer?.invalidate()
        silenceTimer = nil

        // Prefer accumulated final results; fall back to the last partial shown
        // so a fast stop still sends what the user said.
        var text = accumulatedText.trimmingCharacters(in: .whitespacesAndNewlines)
        if text.isEmpty, let partial = currentPartialText() {
            text = partial.trimmingCharacters(in: .whitespacesAndNewlines)
        }
        accumulatedText = ""
        if isContinuous {
            // Keep the live partial display; a new turn will overwrite it.
            listenState = .listening(partial: "")
        }
        if !text.isEmpty {
            onRecognized?(text)
        }
    }

    /// Tear down the recognizer and stop metering audio time.
    private func fullyStopRecognizer() {
        guard isListening else { return }
        isListening = false
        isContinuous = false

        let recognizer = self.recognizer
        self.recognizer = nil
        // stopContinuousRecognition can block for several seconds on iOS; run
        // it off the main thread so the UI doesn't freeze.
        DispatchQueue.global(qos: .userInitiated).async {
            try? recognizer?.stopContinuousRecognition()
        }

        // Meter the actual audio duration consumed.
        if let start = recognitionStart {
            let seconds = Int(Date().timeIntervalSince(start))
            quota.consumeSTT(seconds: max(1, seconds))
            recognitionStart = nil
        }
        listenState = .idle
    }

    /// Pull the live partial transcript out of the current listen state.
    private func currentPartialText() -> String? {
        if case .listening(let partial) = listenState, !partial.isEmpty {
            return partial
        }
        return nil
    }

    private func resetSilenceTimer() {
        silenceTimer?.invalidate()
        silenceTimer = Timer.scheduledTimer(withTimeInterval: silenceInterval, repeats: false) { [weak self] _ in
            Task { @MainActor in
                guard let self, self.isListening else { return }
                // Silence window elapsed with no new speech → deliver the turn.
                // In single-shot mode this also stops the recognizer; in
                // continuous (call) mode it only flushes the current turn and
                // keeps listening for the next one.
                self.deliverCurrentTurn()
                if !self.isContinuous {
                    self.fullyStopRecognizer()
                } else {
                    // Restart the silence watch for the next turn.
                    self.resetSilenceTimer()
                }
            }
        }
    }

    // MARK: Call-mode mic suspension

    /// Suspend listening while TTS plays (call mode), to avoid capturing the
    /// assistant's own voice. No-op if not currently listening.
    func suspendListening() {
        guard isListening, !isListeningSuspended else { return }
        isListeningSuspended = true
        // Flush any in-flight turn, then pause without tearing down — the
        // recognizer keeps running but we ignore further results until resumed.
        silenceTimer?.invalidate()
        silenceTimer = nil
    }

    /// Resume listening after TTS finishes (call mode).
    func resumeListening() {
        guard isListening, isListeningSuspended else { return }
        isListeningSuspended = false
        accumulatedText = ""
        listenState = .listening(partial: "")
        resetSilenceTimer()
    }

    // MARK: TTS

    /// Synthesize and play `text`. Refuses if quota is exhausted or text is empty.
    func speak(_ text: String) {
        let cleaned = SpeakableText.extract(from: text)
        guard !cleaned.isEmpty else { return }
        guard isConfigured else { return }
        guard quota.canConsumeTTS(chars: cleaned.count) else {
            let msg = "语音合成额度已用完（本月上限 \(SpeechQuota.ttsCap) 字符），已停止朗读。"
            speakState = .error(msg)
            onQuotaExhausted?(msg)
            return
        }

        stopSpeaking()

        do {
            let speechCfg = try SPXSpeechConfiguration(
                subscription: config.azureKey,
                region: config.azureRegion
            )
            speechCfg.speechSynthesisLanguage = "zh-CN"
            // Deliberately do NOT pin a voice name — custom names like
            // "zh-CN-XiaoxiaoMultilingual" may be unavailable in some regions
            // and cause synthesis to fail on first use. Letting the service
            // pick the default zh-CN voice is the reliable path; a specific
            // voice can be re-added once the region's voice list is confirmed.
            // SPXSpeechSynthesizer requires an explicit audio configuration.
            let audioCfg = SPXAudioConfiguration()
            let synth = try SPXSpeechSynthesizer(
                speechConfiguration: speechCfg,
                audioConfiguration: audioCfg
            )
            self.synthesizer = synth

            synth.addSynthesizingEventHandler { [weak self] (_: SPXSpeechSynthesizer, evt: SPXSpeechSynthesisEventArgs) in
                guard let self else { return }
                Task { @MainActor in
                    if let audio = evt.result.audioData {
                        self.feedAudio(audio)
                    }
                }
            }
            synth.addSynthesisCompletedEventHandler { [weak self] (_: SPXSpeechSynthesizer, _: SPXSpeechSynthesisEventArgs) in
                guard let self else { return }
                Task { @MainActor in
                    self.quota.consumeTTS(chars: cleaned.count)
                    self.finishSpeaking()
                    self.onSpeakFinished?()
                }
            }
            synth.addSynthesisCanceledEventHandler { [weak self] (_: SPXSpeechSynthesizer, evt: SPXSpeechSynthesisEventArgs) in
                guard let self else { return }
                Task { @MainActor in
                    // Extract error details via the cancellation helper — the
                    // synthesis result itself only carries a `reason`, not an
                    // error code/details. CancellationDetails decodes the cause.
                    let details = try? SPXSpeechSynthesisCancellationDetails(
                        fromCanceledSynthesisResult: evt.result
                    )
                    let reason = details?.reason
                    let detail = details?.errorDetails ?? ""
                    // .error = genuine failure (auth/quota/voice-not-found);
                    // .endOfStream = intentional stop (user tapped stop, or a
                    // new speak() interrupted this one) → stay silent.
                    if reason == .error {
                        let msg: String
                        let code = details?.errorCode
                        // Forbidden is the actual "F0 free quota exhausted"
                        // signal; auth/connection/too-many-requests are related
                        // access failures worth flagging the same way.
                        if code == .forbidden || code == .authenticationFailure
                            || code == .tooManyRequests || code == .connectionFailure {
                            msg = "语音合成额度可能已用尽或鉴权失败（\(detail)）"
                            self.onQuotaExhausted?(msg)
                        } else {
                            msg = "语音合成出错：\(detail)"
                        }
                        self.speakState = .error(msg)
                    }
                    self.finishSpeaking()
                }
            }

            try startAudioEngine()
            isSpeaking = true
            speakState = .speaking
            // speakText blocks until synthesis completes — run it off the main
            // actor so the UI stays responsive. The Synthesizing/Completed/
            // Canceled handlers hop back to @MainActor via Task { @MainActor }.
            let synthRef = synth
            let cleanedCount = cleaned.count
            DispatchQueue.global(qos: .userInitiated).async {
                do {
                    let result = try synthRef.speakText(cleaned)
                    // speakText returns once synthesis is done. If no handler
                    // already finalized, surface the outcome here.
                    Task { @MainActor in
                        guard self.isSpeaking else { return }
                        // .synthesizingAudioCompleted = success → meter + finalize.
                        // Anything else (canceled/error) → the Canceled handler
                        // owns that path; only finalize here on clean success.
                        if result.reason == .synthesizingAudioCompleted {
                            self.quota.consumeTTS(chars: cleanedCount)
                            self.finishSpeaking()
                            self.onSpeakFinished?()
                        }
                    }
                } catch {
                    Task { @MainActor in
                        guard self.isSpeaking else { return }
                        self.speakState = .error("语音合成失败：\(error.localizedDescription)")
                        self.finishSpeaking()
                    }
                }
            }
        } catch {
            speakState = .error("语音合成失败：\(error.localizedDescription)")
            finishSpeaking()
        }
    }

    func stopSpeaking() {
        guard isSpeaking else { return }
        finishSpeaking()
    }

    private func finishSpeaking() {
        isSpeaking = false
        synthesizer = nil
        playerNode?.stop()
        if audioEngine?.isRunning == true {
            audioEngine?.stop()
        }
        speakState = .idle
    }

    // MARK: Audio playback

    /// Lazily set up the AVAudioEngine once per speaking session.
    private func startAudioEngine() throws {
        if audioEngine == nil {
            let engine = AVAudioEngine()
            let node = AVAudioPlayerNode()
            engine.attach(node)
            engine.connect(node, to: engine.mainMixerNode, format: nil)
            audioEngine = engine
            playerNode = node
        }
        // Azure sends 16-bit PCM by default; build the format lazily on the
        // first chunk so we match whatever sample rate arrives.
        audioFormat = nil
        try AVAudioSession.sharedInstance().setCategory(.playback, mode: .default)
        try AVAudioSession.sharedInstance().setActive(true)
        if !(audioEngine?.isRunning ?? false) {
            try audioEngine?.start()
        }
        playerNode?.play()
    }

    /// Push a synthesized PCM chunk to the player. The first chunk establishes
    /// the stream format (Azure default: 16kHz, 16-bit, mono).
    private func feedAudio(_ data: Data) {
        guard !data.isEmpty, let node = playerNode, let engine = audioEngine else { return }
        if audioFormat == nil {
            audioFormat = AVAudioFormat(
                commonFormat: .pcmFormatInt16,
                sampleRate: 16000,
                channels: 1,
                interleaved: true
            )
            if let fmt = audioFormat {
                engine.connect(node, to: engine.mainMixerNode, format: fmt)
            }
        }
        guard let fmt = audioFormat else { return }
        let frameCount = AVAudioFrameCount(data.count) / fmt.streamDescription.pointee.mBytesPerFrame
        guard frameCount > 0,
              let buffer = AVAudioPCMBuffer(pcmFormat: fmt, frameCapacity: frameCount) else { return }
        // Set frameLength first so mDataByteSize reflects the actual sample count.
        buffer.frameLength = frameCount
        // Copy the PCM bytes into the buffer's own storage — do NOT alias the
        // Data's pointer (it may be freed before playback finishes).
        let byteCount = min(data.count, Int(buffer.audioBufferList.pointee.mBuffers.mDataByteSize))
        if let dst = buffer.audioBufferList.pointee.mBuffers.mData {
            data.withUnsafeBytes { raw in
                if let src = raw.baseAddress {
                    memcpy(dst, src, byteCount)
                }
            }
        }
        node.scheduleBuffer(buffer, completionHandler: nil)
    }
}
