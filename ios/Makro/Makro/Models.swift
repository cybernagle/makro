import Foundation

struct Session: Codable, Identifiable, Equatable {
    let name: String
    let active: Bool
    var working: Bool
    var unread: Int
    var id: String { name }

    init(name: String, active: Bool, working: Bool = false, unread: Int = 0) {
        self.name = name
        self.active = active
        self.working = working
        self.unread = unread
    }

    private enum CodingKeys: String, CodingKey { case name, active, working, unread }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        name = try c.decode(String.self, forKey: .name)
        active = try c.decode(Bool.self, forKey: .active)
        // working/unread are omitempty on the server → decodeIfPresent for
        // backward compatibility with payloads that omit them.
        working = try c.decodeIfPresent(Bool.self, forKey: .working) ?? false
        unread = try c.decodeIfPresent(Int.self, forKey: .unread) ?? 0
    }
}

// Posted by ChatViewModel when a session_state WS event arrives, so the
// Sessions list can update working/unread in real time without polling.
extension Notification.Name {
    static let sessionStateChanged = Notification.Name("makro.sessionStateChanged")
}

struct KanbanTask: Codable, Identifiable, Equatable {
    let id: String
    let title: String
    let content: String
    let column: String
    let order: Int
    let createdAt: String?
    let updatedAt: String?
    let assignedSession: String?

    enum CodingKeys: String, CodingKey {
        case id, title, content, column, order
        case createdAt = "created_at"
        case updatedAt = "updated_at"
        case assignedSession = "assigned_session"
    }
}

struct ChatMessage: Identifiable, Equatable {
    enum Role: String, Codable {
        case user, assistant, system
    }

    let id: UUID
    let role: Role
    var text: String
    let timestamp: Date

    init(id: UUID = UUID(), role: Role, text: String, timestamp: Date = Date()) {
        self.id = id
        self.role = role
        self.text = text
        self.timestamp = timestamp
    }
}

enum ConnectionState: Equatable {
    case disconnected
    case connecting
    case connected
}
