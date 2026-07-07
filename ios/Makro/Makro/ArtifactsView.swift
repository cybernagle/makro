import SwiftUI

/// Lists HTML/video artifacts across ALL sessions by default, with a chip
/// filter (per session) and a live name search. Both filters are client-side
/// against a single full fetch, so switching is instant. Tap to preview.
struct ArtifactsView: View {
    @StateObject private var vm = ArtifactViewModel()
    @State private var appeared = false

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    statsHeader
                    filterBar
                    content
                }
                .padding(.horizontal, 16)
                .padding(.top, 6)
                .padding(.bottom, 24)
            }
            .scrollDismissesKeyboard(.immediately)
            .background(DS.Canvas.app.ignoresSafeArea())
            .navigationTitle("")
            .toolbar {
                ToolbarItem(placement: .principal) {
                    Text("Artifacts")
                        .font(DS.display(18, .semibold))
                        .tracking(-0.3)
                        .foregroundStyle(.primary)
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Button {
                        Task { await vm.loadArtifacts() }
                    } label: {
                        Image(systemName: "arrow.clockwise")
                            .font(.system(size: 14, weight: .medium))
                            .foregroundStyle(.secondary)
                            .rotationEffect(vm.isLoading ? .degrees(360) : .zero)
                            .animation(
                                vm.isLoading
                                    ? .linear(duration: 0.8).repeatForever(autoreverses: false)
                                    : .default,
                                value: vm.isLoading
                            )
                    }
                    .disabled(vm.isLoading)
                    .accessibilityLabel("Refresh")
                }
            }
            .refreshable { await vm.loadArtifacts() }
            .task {
                await vm.loadArtifacts()
                withAnimation(DS.spring) { appeared = true }
            }
        }
    }

    // MARK: - Stats header

    private var statsHeader: some View {
        HStack(alignment: .firstTextBaseline, spacing: 16) {
            stat(vm.totalCount, "total", .primary)
            Divider().frame(height: 30)
            stat(vm.htmlCount, "html", DS.Ink.mint)
            Divider().frame(height: 30)
            stat(vm.videoCount, "video", DS.Ink.amber)
            Spacer(minLength: 0)
        }
        .opacity(appeared ? 1 : 0)
        .offset(y: appeared ? 0 : 6)
        .animation(DS.spring, value: appeared)
    }

    private func stat(_ n: Int, _ label: String, _ color: Color) -> some View {
        VStack(alignment: .leading, spacing: 1) {
            Text("\(n)")
                .font(DS.mono(28, .semibold))
                .foregroundStyle(color)
                .tracking(-0.4)
                .contentTransition(.numericText())
                .animation(DS.snappy, value: n)
            Text(label)
                .font(DS.micro(9))
                .textCase(.uppercase)
                .foregroundStyle(.secondary)
        }
    }

    // MARK: - Filter bar (session chips + search)

    private var filterBar: some View {
        VStack(alignment: .leading, spacing: 10) {
            chipRow
            searchField
        }
        .opacity(appeared ? 1 : 0)
        .offset(y: appeared ? 0 : 6)
        .animation(DS.spring.delay(0.06), value: appeared)
    }

    private var chipRow: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 8) {
                SessionChip(
                    label: "All",
                    count: vm.totalCount,
                    selected: vm.selectedSession == nil,
                    color: .primary
                ) {
                    if vm.selectedSession != nil {
                        vm.selectedSession = nil
                    }
                }
                ForEach(vm.sessionCounts) { sc in
                    SessionChip(
                        label: sc.name,
                        count: sc.count,
                        selected: vm.selectedSession == sc.name,
                        color: ArtifactPalette.color(for: sc.name)
                    ) {
                        if vm.selectedSession != sc.name {
                            vm.selectedSession = sc.name
                        }
                    }
                }
            }
            .padding(.horizontal, 1)
            .padding(.vertical, 2)
        }
    }

    private var searchField: some View {
        HStack(spacing: 8) {
            Image(systemName: "magnifyingglass")
                .font(.system(size: 13, weight: .medium))
                .foregroundStyle(.secondary)
            TextField("Search by name", text: $vm.searchText)
                .font(DS.text(14, .regular))
                .foregroundStyle(.primary)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
            if !vm.searchText.isEmpty {
                Button {
                    vm.searchText = ""
                } label: {
                    Image(systemName: "xmark.circle.fill")
                        .font(.system(size: 14))
                        .foregroundStyle(.tertiary)
                }
                .transition(.opacity.combined(with: .scale))
            }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 10)
        .background(DS.Canvas.card)
        .clipShape(RoundedRectangle(cornerRadius: DS.R.md, style: .continuous))
        .glassBorder(DS.R.md)
        .animation(DS.snappy, value: vm.searchText.isEmpty)
    }

    // MARK: - Content switch

    @ViewBuilder private var content: some View {
        if vm.isLoading && vm.artifacts.isEmpty {
            skeletonList
        } else if let err = vm.error {
            stateVisual("exclamationmark.triangle", tint: DS.Ink.rose,
                        title: "出了点问题", sub: err)
        } else if vm.artifacts.isEmpty {
            stateVisual("doc.richtext", tint: DS.Ink.mint,
                        title: "No artifacts yet",
                        sub: "AI 生成的 HTML / 视频会出现在这里。\n默认列出所有 session。")
        } else if vm.filtered.isEmpty {
            stateVisual("magnifyingglass", tint: DS.Ink.zinc,
                        title: "无匹配结果",
                        sub: "试试其他关键字,或切换 session 筛选。")
        } else {
            artifactList
        }
    }

    private var artifactList: some View {
        VStack(alignment: .leading, spacing: 10) {
            if !vm.htmlFiltered.isEmpty {
                sectionHeader("HTML", count: vm.htmlFiltered.count)
                ForEach(Array(vm.htmlFiltered.enumerated()), id: \.element.id) { idx, a in
                    NavigationLink {
                        ArtifactPreviewView(artifact: a)
                    } label: {
                        ArtifactRow(artifact: a, showSession: vm.isAllSessions, index: idx)
                    }
                    .buttonStyle(PressDown())
                }
            }
            if !vm.videoFiltered.isEmpty {
                sectionHeader("Video", count: vm.videoFiltered.count)
                ForEach(Array(vm.videoFiltered.enumerated()), id: \.element.id) { idx, a in
                    NavigationLink {
                        ArtifactPreviewView(artifact: a)
                    } label: {
                        ArtifactRow(artifact: a, showSession: vm.isAllSessions, index: idx)
                    }
                    .buttonStyle(PressDown())
                }
            }
        }
        .padding(.top, 4)
    }

    private func sectionHeader(_ title: String, count: Int) -> some View {
        HStack(spacing: 6) {
            Text(title)
                .font(DS.micro(10, .semibold))
                .textCase(.uppercase)
                .foregroundStyle(.secondary)
            Text("\(count)")
                .font(DS.mono(10, .semibold))
                .foregroundStyle(.tertiary)
            Spacer()
        }
        .padding(.top, 10)
    }

    // MARK: - Loading skeleton

    private var skeletonList: some View {
        VStack(alignment: .leading, spacing: 10) {
            ForEach(0..<4, id: \.self) { _ in ArtifactRowSkeleton() }
        }
        .padding(.top, 8)
    }

    // MARK: - State visual (empty / error / search-empty share layout)

    @ViewBuilder
    private func stateVisual(_ icon: String, tint: Color, title: String, sub: String) -> some View {
        VStack(spacing: 14) {
            ZStack {
                Circle()
                    .stroke(tint.opacity(0.18), lineWidth: 1)
                    .frame(width: 88, height: 88)
                Circle()
                    .fill(tint.opacity(0.08))
                    .frame(width: 56, height: 56)
                Image(systemName: icon)
                    .font(.system(size: 22, weight: .light))
                    .foregroundStyle(tint)
            }
            VStack(spacing: 4) {
                Text(title)
                    .font(DS.display(16, .semibold))
                    .foregroundStyle(.primary)
                    .tracking(-0.2)
                Text(sub)
                    .font(DS.text(12.5))
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
            }
        }
        .frame(maxWidth: .infinity)
        .padding(.horizontal, 32)
        .padding(.top, 56)
    }
}

