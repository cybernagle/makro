import Foundation

// MARK: - Voice activity detection

/// A lightweight, on-device voice activity detector that gates which PCM frames
/// reach the Azure recognizer.
///
/// The recognizer can't run continuously in call mode without burning Azure STT
/// quota on silence, so we run this local energy VAD on the mic tap: it only
/// emits frames (via `onAudio`) while real speech is detected, plus a short
/// pre-roll captured before speech was confirmed so the leading consonant isn't
/// clipped. Silence frames never reach the recognizer → not billed.
///
/// `process(_:)` is safe to call from the AVAudioEngine render thread: it only
/// hops the frame onto a serial queue; all RMS math, the state machine, and the
/// `onAudio` emission run off the real-time thread. The `vadSpeechStarted`
/// signal lets call mode interrupt TTS the instant real mic energy appears
/// (more reliable than waiting for Azure's first partial).
///
/// v1 uses a simple RMS/energy threshold. The `process` internals can later be
/// swapped for a WebRTC-style VAD without changing this contract.
@MainActor protocol VoiceActivityDetectorDelegate: AnyObject {
    /// Fired (on the main actor) once `minActiveFrames` consecutive active
    /// frames confirm the start of an utterance. Pre-roll has already been
    /// emitted by the time this fires.
    func vadSpeechStarted()
    /// Fired (on the main actor) after `hangoverFrames` consecutive silent
    /// frames end an utterance. Audio for the trailing silence was already
    /// emitted so Azure can endpoint naturally.
    func vadSpeechEnded()
}

final class VoiceActivityDetector {

    struct Config {
        /// Normalized RMS (0...1) at/above which a single frame counts as speech.
        var threshold: Double
        /// Frames of audio kept before a confirmed speech start, emitted first so
        /// the leading word isn't clipped. ~300ms at the frame cadence used.
        var preRollFrames: Int
        /// Consecutive active frames required to confirm a speech start. Debounces
        /// transient clicks/keyboard noise (~30ms).
        var minActiveFrames: Int
        /// Consecutive silent frames required to confirm an utterance end. Keeps
        /// brief mid-sentence pauses from cutting a turn (~400ms).
        var hangoverFrames: Int

        static let `default` = Config(threshold: 0.02, preRollFrames: 30, minActiveFrames: 3, hangoverFrames: 40)
    }

    private let config: Config
    /// Off-thread serial queue: owns the state machine + audio emission so the
    /// render thread only pays for one `async` dispatch per frame.
    private let queue = DispatchQueue(label: "makro.vad", qos: .userInitiated)
    /// Called (on `queue`, in frame order) for every PCM chunk that should reach
    /// the recognizer: pre-roll on speech start, then active frames, then the
    /// trailing-silence hangover.
    private let onAudio: (Data) -> Void

    weak var delegate: VoiceActivityDetectorDelegate?

    // State — all mutated only on `queue`.
    private var preRoll: [Data] = []
    private var isActive = false
    private var activeStreak = 0
    private var silentStreak = 0

    init(config: Config = .default, onAudio: @escaping (Data) -> Void) {
        self.config = config
        self.onAudio = onAudio
    }

    /// Feed one frame of 16-bit mono PCM (the same bytes that would go to the
    /// push stream). Computes nothing on the calling thread.
    func process(_ data: Data) {
        let frame = data
        queue.async { [weak self] in
            self?.handleFrame(frame)
        }
    }

    /// Drop buffered state (called when listening suspends/resumes).
    func reset() {
        queue.async { [weak self] in
            guard let self else { return }
            self.preRoll.removeAll(keepingCapacity: true)
            self.isActive = false
            self.activeStreak = 0
            self.silentStreak = 0
        }
    }

    private func handleFrame(_ data: Data) {
        let frameActive = rms(of: data) >= config.threshold

        if isActive {
            // Emit every frame while active — including the trailing-silence
            // hangover — so Azure's utterance endpointing works on natural audio.
            onAudio(data)
            if frameActive {
                silentStreak = 0
            } else {
                silentStreak += 1
                if silentStreak >= config.hangoverFrames {
                    isActive = false
                    silentStreak = 0
                    activeStreak = 0
                    preRoll.removeAll(keepingCapacity: true)
                    notify { $0.vadSpeechEnded() }
                }
            }
        } else {
            // Idle: keep a rolling pre-roll so the start of the next utterance
            // isn't lost when speech is confirmed a few frames late.
            preRoll.append(data)
            if preRoll.count > config.preRollFrames {
                preRoll.removeFirst(preRoll.count - config.preRollFrames)
            }
            if frameActive {
                activeStreak += 1
                if activeStreak >= config.minActiveFrames {
                    isActive = true
                    silentStreak = 0
                    // Emit captured pre-roll (which already includes this frame)
                    // in order, then arm active mode.
                    for pre in preRoll { onAudio(pre) }
                    preRoll.removeAll(keepingCapacity: true)
                    notify { $0.vadSpeechStarted() }
                }
            } else {
                activeStreak = 0
            }
        }
    }

