import SwiftUI

/// Lists HTML/video artifacts for a session, grouped by type. Tap to preview.
struct ArtifactsView: View {
    @StateObject private var vm = ArtifactViewModel()

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                sessionPicker

                if vm.isLoading {
                    loadingState
                } else if let err = vm.error {
                    errorState(err)
                } else if vm.artifacts.isEmpty {
                    emptyState
                } else {
                    artifactList
                }
            }
            .background(DS.Canvas.app.ignoresSafeArea())
            .navigationTitle("Artifacts")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .navigationBarTrailing) {
                    Button {
                        Task { await vm.loadArtifacts() }
                    } label: {
                        Image(systemName: "arrow.clockwise")
                            .font(.system(size: 14, weight: .medium))
                            .foregroundStyle(.primary)
                    }
                    .disabled(vm.selectedSession == nil)
                }
            }
            .task {
                await vm.loadSessions()
            }
        }
    }

    private var sessionPicker: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("Session")
                .font(DS.micro(10, .semibold))
                .textCase(.uppercase)
                .foregroundStyle(.secondary)
            Picker("Session", selection: $vm.selectedSession) {
                Text("选择 session").tag(nil as String?)
                ForEach(vm.sessions) { s in
                    Text(s.name).tag(s.name as String?)
                }
            }
            .pickerStyle(.menu)
            .font(DS.text(14, .medium))
            .onChange(of: vm.selectedSession) { _ in
                Task { await vm.loadArtifacts() }
            }
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 10)
    }

    private var artifactList: some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: 10) {
                if !vm.htmlArtifacts.isEmpty {
                    sectionHeader("HTML 页面")
                    ForEach(vm.htmlArtifacts) { a in
                        NavigationLink {
                            ArtifactPreviewView(artifact: a, session: vm.selectedSession ?? "")
                        } label: {
                            ArtifactRow(artifact: a)
                        }
                    }
                }
                if !vm.videoArtifacts.isEmpty {
                    sectionHeader("视频")
                    ForEach(vm.videoArtifacts) { a in
                        NavigationLink {
                            ArtifactPreviewView(artifact: a, session: vm.selectedSession ?? "")
                        } label: {
                            ArtifactRow(artifact: a)
                        }
                    }
                }
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 12)
        }
    }

    private func sectionHeader(_ title: String) -> some View {
        Text(title)
            .font(DS.micro(10, .semibold))
            .textCase(.uppercase)
            .foregroundStyle(.secondary)
            .padding(.top, 8)
    }

    private var loadingState: some View {
        VStack(spacing: 10) {
            ProgressView()
            Text("扫描中…")
                .font(DS.text(13))
                .foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private func errorState(_ msg: String) -> some View {
        VStack(spacing: 10) {
            Image(systemName: "exclamationmark.triangle")
                .font(.system(size: 28, weight: .light))
                .foregroundStyle(DS.Ink.amber)
            Text(msg)
                .font(DS.text(13))
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
        }
        .padding(.horizontal, 32)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private var emptyState: some View {
        VStack(spacing: 14) {
            ZStack {
                Circle()
                    .stroke(DS.Ink.mint.opacity(0.18), lineWidth: 1)
                    .frame(width: 76, height: 76)
                Image(systemName: "doc.richtext")
                    .font(.system(size: 26, weight: .light))
                    .foregroundStyle(DS.Ink.mint)
            }
            VStack(spacing: 4) {
                Text("No artifacts found")
                    .font(DS.display(15, .semibold))
                    .foregroundStyle(.primary)
                Text("AI 生成的 HTML/视频会出现在这里。\n扫描 dist / output / artifacts 等目录。")
                    .font(DS.text(12))
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .padding(.horizontal, 32)
    }
}

private struct ArtifactRow: View {
    let artifact: Artifact

    var body: some View {
        HStack(spacing: 12) {
            Image(systemName: artifact.isHTML ? "globe" : "film")
                .font(.system(size: 16, weight: .medium))
                .foregroundStyle(artifact.isHTML ? DS.Ink.mint : DS.Ink.amber)
                .frame(width: 32)
            VStack(alignment: .leading, spacing: 3) {
                Text(artifact.name)
                    .font(DS.text(14, .medium))
                    .foregroundStyle(.primary)
                    .lineLimit(1)
                Text("\(formatSize(artifact.size)) · \(formatDate(artifact.mtime))")
                    .font(DS.mono(10, .regular))
                    .foregroundStyle(.secondary)
            }
            Spacer()
            Image(systemName: "chevron.right")
                .font(.system(size: 11, weight: .semibold))
                .foregroundStyle(.tertiary)
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 12)
        .background(DS.Canvas.card)
        .clipShape(RoundedRectangle(cornerRadius: DS.R.md, style: .continuous))
        .glassBorder(DS.R.md)
    }

    private func formatSize(_ bytes: Int64) -> String {
        let f = ByteCountFormatter()
        f.allowedUnits = [.useKB, .useMB]
        f.countStyle = .file
        return f.string(fromByteCount: bytes)
    }

    private func formatDate(_ ts: Int64) -> String {
        let d = Date(timeIntervalSince1970: TimeInterval(ts))
        let f = DateFormatter()
        f.dateFormat = "MM-dd HH:mm"
        return f.string(from: d)
    }
}

@MainActor
final class ArtifactViewModel: ObservableObject {
    @Published var sessions: [Session] = []
    @Published var selectedSession: String?
    @Published private(set) var artifacts: [Artifact] = []
    @Published private(set) var isLoading = false
    @Published var error: String?

    private let api = APIClient.shared

    var htmlArtifacts: [Artifact] { artifacts.filter { $0.isHTML } }
    var videoArtifacts: [Artifact] { artifacts.filter { $0.isVideo } }

    func loadSessions() async {
        do {
            sessions = try await api.fetchSessions()
            if selectedSession == nil { selectedSession = sessions.first?.name }
        } catch {}
    }

    func loadArtifacts() async {
        guard let session = selectedSession else { return }
        isLoading = true
        error = nil
        do {
            artifacts = try await api.fetchArtifacts(session: session)
        } catch let e as APIClientError {
            self.error = e.errorDescription
            artifacts = []
        } catch {
            self.error = error.localizedDescription
            artifacts = []
        }
        isLoading = false
    }
}
