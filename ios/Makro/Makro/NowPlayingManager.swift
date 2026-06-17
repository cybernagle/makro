import Foundation
import MediaPlayer

/// Surfaces the active voice call on the iOS lock screen via the Now Playing
/// infrastructure. While a call is active the lock screen shows a card with the
/// call title/phase and a playback-position style timer, plus remote-command
/// buttons mapped to call actions:
///   - togglePlayPause → mute/unmute the mic
///   - nextTrack       → hang up
///
/// This is lighter than CallKit (no incoming-call UX, no carrier integration)
/// and is the right fit for an app-initiated voice session that needs to stay
/// visible/controllable when the screen locks.
final class NowPlayingManager {

    static let shared = NowPlayingManager()

    /// Called when the user taps the hang-up button on the lock screen.
    var onHangUp: (() -> Void)?
    /// Called when the user taps play/pause (mute/unmute) on the lock screen.
    var onToggleMute: (() -> Void)?

    private var callStart: Date?
    private var timer: Timer?
    private var phaseLabel = "通话中"

    private init() {}

    // MARK: Lifecycle

    /// Begin publishing call info to the lock screen and wire remote commands.
    func startCall() {
        callStart = Date()
        phaseLabel = "正在聆听…"
        configureRemoteCommands()
        updateInfo()
        // Refresh elapsed time every second so the lock-screen timer ticks.
        timer?.invalidate()
        timer = Timer.scheduledTimer(withTimeInterval: 1.0, repeats: true) { [weak self] _ in
            self?.updateInfo()
        }
    }

    /// Stop publishing; clears the lock-screen card and releases command handlers.
    func endCall() {
        timer?.invalidate()
        timer = nil
        callStart = nil
        MPNowPlayingInfoCenter.default().nowPlayingInfo = nil
        // Disable our command handlers so the system stops routing events.
        let cc = MPRemoteCommandCenter.shared()
        cc.togglePlayPauseCommand.removeTarget(nil)
        cc.nextTrackCommand.removeTarget(nil)
    }

    /// Update the phase label shown on the lock screen (listening/thinking/speaking).
    func updatePhase(_ label: String) {
        phaseLabel = label
        updateInfo()
    }

    // MARK: Internals

    private func configureRemoteCommands() {
        let cc = MPRemoteCommandCenter.shared()
        // Play/pause button → mute/unmute the mic.
        cc.togglePlayPauseCommand.isEnabled = true
        cc.togglePlayPauseCommand.removeTarget(nil)
        cc.togglePlayPauseCommand.addTarget { [weak self] _ in
            self?.onToggleMute?()
            return .success
        }
        // "Next track" button → hang up (the most discoverable spare button).
        cc.nextTrackCommand.isEnabled = true
        cc.nextTrackCommand.removeTarget(nil)
        cc.nextTrackCommand.addTarget { [weak self] _ in
            self?.onHangUp?()
            return .success
        }
        // Disable the rest so the lock screen shows a clean, minimal control set.
        cc.playCommand.isEnabled = false
        cc.pauseCommand.isEnabled = false
        cc.previousTrackCommand.isEnabled = false
        cc.changePlaybackPositionCommand.isEnabled = false
        cc.skipForwardCommand.isEnabled = false
        cc.skipBackwardCommand.isEnabled = false
    }

    private func updateInfo() {
        var info: [String: Any] = [:]
        info[MPMediaItemPropertyTitle] = "Makro 通话"
        info[MPMediaItemPropertyArtist] = phaseLabel

        if let start = callStart {
            let elapsed = Date().timeIntervalSince(start)
            info[MPMediaItemPropertyPlaybackDuration] = elapsed
            info[MPNowPlayingInfoPropertyElapsedPlaybackTime] = elapsed
            info[MPNowPlayingInfoPropertyPlaybackRate] = 1.0
        }
        MPNowPlayingInfoCenter.default().nowPlayingInfo = info
    }
}
