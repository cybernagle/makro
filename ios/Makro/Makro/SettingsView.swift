import SwiftUI

struct SettingsView: View {
    @StateObject private var config = Config.shared
    @State private var testResult: TestResult?
    @State private var isTesting = false
    @State private var revealPassword = false
    @State private var copied = false
    @Environment(\.dismiss) private var dismiss

    enum TestResult: Equatable {
        case success
        case failure(String)
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 18) {
                serverCard
                authCard
                testCard
                infoCard
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 14)
        }
        .background(DS.Canvas.app.ignoresSafeArea())
        .navigationTitle("Settings")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .navigationBarTrailing) {
                Button("Save") {
                    config.save()
                    dismiss()
                }
                .font(DS.text(15, .semibold))
                .foregroundStyle(DS.Ink.mint)
            }
        }
    }

    // MARK: - Server

    private var serverCard: some View {
        VStack(alignment: .leading, spacing: 10) {
            sectionLabel("Server URL")
            HStack(spacing: 10) {
                Image(systemName: "link")
                    .font(.system(size: 13, weight: .semibold))
                    .foregroundStyle(.tertiary)
                TextField("https://host:port", text: $config.serverURL)
                    .font(DS.mono(13, .regular))
                    .autocorrectionDisabled()
                    .textInputAutocapitalization(.never)
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 12)
            .background(DS.Canvas.inset)
            .clipShape(RoundedRectangle(cornerRadius: DS.R.md, style: .continuous))
            .glassBorder(DS.R.md)

            Button {
                UIPasteboard.general.string = config.serverURL
                withAnimation(DS.snappy) { copied = true }
                DispatchQueue.main.asyncAfter(deadline: .now() + 1.4) {
                    withAnimation(DS.snappy) { copied = false }
                }
            } label: {
                HStack(spacing: 6) {
                    Image(systemName: copied ? "checkmark" : "doc.on.doc")
                        .font(.system(size: 11, weight: .semibold))
                    Text(copied ? "Copied" : "Copy")
                        .font(DS.micro(10, .semibold))
                        .textCase(.uppercase)
                }
                .foregroundStyle(copied ? DS.Ink.mint : .secondary)
                .padding(.horizontal, 10)
                .padding(.vertical, 6)
                .background((copied ? DS.Ink.mint : DS.Ink.zinc).opacity(0.12))
                .clipShape(Capsule())
            }
        }
        .padding(14)
        .background(DS.Canvas.card)
        .clipShape(RoundedRectangle(cornerRadius: DS.R.lg, style: .continuous))
        .glassBorder(DS.R.lg)
    }

    // MARK: - Auth

    private var authCard: some View {
        VStack(alignment: .leading, spacing: 10) {
            sectionLabel("Password")
            HStack(spacing: 10) {
                Image(systemName: "key.fill")
                    .font(.system(size: 12, weight: .semibold))
                    .foregroundStyle(.tertiary)
                Group {
                    if revealPassword {
                        TextField("Password", text: $config.password)
                    } else {
                        SecureField("Password", text: $config.password)
                    }
                }
                .font(DS.mono(13, .regular))
                .autocorrectionDisabled()
                .textInputAutocapitalization(.never)

                Button {
                    withAnimation(DS.snappy) { revealPassword.toggle() }
                } label: {
                    Image(systemName: revealPassword ? "eye.slash" : "eye")
                        .font(.system(size: 13, weight: .semibold))
                        .foregroundStyle(.secondary)
                }
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 12)
            .background(DS.Canvas.inset)
            .clipShape(RoundedRectangle(cornerRadius: DS.R.md, style: .continuous))
            .glassBorder(DS.R.md)
        }
        .padding(14)
        .background(DS.Canvas.card)
        .clipShape(RoundedRectangle(cornerRadius: DS.R.lg, style: .continuous))
        .glassBorder(DS.R.lg)
    }

    // MARK: - Test

    private var testCard: some View {
        VStack(alignment: .leading, spacing: 12) {
            Button {
                config.save()
                testConnection()
            } label: {
                HStack {
                    if isTesting {
                        HStack(spacing: 4) {
                            ForEach(0..<3) { i in
                                Circle()
                                    .fill(.white)
                                    .frame(width: 4, height: 4)
                                    .scaleEffect(isTesting ? 1 : 0.5)
                                    .opacity(isTesting ? 1 : 0.4)
                                    .animation(
                                        .easeInOut(duration: 0.5).repeatForever().delay(Double(i) * 0.15),
                                        value: isTesting
                                    )
                            }
                        }
                    }
                    Text(isTesting ? "Testing" : "Save & Test Connection")
                        .font(DS.text(14, .semibold))
                        .foregroundStyle(.white)
                }
                .frame(maxWidth: .infinity)
                .padding(.vertical, 13)
                .background(
                    LinearGradient(
                        colors: [DS.Ink.mint, DS.Ink.mintDeep],
                        startPoint: .topLeading,
                        endPoint: .bottomTrailing
                    )
                )
                .clipShape(RoundedRectangle(cornerRadius: DS.R.md, style: .continuous))
                .overlay(
                    RoundedRectangle(cornerRadius: DS.R.md, style: .continuous)
                        .stroke(Color.white.opacity(0.12), lineWidth: 0.5)
                )
            }
            .disabled(isTesting)

            if let result = testResult {
                HStack(spacing: 8) {
                    Image(systemName: result == .success ? "checkmark.circle.fill" : "xmark.octagon.fill")
                        .font(.system(size: 13, weight: .semibold))
                        .foregroundStyle(result == .success ? DS.Ink.mint : DS.Ink.rose)
                    Text(resultText(result))
                        .font(DS.mono(11, .regular))
                        .foregroundStyle(.secondary)
                        .lineLimit(2)
                }
                .padding(.horizontal, 12)
                .padding(.vertical, 8)
                .background((result == .success ? DS.Ink.mint : DS.Ink.rose).opacity(0.08))
                .clipShape(RoundedRectangle(cornerRadius: DS.R.sm, style: .continuous))
                .transition(.opacity.combined(with: .scale))
            }
        }
        .padding(14)
        .background(DS.Canvas.card)
        .clipShape(RoundedRectangle(cornerRadius: DS.R.lg, style: .continuous))
        .glassBorder(DS.R.lg)
    }

    // MARK: - Info

    private var infoCard: some View {
        HStack(alignment: .top, spacing: 10) {
            Image(systemName: "info.circle")
                .font(.system(size: 12, weight: .semibold))
                .foregroundStyle(.tertiary)
            Text("Makro server must be running and reachable on this host.")
                .font(DS.text(12, .regular))
                .foregroundStyle(.secondary)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(14)
        .background(DS.Canvas.inset.opacity(0.4))
        .clipShape(RoundedRectangle(cornerRadius: DS.R.md, style: .continuous))
    }

    // MARK: - Helpers

    private func sectionLabel(_ text: String) -> some View {
        Text(text)
            .font(DS.micro(10, .semibold))
            .textCase(.uppercase)
            .foregroundStyle(.secondary)
    }

    private func resultText(_ result: TestResult) -> String {
        switch result {
        case .success: return "Connected successfully"
        case .failure(let msg): return msg
        }
    }

    private func testConnection() {
        isTesting = true
        testResult = nil
        Task {
            do {
                let url = config.httpBaseURL.appendingPathComponent("api/sessions")
                var request = URLRequest(url: url)
                request.timeoutInterval = 5
                if !config.password.isEmpty {
                    request.setValue("Bearer \(config.password)", forHTTPHeaderField: "Authorization")
                }
                let (_, response) = try await URLSession.shared.data(for: request)
                if let http = response as? HTTPURLResponse {
                    if http.statusCode == 200 {
                        withAnimation(DS.spring) { testResult = .success }
                    } else if http.statusCode == 401 {
                        withAnimation(DS.spring) { testResult = .failure("Wrong password") }
                    } else {
                        withAnimation(DS.spring) { testResult = .failure("Server returned \(http.statusCode)") }
                    }
                }
            } catch {
                withAnimation(DS.spring) { testResult = .failure(error.localizedDescription) }
            }
            withAnimation(DS.snappy) { isTesting = false }
        }
    }
}
