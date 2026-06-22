import Foundation
import Security

/// Thin Keychain wrapper for secret strings (server password, API keys).
///
/// Secrets here are encrypted by the iOS keychain and — because we use
/// `...ThisDeviceOnly` accessibility — are NOT included in iTunes/iCloud
/// backups, unlike UserDefaults (which stores them as a plaintext plist).
enum KeychainHelper {
    private static let service = "com.cybernagle.Makro"

    /// Accessibility chosen so secrets remain reachable from background
    /// sessions (voice-call WS, push-triggered fetches) after the first unlock,
    /// while still being device-local (no backup sync).
    private static let accessibility = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly

    static func set(_ value: String, for account: String) {
        guard let data = value.data(using: .utf8) else { return }
        let baseQuery: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ]
        // Replace any existing item for this account (upsert).
        SecItemDelete(baseQuery as CFDictionary)
        var attrs = baseQuery
        attrs[kSecValueData as String] = data
        attrs[kSecAttrAccessible as String] = accessibility
        SecItemAdd(attrs as CFDictionary, nil)
    }

    static func get(_ account: String) -> String? {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecReturnData as String: true,
            kSecMatchLimit as String: kSecMatchLimitOne,
        ]
        var item: CFTypeRef?
        guard SecItemCopyMatching(query as CFDictionary, &item) == errSecSuccess,
              let data = item as? Data else { return nil }
        return String(data: data, encoding: .utf8)
    }

    static func remove(_ account: String) {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ]
        SecItemDelete(query as CFDictionary)
    }
}
