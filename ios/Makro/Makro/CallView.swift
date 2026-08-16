import SwiftUI

/// Full-screen phone-call style voice conversation.
///
/// The mic stays open (continuous STT); each recognized utterance is sent, and
/// every assistant reply is read aloud. A status orb in the center reflects
/// the current phase (listening / thinking / speaking), the live transcript
/// scrolls below it, and a red hang-up button ends the call.
struct CallView: View {
    @ObservedObject var vm: ChatViewModel
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        ZStack {
            DS.Canvas.terminal.ignoresSafeArea()

            VStack(spacing: 0) {
                header
                Spacer()
                statusOrb
                Spacer()
                transcript
                if vm.pendingPlan != nil {
                    pendingPlanCard
                        .padding(.top, 8)
                }
                Spacer()
                controlsRow
            }
            .padding(.horizontal, 24)
            .padding(.bottom, 40)
            .padding(.top, 20)
            .animation(DS.snappy, value: vm.pendingPlan)
        }
        .preferredColorScheme(.dark)
        .onAppear { vm.startCall() }
        .onDisappear { vm.endCall() }
        .onReceive(CallRouter.shared.$pendingEnd) { wantsEnd in
            // Siri/Shortcuts "hang up" → dismiss; onDisappear ends the call
            // (stops STT/TTS, clears Now Playing).
            guard wantsEnd else { return }
            CallRouter.shared.pendingEnd = false
            dismiss()
        }
        .onChange(of: phase) { newPhase in
            // Keep the lock-screen card in sync with the call phase.
            NowPlayingManager.shared.updatePhase(nowPlayingPhaseLabel(for: newPhase))
        }
        .onChange(of: vm.isMuted) { _ in
            NowPlayingManager.shared.updatePhase(nowPlayingPhaseLabel(for: phase))
        }
        .onChange(of: vm.pendingPlan) { _ in
            // A staged plan flips the lock-screen prompt to the confirm ask.
            NowPlayingManager.shared.updatePhase(nowPlayingPhaseLabel(for: phase))
        }
    }

    /// Lock-screen card label. Paused takes priority (the mic is fully off;
    /// mute/phase are meaningless while on hold), then mute, then phase.
    private func nowPlayingPhaseLabel(for p: Phase) -> String {
        if vm.isCallPaused { return "已暂停" }
        if vm.isMuted { return "已静音" }
        // Mirror the in-app computed phaseLabel: a staged plan takes over the
        // cue (lock screen should prompt 确认, not "正在聆听…").
        if vm.pendingPlan != nil { return "待你确认 — 说『确认』或点按钮" }
        return phaseLabel(for: p)
    }

    // MARK: - Header

    private var header: some View {
        VStack(spacing: 4) {
            // Mode dropdown: tap to switch 闲聊 / 落实 / 查询 (manual; voice-
            // switching is intentionally deferred — reliability + complexity).
            Menu {
                ForEach(CallMode.allCases, id: \.self) { mode in
                    Button {
                        vm.setCallMode(mode)
                    } label: {
                        HStack {
                            Text("\(mode.label) · \(mode.hint)")
                            if mode == vm.callMode { Image(systemName: "checkmark") }
                        }
                    }
                }
            } label: {
                HStack(spacing: 5) {
                    Text("Makro")
                        .font(DS.display(26, .semibold))
                        .foregroundStyle(DS.Canvas.phosphor)
                    Text("·\(vm.callMode.label)")
                        .font(DS.text(15, .medium))
                        .foregroundStyle(.white.opacity(0.75))
                    Image(systemName: "chevron.down")
                        .font(.system(size: 10, weight: .bold))
                        .foregroundStyle(.white.opacity(0.5))
                }
            }
            Text(phaseLabel)
                .font(DS.text(13, .medium))
                .foregroundStyle(.white.opacity(0.6))
                .animation(DS.snappy, value: phase)
        }
    }

    // MARK: - Status orb

    private var statusOrb: some View {
        ZStack {
            // Pulsing rings. Hidden while paused — the call is on hold.
            ForEach(0..<3, id: \.self) { i in
                Circle()
                    .stroke(orbColor.opacity(0.18 - Double(i) * 0.05), lineWidth: 1.5)
                    .frame(width: 150 + CGFloat(i) * 40, height: 150 + CGFloat(i) * 40)
                    .scaleEffect(animateRings ? 1.08 : 0.92)
                    .opacity(vm.isCallPaused ? 0 : (animateRings ? 0.7 : 0.3))
                    .animation(
                        .easeInOut(duration: pulseDuration)
                            .repeatForever(autoreverses: true)
                            .delay(Double(i) * 0.2),
                        value: animateRings
                    )
            }
            // Core.
            Circle()
                .fill(
                    RadialGradient(
                        colors: [orbColor.opacity(0.9), orbColor.opacity(0.5)],
                        center: .center,
                        startRadius: 4,
                        endRadius: 56
                    )
                )
                .frame(width: 112, height: 112)
                .overlay(
                    Image(systemName: orbIcon)
                        .font(.system(size: 40, weight: .light))
                        .foregroundStyle(.white)
                )
                .scaleEffect(vm.isSpeaking ? 1.05 : 1.0)
                .animation(DS.spring, value: vm.isSpeaking)
        }
        .onAppear { animateRings = true }
        .onDisappear { animateRings = false }
    }

    // MARK: - Transcript

    private var transcript: some View {
        VStack(spacing: 8) {
            // Live partial / most recent user utterance.
            if let partial = vm.partialTranscript, !partial.isEmpty, vm.isListening {
                Text(partial)
                    .font(DS.text(15, .regular))
                    .foregroundStyle(.white.opacity(0.85))
                    .multilineTextAlignment(.center)
                    .frame(maxWidth: .infinity)
                    .padding(.horizontal, 8)
            }
            // Most recent assistant reply (what's being spoken).
            if let last = vm.messages.last, last.role == .assistant, !last.text.isEmpty {
                Text(last.text)
                    .font(DS.text(14, .regular))
                    .foregroundStyle(vm.isSpeaking ? DS.Canvas.phosphor : .white.opacity(0.5))
                    .multilineTextAlignment(.center)
                    .lineLimit(6)
                    .frame(maxWidth: .infinity)
                    .padding(.horizontal, 8)
                    .opacity(vm.isSpeaking ? 1 : 0.6)
                    .animation(DS.snappy, value: vm.isSpeaking)
            }
        }
        .frame(maxHeight: 180)
    }

    // MARK: - Pending plan (confirm before dispatch)

    /// Card shown when the assistant has proposed a plan during the call. The
    /// spoken summary already appeared in the transcript; this shows the
    /// structured execution target (who/what) and the confirm/deny buttons.
    /// Confirm → server dispatches to the session; deny → back to discussion.
    @ViewBuilder
    private var pendingPlanCard: some View {
        if let plan = vm.pendingPlan {
            VStack(alignment: .leading, spacing: 10) {
                HStack(spacing: 6) {
                    Image(systemName: "paperplane.fill")
                        .font(.system(size: 13, weight: .semibold))
                        .foregroundStyle(DS.Canvas.phosphor)
                    Text("待确认落实")
                        .font(DS.text(14, .semibold))
                        .foregroundStyle(.white)
                }
                Text(plan.summary.isEmpty ? plan.brief : plan.summary)
                    .font(DS.text(15, .medium))
                    .foregroundStyle(.white)
                    .multilineTextAlignment(.leading)
                    .fixedSize(horizontal: false, vertical: true)
                HStack(spacing: 5) {
                    Image(systemName: "person.crop.square.filled.and.at.rectangle")
                        .font(.system(size: 11))
                    Text("执行：\(plan.session)")
                }
                .font(DS.text(12, .regular))
                .foregroundStyle(.white.opacity(0.6))
                if !plan.brief.isEmpty && plan.brief != plan.summary {
                    Text(plan.brief)
                        .font(DS.text(12, .regular))
                        .foregroundStyle(.white.opacity(0.7))
                        .multilineTextAlignment(.leading)
                        .lineLimit(4)
                        .fixedSize(horizontal: false, vertical: true)
                }
                HStack(spacing: 10) {
                    Button { vm.denyPlan() } label: {
                        Text("取消")
                            .font(DS.text(14, .semibold))
                            .foregroundStyle(.white)
                            .frame(maxWidth: .infinity)
                            .padding(.vertical, 10)
                            .background(.white.opacity(0.12))
                            .clipShape(RoundedRectangle(cornerRadius: 10))
                    }
                    Button { vm.confirmPlan() } label: {
                        Text("确认落实")
                            .font(DS.text(14, .semibold))
                            .foregroundStyle(.black)
                            .frame(maxWidth: .infinity)
                            .padding(.vertical, 10)
                            .background(DS.Canvas.phosphor)
                            .clipShape(RoundedRectangle(cornerRadius: 10))
                    }
                }
                .padding(.top, 2)
            }
            .padding(16)
            .background(.white.opacity(0.06))
            .overlay(RoundedRectangle(cornerRadius: 14).stroke(DS.Canvas.phosphor.opacity(0.4), lineWidth: 1))
            .clipShape(RoundedRectangle(cornerRadius: 14))
        }
    }

    // MARK: - Call controls

    /// Pause (hold) + hang-up. Pause fully stops mic and TTS without dropping
    /// the call context (plan/phase/server state survive), for when a human
    /// conversation interrupts; resume rebuilds the recognizer in ~1-2s.
    private var controlsRow: some View {
        HStack(spacing: 48) {
            Button {
                vm.isCallPaused ? vm.resumeCall() : vm.pauseCall()
            } label: {
                VStack(spacing: 6) {
                    Image(systemName: vm.isCallPaused ? "play.fill" : "pause.fill")
                        .font(.system(size: 24, weight: .semibold))
                        .foregroundStyle(.white)
                        .frame(width: 64, height: 64)
                        .background(.white.opacity(vm.isCallPaused ? 0.28 : 0.14))
                        .clipShape(Circle())
                        .overlay(Circle().stroke(.white.opacity(0.15), lineWidth: 0.5))
                    Text(vm.isCallPaused ? "继续" : "暂停")
                        .font(DS.micro(11, .semibold))
                        .foregroundStyle(.white.opacity(0.6))
                }
            }
            hangUpButton
        }
    }

    private var hangUpButton: some View {
        Button {
            dismiss()
        } label: {
            VStack(spacing: 6) {
                Image(systemName: "phone.down.fill")
                    .font(.system(size: 26, weight: .semibold))
                    .foregroundStyle(.white)
                    .frame(width: 76, height: 76)
                    .background(DS.Ink.rose)
                    .clipShape(Circle())
                    .overlay(Circle().stroke(.white.opacity(0.15), lineWidth: 0.5))
                Text("结束通话")
                    .font(DS.micro(11, .semibold))
                    .foregroundStyle(.white.opacity(0.6))
            }
        }
    }

    // MARK: - Derived state

    @State private var animateRings = false

    private enum Phase: Equatable { case paused, listening, thinking, speaking }
    private var phase: Phase {
        if vm.isCallPaused { return .paused }
        if vm.isSpeaking { return .speaking }
        if vm.thinkingText != nil || vm.isStreaming { return .thinking }
        return .listening
    }

    private var phaseLabel: String {
        // A staged plan takes over the phase cue: the call is waiting on the
        // user's confirm/deny, not listening for a new utterance.
        if vm.pendingPlan != nil { return "待你确认 — 说『确认』或点按钮" }
        return phaseLabel(for: phase)
    }

    private func phaseLabel(for p: Phase) -> String {
        switch p {
        case .paused: return "已暂停"
        case .listening:
            // 闲聊 auto-sends on pause — teaching the commit phrase there would
            // be misleading. Other modes still need the explicit phrase.
            if vm.callMode == .chat { return vm.isListening ? "正在聆听…" : "准备中…" }
            return vm.isListening ? "正在聆听… 说『请发送』结束" : "准备中…"
        case .thinking: return "思考中…"
        case .speaking: return "正在回答…"
        }
    }

    private var pulseDuration: Double {
        switch phase {
        case .paused: return 3.0
        case .speaking: return 0.9
        case .thinking: return 1.6
        case .listening: return 2.2
        }
    }

    private var orbColor: Color {
        switch phase {
        case .paused: return .white.opacity(0.45)
        case .listening: return DS.Ink.mint
        case .thinking: return DS.Ink.amber
        case .speaking: return DS.Canvas.phosphor
        }
    }

    private var orbIcon: String {
        switch phase {
        case .paused: return "pause.fill"
        case .listening: return "waveform"
        case .thinking: return "ellipsis"
        case .speaking: return "speaker.wave.2.fill"
        }
    }
}
