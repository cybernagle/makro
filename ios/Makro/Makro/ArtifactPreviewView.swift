import SwiftUI
import WebKit
import AVKit
import UIKit
import CoreImage
import CoreImage.CIFilterBuiltins

/// Previews a single artifact. HTML renders in a WKWebView loaded from local
/// string data; video downloads then plays via AVPlayer. Both load locally to
/// sidestep the self-signed TLS cert (WKWebView/AVPlayer don't share the
/// URLSession's pinned trust, so remote fetch would fail cert validation).
struct ArtifactPreviewView: View {
    let artifact: Artifact

    @State private var loadState: LoadState = .loading
    @State private var sharing = false
    @State private var shareError: String?
    @State private var showShareError = false
    @State private var sharedURL = ""
    @State private var showQR = false

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
        .toolbar {
            // Share: uploads to OSS (server-side) + pops a QR sheet with the
            // presigned URL. Mac backend must be reachable + OSS creds configured.
            ToolbarItem(placement: .topBarTrailing) {
                Button {
                    Task { await shareArtifact() }
                } label: {
                    if sharing {
                        ProgressView().controlSize(.small)
                    } else {
                        Image(systemName: "square.and.arrow.up")
                    }
                }
                .disabled(sharing)
                .accessibilityLabel("分享")
            }
        }
        .alert("分享失败", isPresented: $showShareError) {
            Button("好", role: .cancel) {}
        } message: {
            Text(shareError ?? "")
        }
        .sheet(isPresented: $showQR) {
            ShareQRView(url: sharedURL)
        }
    }

    private func loadContent() async {
        do {
            let data = try await APIClient.shared.fetchArtifactContent(session: artifact.session, path: artifact.path)
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

    /// Uploads the artifact to OSS (server-side) then pops a QR sheet with the
    /// returned presigned URL. Memoized server-side — second tap is instant.
    private func shareArtifact() async {
        sharing = true
        defer { sharing = false }
        do {
            let res = try await APIClient.shared.shareArtifact(session: artifact.session, path: artifact.path)
            await MainActor.run {
                sharedURL = res.url
                showQR = true
            }
        } catch {
            await MainActor.run {
                shareError = error.localizedDescription
                showShareError = true
            }
        }
    }
}

// MARK: - Share QR sheet

/// Shows a QR for the share URL + copy / system-share actions. The QR encodes
/// the presigned URL directly (OSS V1 signature is over the path+expires, so
/// the recipient scans → opens the exact signed link).
struct ShareQRView: View {
    let url: String
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(spacing: 20) {
                    if let qr = generateQRImage(from: url) {
                        Image(uiImage: qr)
                            .interpolation(.none)
                            .resizable()
                            .scaledToFit()
                            .frame(width: 232, height: 232)
                            .padding(16)
                            .background(Color.white)
                            .cornerRadius(14)
                            .shadow(color: .black.opacity(0.08), radius: 10, y: 4)
                    } else {
                        Image(systemName: "qrcode")
                            .font(.system(size: 64))
                            .foregroundStyle(.tertiary)
                            .frame(width: 232, height: 232)
                    }
                    Text("扫码在手机查看")
                        .font(DS.text(14, .medium))
                        .foregroundStyle(.secondary)

                    Text(url)
                        .font(.system(.caption, design: .monospaced))
                        .foregroundStyle(.tertiary)
                        .lineLimit(2)
                        .truncationMode(.middle)
                        .padding(.horizontal, 24)
                        .textSelection(.enabled)

                    HStack(spacing: 12) {
                        Button {
                            UIPasteboard.general.string = url
                        } label: {
                            Label("复制链接", systemImage: "doc.on.doc")
                                .frame(maxWidth: .infinity)
                        }
                        .buttonStyle(.bordered)
                        Button {
                            presentShareSheet(items: [url])
                        } label: {
                            Label("系统分享", systemImage: "square.and.arrow.up")
                                .frame(maxWidth: .infinity)
                        }
                        .buttonStyle(.borderedProminent)
                    }
                    .padding(.horizontal, 24)
                    .padding(.top, 8)
                }
                .padding(.vertical, 28)
            }
            .background(DS.Canvas.app.ignoresSafeArea())
            .navigationTitle("分享")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button("完成") { dismiss() }
                }
            }
        }
    }
}

/// Generates a QR UIImage for the string via CoreImage (built into iOS, no
/// external dep). correctionLevel "M" balances density + damage tolerance.
func generateQRImage(from string: String, scale: CGFloat = 8) -> UIImage? {
    let filter = CIFilter.qrCodeGenerator()
    filter.message = Data(string.utf8)
    filter.correctionLevel = "M"
    guard let output = filter.outputImage?.transformed(by: CGAffineTransform(scaleX: scale, y: scale)) else {
        return nil
    }
    let context = CIContext()
    guard let cg = context.createCGImage(output, from: output.extent) else { return nil }
    return UIImage(cgImage: cg)
}

/// Presents the system share sheet (WeChat / mail / AirDrop / copy) from the
/// topmost view controller.
func presentShareSheet(items: [Any]) {
    guard let scene = UIApplication.shared.connectedScenes.first as? UIWindowScene,
          let root = scene.windows.first?.rootViewController else { return }
    let vc = UIActivityViewController(activityItems: items, applicationActivities: nil)
    var top = root
    while let p = top.presentedViewController { top = p }
    top.present(vc, animated: true)
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
