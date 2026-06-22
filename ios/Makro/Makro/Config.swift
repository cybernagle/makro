import Foundation
import CryptoKit
import Security

class Config: ObservableObject {
    static let shared = Config()

    private enum Key {
        static let serverURL = "makro_server_url"
        static let password = "makro_password"
        static let azureRegion = "azure_speech_region"
        static let azureKey = "azure_speech_key"
        static let commitPhrases = "makro_commit_phrases"
        static let vadEnabled = "makro_vad_enabled"
        static let vadThreshold = "makro_vad_threshold"
    }

    @Published var serverURL: String
    @Published var password: String
    @Published var azureRegion: String
    @Published var azureKey: String
    /// Comma-separated结束语 the user says to submit a voice turn, e.g. "请发送".
    /// Matched at the trailing edge of a recognized segment in call mode.
    @Published var commitPhrases: String
    /// When true, call mode uses local VAD + a push-stream recognizer that only
    /// consumes cloud STT quota while real speech is detected. When false, call
    /// mode falls back to the legacy always-on recognizer (silence-based send).
    @Published var vadEnabled: Bool
    /// Normalized RMS energy 0...1 above which a mic frame counts as speech.
    @Published var vadThreshold: Double

    private init() {
        serverURL = UserDefaults.standard.string(forKey: Key.serverURL)
            ?? "https://47.117.13.195:39222"
        // Password lives in the Keychain (encrypted, excluded from backups),
        // not UserDefaults. Migrate once from the legacy UserDefaults value so
        // existing installs keep working without re-entering it.
        if let stored = KeychainHelper.get(Key.password) {
            password = stored
        } else if let legacy = UserDefaults.standard.string(forKey: Key.password), !legacy.isEmpty {
            KeychainHelper.set(legacy, for: Key.password)
            UserDefaults.standard.removeObject(forKey: Key.password)
            password = legacy
        } else {
            password = ""
        }
        azureRegion = UserDefaults.standard.string(forKey: Key.azureRegion) ?? "eastasia"
        azureKey = UserDefaults.standard.string(forKey: Key.azureKey) ?? ""
        commitPhrases = UserDefaults.standard.string(forKey: Key.commitPhrases)
            ?? "请发送,我说完了,OK,好,就这样,可以了"
        // object(forKey:) so the first-launch default is `true`; bool(forKey:)
        // returns false for an unset key, which would wrongly disable VAD.
        vadEnabled = (UserDefaults.standard.object(forKey: Key.vadEnabled) as? Bool) ?? true
        vadThreshold = (UserDefaults.standard.object(forKey: Key.vadThreshold) as? Double) ?? 0.02
    }

    func save() {
        UserDefaults.standard.set(serverURL, forKey: Key.serverURL)
        if password.isEmpty {
            KeychainHelper.remove(Key.password)
        } else {
            KeychainHelper.set(password, for: Key.password)
        }
        UserDefaults.standard.set(azureRegion, forKey: Key.azureRegion)
        UserDefaults.standard.set(azureKey, forKey: Key.azureKey)
        UserDefaults.standard.set(commitPhrases, forKey: Key.commitPhrases)
        UserDefaults.standard.set(vadEnabled, forKey: Key.vadEnabled)
        UserDefaults.standard.set(vadThreshold, forKey: Key.vadThreshold)
    }

    /// Normalized list of结束语句 parsed from `commitPhrases`. Whitespace trimmed,
    /// empties dropped. Case-folding is left to the CommitPhraseDetector so callers
    /// always see the phrases as the user typed them.
    var commitPhraseList: [String] {
        commitPhrases
            .split(separator: ",")
            .map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { !$0.isEmpty }
    }

    var httpBaseURL: URL {
        let urlString = serverURL
            .replacingOccurrences(of: "wss://", with: "https://")
            .replacingOccurrences(of: "ws://", with: "http://")
        return URL(string: urlString)!
    }

    var chatWSURL: URL {
        let ws = serverURL
            .replacingOccurrences(of: "https://", with: "wss://")
            .replacingOccurrences(of: "http://", with: "ws://")
        var url = URL(string: "\(ws)/ws/chat")!
        if !password.isEmpty {
            url = URL(string: "\(url.absoluteString)?token=\(password.addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed) ?? password)")!
        }
        return url
    }

    func terminalWSURL(for sessionName: String, cols: Int = 80, rows: Int = 24) -> URL {
        let ws = serverURL
            .replacingOccurrences(of: "https://", with: "wss://")
            .replacingOccurrences(of: "http://", with: "ws://")
        let encoded = sessionName.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? sessionName
        var url = URL(string: "\(ws)/ws/xterm/\(encoded)?cols=\(cols)&rows=\(rows)")!
        if !password.isEmpty {
            url = URL(string: "\(url.absoluteString)&token=\(password.addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed) ?? password)")!
        }
        return url
    }