    /// RMS of 16-bit mono PCM, normalized to 0...1.
    private func rms(of data: Data) -> Double {
        let sampleCount = data.count / 2
        guard sampleCount > 0 else { return 0 }
        return data.withUnsafeBytes { raw -> Double in
            guard let base = raw.baseAddress?.assumingMemoryBound(to: Int16.self) else { return 0 }
            var sumSq: Double = 0
            for i in 0..<sampleCount {
                let s = Double(base[i]) / 32768.0
                sumSq += s * s
            }
            return (sumSq / Double(sampleCount)).squareRoot()
        }
    }

    private func notify(_ block: @MainActor @escaping (VoiceActivityDetectorDelegate) -> Void) {
        guard let delegate else { return }
        Task { @MainActor in block(delegate) }
    }
}

// MARK: - Commit-phrase detection

/// Decides, per Azure `Recognized` segment, whether the user said a结束语
/// (commit phrase) that should submit the voice turn.
///
/// A phrase matches only at the TRAILING edge of a segment (case-insensitive,
/// trailing punctuation tolerated). On a match the phrase is stripped from the
/// running transcript and `.commit(payload)` is returned; the caller sends
/// `payload` unless it's empty (user said only the phrase with nothing before
/// it — nothing to send, keep listening). Segments without a phrase are folded
/// into the transcript via `.accumulate`.
final class CommitPhraseDetector {

    /// Trailing punctuation that often follows a spoken phrase and would block
    /// a suffix match if not stripped first.
    private static let trailingPunct = CharacterSet(charactersIn: "。，,、.!！?？;；:： \t\n")

    private let phrases: [String]      // lowercased, trimmed
    private(set) var accumulated = ""

    init(phrases: [String]) {
        self.phrases = phrases
            .map { $0.trimmingCharacters(in: .whitespaces).lowercased() }
            .filter { !$0.isEmpty }
    }

    enum Decision: Equatable {
        /// Fold this segment into the running transcript; keep listening.
        case accumulate(transcript: String)
        /// A trailing commit phrase was matched and stripped. `payload` is the
        /// ready-to-send transcript (may be empty → caller should ignore and
        /// continue listening).
        case commit(payload: String)
    }

    func ingest(_ segment: String) -> Decision {
        let cleaned = segment.trimmingCharacters(in: Self.trailingPunct)
        let lower = cleaned.lowercased()

        for phrase in phrases {
            guard !phrase.isEmpty, lower.hasSuffix(phrase) else { continue }
            // Boundary guard: the phrase must be preceded by a word boundary
            // (start of string, whitespace, or punctuation). Without this a
            // single-character phrase like "好" would falsely match the tail of
            // "...很好" / "你好" and commit a half-thought.
            let prefix = String(lower.dropLast(phrase.count))
            let boundaryOK = prefix.isEmpty || Self.isBoundary(prefix.last!)
            guard boundaryOK else { continue }
            // Strip the matched phrase from the original-cased segment so we keep
            // the user's casing in what we send.
            let stripped = String(cleaned.dropLast(phrase.count))
                .trimmingCharacters(in: Self.trailingPunct)
            if !accumulated.isEmpty, !stripped.isEmpty {
                accumulated += " " + stripped
            } else if !stripped.isEmpty {
                accumulated = stripped
            }
            // else: segment was only the phrase → leave accumulated untouched.
            let payload = accumulated.trimmingCharacters(in: .whitespaces)
            accumulated = ""
            return .commit(payload: payload)
        }

        // No commit phrase: accumulate the original-cased segment.
        accumulated += accumulated.isEmpty ? segment : " " + segment
        return .accumulate(transcript: accumulated)
    }

    /// True for characters that may sit immediately before a commit phrase:
    /// whitespace or common CJK/Latin sentence/clause punctuation.
    private static func isBoundary(_ char: Character) -> Bool {
        if char.isWhitespace { return true }
        return "。，,、.!！?？;；:：".contains(char)
    }

    func reset() {
        accumulated = ""
    }
}