// MARK: - Session chip

private struct SessionChip: View {
    let label: String
    let count: Int
    let selected: Bool
    let color: Color
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack(spacing: 5) {
                Circle()
                    .fill(color)
                    .frame(width: 6, height: 6)
                    .opacity(selected ? 1 : 0.55)
                Text(label)
                    .font(DS.text(13, .semibold))
                    .foregroundStyle(selected ? color : .primary)
                Text("\(count)")
                    .font(DS.mono(11, .semibold))
                    .foregroundStyle(selected ? color : .secondary)
            }
            .padding(.horizontal, 11)
            .padding(.vertical, 7)
            .background(selected ? color.opacity(0.14) : DS.Canvas.card)
            .clipShape(Capsule())
            .overlay(
                Capsule()
                    .stroke(Color.primary.opacity(selected ? 0 : 0.06), lineWidth: 0.5)
            )
        }
        .buttonStyle(PressDown())
        .animation(DS.snappy, value: selected)
    }
}

// MARK: - Artifact row

private struct ArtifactRow: View {
    let artifact: Artifact
    var showSession: Bool = false
    var index: Int = 0
    @State private var revealed = false

    private var tint: Color { artifact.isHTML ? DS.Ink.mint : DS.Ink.amber }

    var body: some View {
        HStack(spacing: 12) {
            ZStack {
                Circle()
                    .fill(tint.opacity(0.14))
                    .frame(width: 34, height: 34)
                Image(systemName: artifact.isHTML ? "globe" : "film")
                    .font(.system(size: 14, weight: .semibold))
                    .foregroundStyle(tint)
            }
            VStack(alignment: .leading, spacing: 4) {
                HStack(spacing: 6) {
                    Text(artifact.name)
                        .font(DS.text(14, .medium))
                        .foregroundStyle(.primary)
                        .lineLimit(1)
                        .tracking(-0.1)
                    if showSession {
                        sessionBadge
                    }
                    Spacer(minLength: 0)
                }
                Text("\(formatSize(artifact.size)) · \(formatDate(artifact.mtime))")
                    .font(DS.mono(10, .regular))
                    .foregroundStyle(.secondary)
            }
            Image(systemName: "chevron.right")
                .font(.system(size: 10, weight: .bold))
                .foregroundStyle(.tertiary)
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 12)
        .background(DS.Canvas.card)
        .clipShape(RoundedRectangle(cornerRadius: DS.R.md, style: .continuous))
        .glassBorder(DS.R.md)
        .opacity(revealed ? 1 : 0)
        .offset(y: revealed ? 0 : 8)
        .onAppear {
            // Stagger reveal capped at ~8 so long lists don't drag the tail.
            withAnimation(DS.spring.delay(Double(min(index, 8)) * 0.04)) { revealed = true }
        }
    }

