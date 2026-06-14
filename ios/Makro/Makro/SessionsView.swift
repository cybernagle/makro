import SwiftUI

struct SessionsView: View {
    @StateObject private var vm = SessionsViewModel()
    @State private var appeared = false

    private var activeCount: Int { vm.sessions.filter { $0.active }.count }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 18) {
                    header
                    if vm.sessions.isEmpty {
                        emptyState
                            .padding(.top, 60)
                    } else {
                        bentoGrid
                    }
                }
                .padding(.horizontal, 16)
                .padding(.top, 8)
                .padding(.bottom, 24)
            }
            .background(DS.Canvas.app.ignoresSafeArea())
            .navigationTitle("")
            .toolbar {
                ToolbarItem(placement: .principal) {
                    Text("Terminal")
                        .font(DS.display(18, .semibold))
                        .tracking(-0.3)
                        .foregroundStyle(.primary)
                }
            }
            .task { await vm.refreshSessions(); appeared = true }
            .refreshable { await vm.refreshSessions() }
        }
    }

    private var header: some View {
        HStack(alignment: .firstTextBaseline, spacing: 18) {
            VStack(alignment: .leading, spacing: 2) {
                Text("\(vm.sessions.count)")
                    .font(DS.mono(34, .semibold))
                    .foregroundStyle(.primary)
                    .tracking(-0.5)
                Text("sessions")
                    .font(DS.micro(10))
                    .textCase(.uppercase)
                    .foregroundStyle(.secondary)
            }
            Divider().frame(height: 32)
            VStack(alignment: .leading, spacing: 2) {
                Text("\(activeCount)")
                    .font(DS.mono(34, .semibold))
                    .foregroundStyle(DS.Ink.mint)
                    .tracking(-0.5)
                Text("active")
                    .font(DS.micro(10))
                    .textCase(.uppercase)
                    .foregroundStyle(.secondary)
            }
            Spacer()
            Image(systemName: "terminal")
                .font(.system(size: 22, weight: .light))
                .foregroundStyle(.tertiary)
        }
        .padding(.vertical, 4)
        .opacity(appeared ? 1 : 0)
        .animation(DS.spring, value: appeared)
    }

    private var bentoGrid: some View {
        let active = vm.sessions.filter { $0.active }
        let idle = vm.sessions.filter { !$0.active }

        return LazyVStack(alignment: .leading, spacing: 14) {
            if let hero = active.first {
                NavigationLink(destination: TerminalDetailView(sessionName: hero.name)) {
                    HeroSessionTile(session: hero, index: 0)
                }
                .buttonStyle(PressDown())
            }
            let rest = Array(active.dropFirst()) + idle
            LazyVGrid(columns: [GridItem(.flexible(), spacing: 12), GridItem(.flexible(), spacing: 12)], spacing: 12) {
                ForEach(Array(rest.enumerated()), id: \.element.id) { idx, session in
                    NavigationLink(destination: TerminalDetailView(sessionName: session.name)) {
                        SessionTile(session: session, index: idx + 1)
                    }
                    .buttonStyle(PressDown())
                }
            }
        }
    }

    private var emptyState: some View {
        VStack(spacing: 16) {
            ZStack {
                Circle()
                    .stroke(DS.Ink.mint.opacity(0.18), lineWidth: 1)
                    .frame(width: 88, height: 88)
                Circle()
                    .fill(DS.Ink.mint.opacity(0.08))
                    .frame(width: 56, height: 56)
                Image(systemName: "terminal")
                    .font(.system(size: 22, weight: .light))
                    .foregroundStyle(DS.Ink.mint)
            }
            VStack(spacing: 4) {
                Text("No sessions yet")
                    .font(DS.display(17, .semibold))
                    .foregroundStyle(.primary)
                Text("Send `@session-name` in chat to spin one up.")
                    .font(DS.text(13))
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
            }
        }
        .frame(maxWidth: .infinity)
        .padding(.horizontal, 32)
    }
}

// MARK: - Tiles

private struct HeroSessionTile: View {
    let session: Session
    let index: Int

    private var statusMode: StatusPill.Mode {
        if session.working { return .thinking }
        return session.active ? .active : .idle
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack {
                StatusPill(mode: statusMode)
                Spacer()
                Text(String(format: "%02d", index + 1))
                    .font(DS.mono(11, .semibold))
                    .foregroundStyle(.tertiary)
                if session.unread > 0 {
                    UnreadBadge(count: session.unread)
                }
            }
            Text(session.name)
                .font(DS.display(24, .semibold))
                .foregroundStyle(.primary)
                .lineLimit(1)
                .tracking(-0.4)
            HStack(spacing: 6) {
                Image(systemName: "chevron.right")
                    .font(.system(size: 10, weight: .bold))
                Text("open")
                    .font(DS.micro(10, .semibold))
                    .textCase(.uppercase)
            }
            .foregroundStyle(DS.Ink.mint)
        }
        .padding(18)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(
            LinearGradient(
                colors: [DS.Ink.mint.opacity(0.14), DS.Ink.mint.opacity(0.05)],
                startPoint: .topLeading,
                endPoint: .bottomTrailing
            )
        )
        .clipShape(RoundedRectangle(cornerRadius: DS.R.xl, style: .continuous))
        .glassBorder(DS.R.xl)
    }
}

private struct SessionTile: View {
    let session: Session
    let index: Int

