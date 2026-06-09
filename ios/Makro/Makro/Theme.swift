import SwiftUI

// MARK: - Design Tokens
enum DS {
    enum Ink {
        static let mint = Color(red: 0.36, green: 0.74, blue: 0.62)
        static let mintDeep = Color(red: 0.22, green: 0.56, blue: 0.46)
        static let amber = Color(red: 0.92, green: 0.70, blue: 0.36)
        static let rose = Color(red: 0.87, green: 0.45, blue: 0.51)
        static let zinc = Color(red: 0.55, green: 0.57, blue: 0.62)
    }

    enum Canvas {
        // Light-mode approximations of iOS grouped backgrounds (RGB tuned)
        static let app = Color(red: 0.94, green: 0.94, blue: 0.96)
        static let card = Color(red: 1.0, green: 1.0, blue: 1.0)
        static let inset = Color(red: 0.97, green: 0.97, blue: 0.98)
        static let terminal = Color(red: 0.06, green: 0.07, blue: 0.08)
        static let phosphor = Color(red: 0.62, green: 0.88, blue: 0.71)
    }

    static func display(_ size: CGFloat = 28, _ weight: Font.Weight = .semibold) -> Font {
        .system(size: size, weight: weight, design: .rounded)
    }
    static func text(_ size: CGFloat = 15, _ weight: Font.Weight = .regular) -> Font {
        .system(size: size, weight: weight, design: .default)
    }
    static func mono(_ size: CGFloat = 13, _ weight: Font.Weight = .medium) -> Font {
        .system(size: size, weight: weight, design: .monospaced)
    }
    static func micro(_ size: CGFloat = 10, _ weight: Font.Weight = .semibold) -> Font {
        .system(size: size, weight: weight, design: .rounded)
    }

    static let spring = Animation.spring(response: 0.42, dampingFraction: 0.76)
    static let snappy = Animation.spring(response: 0.28, dampingFraction: 0.85)

    enum R {
        static let sm: CGFloat = 8
        static let md: CGFloat = 12
        static let lg: CGFloat = 18
        static let xl: CGFloat = 24
    }
}

// MARK: - Breathing (perpetual micro-interaction)
private struct Breathing: ViewModifier {
    @State private var alive = false
    let active: Bool
    func body(content: Content) -> some View {
        content
            .scaleEffect(active && alive ? 1.15 : 0.88)
            .opacity(active && alive ? 1.0 : 0.5)
            .animation(active ? .easeInOut(duration: 1.4).repeatForever(autoreverses: true) : .default, value: alive)
            .onAppear { alive = true }
    }
}

extension View {
    func breathing(_ active: Bool = true) -> some View { modifier(Breathing(active: active)) }

    func glassBorder(_ radius: CGFloat = DS.R.lg) -> some View {
        overlay(
            RoundedRectangle(cornerRadius: radius, style: .continuous)
                .stroke(Color.primary.opacity(0.06), lineWidth: 0.5)
        )
    }

    func innerHighlight(_ radius: CGFloat = DS.R.lg) -> some View {
        overlay(
            RoundedRectangle(cornerRadius: radius, style: .continuous)
                .stroke(Color.white.opacity(0.08), lineWidth: 1)
                .blur(radius: 0.5)
                .mask(RoundedRectangle(cornerRadius: radius, style: .continuous).inset(by: 0.5))
        )
    }
}

// MARK: - Status pill
struct StatusPill: View {
    enum Mode { case active, idle, thinking, error, connecting
        var tint: Color {
            switch self {
            case .active: return DS.Ink.mint
            case .idle: return DS.Ink.zinc
            case .thinking: return DS.Ink.amber
            case .error: return DS.Ink.rose
            case .connecting: return DS.Ink.amber
            }
        }
        var label: String {
            switch self {
            case .active: return "live"
            case .idle: return "idle"
            case .thinking: return "thinking"
            case .error: return "error"
            case .connecting: return "linking"
            }
        }
    }
    let mode: Mode
    var compact: Bool = false

    var body: some View {
        HStack(spacing: 5) {
            Circle()
                .fill(mode.tint)
                .frame(width: 6, height: 6)
                .breathing(mode == .active || mode == .thinking || mode == .connecting)
            Text(mode.label)
                .font(DS.micro(compact ? 9 : 10))
                .textCase(.uppercase)
                .foregroundStyle(mode.tint)
        }
        .padding(.horizontal, 8)
        .padding(.vertical, 4)
        .background(mode.tint.opacity(0.12))
        .clipShape(Capsule())
    }
}
