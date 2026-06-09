import Foundation

struct Session: Codable, Identifiable, Equatable {
    let name: String
    let active: Bool
    var id: String { name }
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