    private var statusMode: StatusPill.Mode {
        if session.working { return .thinking }
        return session.active ? .active : .idle
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                Text(String(format: "%02d", index + 1))
                    .font(DS.mono(10, .semibold))
                    .foregroundStyle(.tertiary)
                Spacer()
                if session.unread > 0 {
                    UnreadBadge(count: session.unread)
                }
            }
            Text(session.name)
                .font(DS.display(16, .semibold))
                .foregroundStyle(.primary)
                .lineLimit(2)
                .multilineTextAlignment(.leading)
                .frame(maxWidth: .infinity, alignment: .leading)
            StatusPill(mode: statusMode, compact: true)
        }
        .padding(14)
        .frame(maxWidth: .infinity, minHeight: 96, alignment: .topLeading)
        .background(DS.Canvas.card)
        .clipShape(RoundedRectangle(cornerRadius: DS.R.lg, style: .continuous))
        .glassBorder(DS.R.lg)
    }
}

// Red count badge for finished-task notifications the user hasn't viewed.
private struct UnreadBadge: View {
    let count: Int
    var body: some View {
        Text(count > 9 ? "9+" : "\(count)")
            .font(DS.micro(9, .bold))
            .foregroundStyle(.white)
            .padding(.horizontal, 5)
            .padding(.vertical, 2)
            .background(DS.Ink.rose)
            .clipShape(Capsule())
    }
}

// MARK: - Press-down button style

struct PressDown: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .scaleEffect(configuration.isPressed ? 0.97 : 1.0)
            .opacity(configuration.isPressed ? 0.92 : 1.0)
            .animation(DS.snappy, value: configuration.isPressed)
    }
}

// MARK: - Terminal detail

struct TerminalDetailView: View {
    let sessionName: String
    @StateObject private var terminalVM = TerminalViewModel()
    @State private var inputText = ""
    @FocusState private var inputFocused: Bool

    var body: some View {
        ScrollViewReader { proxy in
            ScrollView {
                Text(ANSI.clean(terminalVM.content))
                    .font(.system(size: 12, weight: .regular))
                    .foregroundStyle(DS.Canvas.phosphor)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 10)
                    .textSelection(.enabled)
                    .id("content")
            }
            .background(DS.Canvas.terminal)
            .clipShape(RoundedRectangle(cornerRadius: DS.R.md, style: .continuous))
            .glassBorder(DS.R.md)
            .padding(.horizontal, 12)
            .padding(.top, 10)
            .safeAreaInset(edge: .bottom) { inputBar }
            .onChange(of: terminalVM.content) { _ in
                withAnimation(.easeOut(duration: 0.1)) {
                    proxy.scrollTo("content", anchor: .bottom)
                }
            }
        }
        .navigationTitle(sessionName)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .principal) {
                HStack(spacing: 6) {
                    Circle()
                        .fill(terminalVM.isConnected ? DS.Ink.mint : DS.Ink.zinc)
                        .frame(width: 6, height: 6)
                        .breathing(terminalVM.isConnected)
                    Text(sessionName)
                        .font(DS.mono(11, .semibold))
                        .foregroundStyle(.primary)
                        .lineLimit(1)
                }
            }
            ToolbarItem(placement: .topBarTrailing) {
                Button {
                    Task { await terminalVM.refresh() }
                } label: {
                    Image(systemName: "arrow.clockwise")
                        .font(.system(size: 14, weight: .medium))
                        .foregroundStyle(.secondary)
                        .rotationEffect(terminalVM.isRefreshing ? .degrees(360) : .zero)
                        .animation(
                            terminalVM.isRefreshing
                                ? .linear(duration: 0.8).repeatForever(autoreverses: false)
                                : .default,
                            value: terminalVM.isRefreshing
                        )
                }
                .disabled(terminalVM.isRefreshing)
                .accessibilityLabel("Refresh")
            }
            ToolbarItemGroup(placement: .keyboard) {
                Spacer()
                Button("Done") { inputFocused = false }
            }
        }
        .onAppear { terminalVM.connect(sessionName: sessionName) }
        .onDisappear { terminalVM.disconnect() }
    }

    private var inputBar: some View {
        HStack(spacing: 10) {
            TextField("send to \(sessionName)", text: $inputText)
                .font(DS.mono(13, .regular))
                .padding(.horizontal, 12)
                .padding(.vertical, 11)
                .foregroundStyle(.primary)
                .background(DS.Canvas.card)
                .clipShape(RoundedRectangle(cornerRadius: DS.R.md, style: .continuous))
                .glassBorder(DS.R.md)
                .focused($inputFocused)
                .submitLabel(.send)
                .onSubmit { sendInput() }

            Button(action: sendInput) {
                Image(systemName: "arrow.up")
                    .font(.system(size: 14, weight: .bold))
                    .foregroundStyle(.white)
                    .frame(width: 42, height: 42)
                    .background(inputText.isEmpty ? DS.Ink.zinc : DS.Ink.mint)
                    .clipShape(Circle())
                    .overlay(Circle().stroke(Color.white.opacity(0.1), lineWidth: 0.5))
            }
            .disabled(inputText.isEmpty)
            .animation(DS.snappy, value: inputText.isEmpty)
        }
        .padding(.horizontal, 12)
        .padding(.top, 10)
        .padding(.bottom, 12)
        .background(.bar)
    }

    private func sendInput() {
        let text = inputText
        guard !text.isEmpty else { return }
        inputText = ""
        // Send goes via HTTP POST /api/sessions/<name>/send, which calls
        // `tmux send-keys` on the server. send-keys handles raw-mode CR
        // semantics internally, so we send the text as-is.
        terminalVM.send(text: text)
    }
}
