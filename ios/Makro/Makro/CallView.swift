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
                Spacer()
                hangUpButton
            }
            .padding(.horizontal, 24)
            .padding(.bottom, 40)
            .padding(.top, 20)
        }
        .preferredColorScheme(.dark)
        .onAppear { vm.startCall() }
        .onDisappear { vm.endCall() }
        .onChange(of: phase) { newPhase in
            // Keep the lock-screen card in sync with the call phase.
            if vm.isMuted {
                NowPlayingManager.shared.updatePhase("已静音")
            } else {
                NowPlayingManager.shared.updatePhase(phaseLabel(for: newPhase))
            }
        }
        .onChange(of: vm.isMuted) { muted in
            NowPlayingManager.shared.updatePhase(muted ? "已静音" : phaseLabel(for: phase))
        }
    }

    // MARK: - Header

    private var header: some View {
        VStack(spacing: 4) {
            Text("Makro")
                .font(DS.display(26, .semibold))
                .foregroundStyle(DS.Canvas.phosphor)
            Text(phaseLabel)
                .font(DS.text(13, .medium))
                .foregroundStyle(.white.opacity(0.6))
                .animation(DS.snappy, value: phase)
        }
    }

    // MARK: - Status orb

    private var statusOrb: some View {
        ZStack {
            // Pulsing rings.
            ForEach(0..<3, id: \.self) { i in
                Circle()
                    .stroke(orbColor.opacity(0.18 - Double(i) * 0.05), lineWidth: 1.5)
                    .frame(width: 150 + CGFloat(i) * 40, height: 150 + CGFloat(i) * 40)
                    .scaleEffect(animateRings ? 1.08 : 0.92)
                    .opacity(animateRings ? 0.7 : 0.3)
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

    // MARK: - Hang up

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

    private enum Phase: Equatable { case listening, thinking, speaking }
    private var phase: Phase {
        if vm.isSpeaking { return .speaking }
        if vm.thinkingText != nil || vm.isStreaming { return .thinking }
        return .listening
    }

    private var phaseLabel: String {
        phaseLabel(for: phase)
    }

    private func phaseLabel(for p: Phase) -> String {
        switch p {
        case .listening: return vm.isListening ? "正在聆听… 说『请发送』结束" : "准备中…"
        case .thinking: return "思考中…"
        case .speaking: return "正在回答…"
        }
    }

    private var pulseDuration: Double {
        switch phase {
        case .speaking: return 0.9
        case .thinking: return 1.6
        case .listening: return 2.2
        }
    }

    private var orbColor: Color {
        switch phase {
        case .listening: return DS.Ink.mint
        case .thinking: return DS.Ink.amber
        case .speaking: return DS.Canvas.phosphor
        }
    }

    private var orbIcon: String {
        switch phase {
        case .listening: return "waveform"
        case .thinking: return "ellipsis"
        case .speaking: return "speaker.wave.2.fill"
        }
    }
}
