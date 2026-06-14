import UIKit
import UserNotifications

/// Bridges UIApplicationDelegate into the SwiftUI lifecycle so the app can
/// register for APNs remote notifications.
final class AppDelegate: NSObject, UIApplicationDelegate {
    func application(
        _ application: UIApplication,
        didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil
    ) -> Bool {
        let center = UNUserNotificationCenter.current()
        center.delegate = self
        center.requestAuthorization(options: [.alert, .sound, .badge]) { granted, _ in
            DispatchQueue.main.async {
                if granted {
                    UIApplication.shared.registerForRemoteNotifications()
                }
            }
        }
        return true
    }

    func application(
        _ application: UIApplication,
        didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data
    ) {
        let token = deviceToken.map { String(format: "%02x", $0) }.joined()
        print("[APNs] token registered: \(token.prefix(16))… (\(token.count) hex chars)")
        let deviceID = UIDevice.current.identifierForVendor?.uuidString ?? UUID().uuidString
        Task {
            do {
                try await APIClient.shared.registerDeviceToken(deviceID: deviceID, token: token)
                print("[APNs] token uploaded to backend")
            } catch {
                print("[APNs] token upload failed: \(error)")
            }
        }
    }

    func application(
        _ application: UIApplication,
        didFailToRegisterForRemoteNotificationsWithError error: Error
    ) {
        print("[APNs] registration FAILED: \(error.localizedDescription)")
        print("[APNs] hint: check entitlements aps-environment + provisioning profile has Push capability")
    }
}

extension AppDelegate: UNUserNotificationCenterDelegate {
    // Tapping the banner → deep-link into the relevant session.
    func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        didReceive response: UNNotificationResponse,
        withCompletionHandler completionHandler: @escaping () -> Void
    ) {
        if let session = response.notification.request.content.userInfo["session"] as? String {
            NotificationCenter.default.post(
                name: .makroOpenSession,
                object: nil,
                userInfo: ["session": session]
            )
        }
        completionHandler()
    }

    // Show banner while app is in foreground too.
    func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        willPresent notification: UNNotification,
        withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void
    ) {
        completionHandler([.banner, .sound])
    }
}
