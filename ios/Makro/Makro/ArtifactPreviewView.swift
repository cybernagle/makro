import SwiftUI
import WebKit
import AVKit

/// Previews a single artifact. HTML renders in a WKWebView loaded from local
/// string data; video downloads then plays via AVPlayer. Both load locally to
/// sidestep the self-signed TLS cert (WKWebView/AVPlayer don't share the
/// URLSession's pinned trust, so remote fetch would fail cert validation).
struct ArtifactPreviewView: View {
    let artifact: Artifact
    let session: String

    @State private var loadState: LoadState = .loading

    enum LoadState: Equatable {
        case loading
        case htmlString(String)
        case videoURL(URL)
        case failed(String)
    }

    var body: some View {
        Group {
            switch loadState {
            case .loading:
                ProgressView("加载中…")
            case .htmlString(let html):
                HTMLPreviewView(html: html)
            case .videoURL(let url):
                VideoPreviewView(url: url)
            case .failed(let msg):
                VStack(spacing: 10) {
                    Image(systemName: "exclamationmark.triangle")
                        .font(.system(size: 28))
                        .foregroundStyle(DS.Ink.amber)
                    Text(msg)
                        .font(DS.text(13))
                        .foregroundStyle(.secondary)
                        .multilineTextAlignment(.center)
                }
                .padding(.horizontal, 32)
            }
        }
        .navigationTitle(artifact.name)
        .navigationBarTitleDisplayMode(.inline)
        .task { await loadContent() }
    }

    private func loadContent() async {
        do {
            let data = try await APIClient.shared.fetchArtifactContent(session: session, path: artifact.path)
            if artifact.isHTML {
                let html = String(data: data, encoding: .utf8) ?? ""
                await MainActor.run { loadState = .htmlString(html) }
            } else {
                // Write to a temp file for AVPlayer (it needs a file URL, not
                // raw Data, for seekable playback).
                let tmp = FileManager.default.temporaryDirectory
                    .appendingPathComponent(artifact.name)
                try data.write(to: tmp)
                await MainActor.run { loadState = .videoURL(tmp) }
            }
        } catch {
            await MainActor.run { loadState = .failed(error.localizedDescription) }
        }
    }
}

// MARK: - HTML preview

/// Wraps WKWebView in SwiftUI. Loads HTML from a string so no remote request
/// is made — the self-signed cert never comes into play.
struct HTMLPreviewView: UIViewRepresentable {
    let html: String

    func makeUIView(context: Context) -> WKWebView {
        let cfg = WKWebViewConfiguration()
        cfg.allowsInlineMediaPlayback = true
        let view = WKWebView(frame: .zero, configuration: cfg)
        view.loadHTMLString(html, baseURL: nil)
        return view
    }

    func updateUIView(_ uiView: WKWebView, context: Context) {
        // Reload only if the HTML actually changed.
        if context.coordinator.lastHTML != html {
            uiView.loadHTMLString(html, baseURL: nil)
            context.coordinator.lastHTML = html
        }
    }

    func makeCoordinator() -> Coordinator { Coordinator() }

    final class Coordinator {
        var lastHTML: String?
    }
}

// MARK: - Video preview

/// Plays a local video file with AVPlayer. Local file URL avoids the
/// self-signed TLS issue entirely. The player is created on first appear so
/// AVPlayerItem is bound to the resolved file URL.
struct VideoPreviewView: View {
    let url: URL
    @State private var player: AVPlayer?

    var body: some View {
        Group {
            if let player {
                VideoPlayer(player: player)
            } else {
                ProgressView("准备播放…")
            }
        }
        .onAppear {
            if player == nil {
                let p = AVPlayer(url: url)
                player = p
                p.play()
            }
        }
        .onDisappear {
            player?.pause()
        }
    }
}