    /// Snapshot-mode terminal WS. Server pushes periodic tmux capture-pane
    /// output as JSON frames. Used by iOS for cursor-residue-free rendering.
    func snapshotWSURL(for sessionName: String) -> URL {
        let ws = serverURL
            .replacingOccurrences(of: "https://", with: "wss://")
            .replacingOccurrences(of: "http://", with: "ws://")
        let encoded = sessionName.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? sessionName
        var url = URL(string: "\(ws)/ws/snapshot/\(encoded)")!
        if !password.isEmpty {
            url = URL(string: "\(url.absoluteString)?token=\(password.addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed) ?? password)")!
        }
        return url
    }

    /// HTTP endpoint for sending keystrokes/text to a tmux session.
    func sessionSendURL(for sessionName: String) -> URL {
        let encoded = sessionName.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? sessionName
        return URL(string: "\(httpBaseURL.absoluteString)/api/sessions/\(encoded)/send")!
    }

    /// HTTP endpoint for one-shot pane capture (alternative to snapshot WS).
    func sessionCaptureURL(for sessionName: String) -> URL {
        let encoded = sessionName.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? sessionName
        return URL(string: "\(httpBaseURL.absoluteString)/api/sessions/\(encoded)/capture")!
    }

    /// Value for the `Authorization: Bearer` header.
    var bearerToken: String { "Bearer \(password)" }

    static func handleTLSChallenge(
        _ challenge: URLAuthenticationChallenge,
        completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void
    ) {
        guard challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust,
              let serverTrust = challenge.protectionSpace.serverTrust else {
            completionHandler(.performDefaultHandling, nil)
            return
        }
        let host = challenge.protectionSpace.host
        let expected = URL(string: shared.serverURL)?.host ?? ""
        // Apply the trust override ONLY for the configured server host or
        // loopback. Every other host gets the system default evaluation — no
        // blanket "trust any cert" anymore.
        guard host == expected || host == "127.0.0.1" || host == "localhost" else {
            completionHandler(.performDefaultHandling, nil)
            return
        }
        // TOFU pinning: the server uses a self-signed cert, so chain validation
        // can't distinguish it from an attacker's cert. Instead we pin the
        // SHA-256 of the leaf cert's public key on first connect and require a
        // match thereafter — a MITM presenting a different key is rejected.
        // (The very first connection is trusted; if you rotate the server
        // certificate, call resetServerTrustPin(forHost:).)
        if let pin = pinnedPubKeyHex(forHost: host) {
            if let leaf = leafPubKeyHex(serverTrust), leaf == pin {
                completionHandler(.useCredential, URLCredential(trust: serverTrust))
            } else {
                completionHandler(.cancelAuthenticationChallenge, nil)
            }
            return
        }
        if let leaf = leafPubKeyHex(serverTrust) {
            setPinnedPubKeyHex(leaf, forHost: host)
            completionHandler(.useCredential, URLCredential(trust: serverTrust))
            return
        }
        completionHandler(.cancelAuthenticationChallenge, nil)
    }

    // MARK: - TLS pinning (TOFU)

    private static let pinDefaultsKey = "makro_tls_pin_"

    /// SHA-256 (hex) of the leaf certificate's public key, or nil if it can't
    /// be extracted.
    private static func leafPubKeyHex(_ trust: SecTrust) -> String? {
        guard let chain = SecTrustCopyCertificateChain(trust) as? [SecCertificate],
              let leaf = chain.first,
              let key = SecCertificateCopyKey(leaf),
              let keyData = SecKeyCopyExternalRepresentation(key, nil) as Data? else { return nil }
        return SHA256.hash(data: keyData).map { String(format: "%02x", $0) }.joined()
    }

    private static func pinnedPubKeyHex(forHost host: String) -> String? {
        UserDefaults.standard.string(forKey: pinDefaultsKey + host)
    }

    private static func setPinnedPubKeyHex(_ hex: String, forHost host: String) {
        UserDefaults.standard.set(hex, forKey: pinDefaultsKey + host)
    }

    /// Clear the pinned server cert key. Call after rotating the server's
    /// self-signed certificate, otherwise TOFU will reject the new key as a
    /// presumed MITM. Pass nil to clear pins for all hosts.
    static func resetServerTrustPin(forHost host: String? = nil) {
        if let host = host {
            UserDefaults.standard.removeObject(forKey: pinDefaultsKey + host)
        } else {
            for key in UserDefaults.standard.dictionaryRepresentation().keys
            where key.hasPrefix(pinDefaultsKey) {
                UserDefaults.standard.removeObject(forKey: key)
            }
        }
    }
}