    private var sessionBadge: some View {
        let c = ArtifactPalette.color(for: artifact.session)
        return HStack(spacing: 4) {
            Circle().fill(c).frame(width: 5, height: 5)
            Text(artifact.session).font(DS.micro(9, .bold))
        }
        .foregroundStyle(c)
        .padding(.horizontal, 6)
        .padding(.vertical, 2)
        .background(c.opacity(0.12))
        .clipShape(Capsule())
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

// MARK: - Skeleton row

private struct ArtifactRowSkeleton: View {
    var body: some View {
        HStack(spacing: 12) {
            Circle()
                .fill(DS.Canvas.inset)
                .frame(width: 34, height: 34)
            VStack(alignment: .leading, spacing: 6) {
                RoundedRectangle(cornerRadius: 4, style: .continuous)
                    .fill(DS.Canvas.inset)
                    .frame(height: 12)
                RoundedRectangle(cornerRadius: 4, style: .continuous)
                    .fill(DS.Canvas.inset)
                    .frame(width: 130, height: 9)
            }
            Spacer()
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 12)
        .background(DS.Canvas.card)
        .clipShape(RoundedRectangle(cornerRadius: DS.R.md, style: .continuous))
        .glassBorder(DS.R.md)
        .shimmering()
    }
}

// MARK: - Session color palette (stable hash → DS 4-color palette)

private enum ArtifactPalette {
    private static let colors: [Color] = [DS.Ink.mint, DS.Ink.amber, DS.Ink.rose, DS.Ink.zinc]

    /// Stable across runs (Swift's String.hashValue is randomized per launch).
    static func color(for session: String) -> Color {
        var h = 0
        for c in session.unicodeScalars {
            h = (h &* 31) &+ Int(c.value)
        }
        return colors[abs(h % colors.count)]
    }
}

// MARK: - View model

@MainActor
final class ArtifactViewModel: ObservableObject {
    @Published var selectedSession: String? = nil
    @Published var searchText = ""
    @Published private(set) var artifacts: [Artifact] = []
    @Published private(set) var isLoading = false
    @Published var error: String?

    private let api = APIClient.shared

    // Client-side filter: session chip + name search, instant (no network).
    var filtered: [Artifact] {
        let q = searchText.trimmingCharacters(in: .whitespaces).lowercased()
        return artifacts.filter { a in
            (selectedSession == nil || a.session == selectedSession)
                && (q.isEmpty || a.name.lowercased().contains(q))
        }
    }

    var htmlFiltered: [Artifact] { filtered.filter { $0.isHTML } }
    var videoFiltered: [Artifact] { filtered.filter { $0.isVideo } }

    var totalCount: Int { artifacts.count }
    var htmlCount: Int { artifacts.filter { $0.isHTML }.count }
    var videoCount: Int { artifacts.filter { $0.isVideo }.count }

    struct SessionCount: Identifiable { let name: String; let count: Int; var id: String { name } }
    var sessionCounts: [SessionCount] {
        let groups = Dictionary(grouping: artifacts, by: \.session)
        return groups.keys.sorted().map { SessionCount(name: $0, count: groups[$0]?.count ?? 0) }
    }

    var isAllSessions: Bool { selectedSession == nil }

    func loadArtifacts() async {
        isLoading = true
        error = nil
        do {
            // Always fetch ALL sessions; filtering is client-side for instant
            // chip/search switching. Backend ?session= filter stays available.
            artifacts = try await api.fetchArtifacts(session: nil)
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
