import Foundation

@MainActor
final class KanbanViewModel: ObservableObject {
    @Published private(set) var tasks: [KanbanTask] = []
    @Published private(set) var isLoading = false

    private let api = APIClient.shared

    var todoTasks: [KanbanTask] {
        tasks.filter { $0.column == "todo" }.sorted { $0.order < $1.order }
    }
    var inProgressTasks: [KanbanTask] {
        tasks.filter { $0.column == "in-progress" }.sorted { $0.order < $1.order }
    }
    var doneTasks: [KanbanTask] {
        tasks.filter { $0.column == "done" }.sorted { $0.order < $1.order }
    }

    func loadTasks() async {
        isLoading = true
        defer { isLoading = false }
        do {
            tasks = try await api.fetchTasks()
        } catch {}
    }

    func createTask(title: String, content: String) async {
        do {
            let task = try await api.createTask(title: title, content: content)
            tasks.append(task)
        } catch {}
    }

    func moveTask(id: String, toColumn column: String) async {
        guard let idx = tasks.firstIndex(where: { $0.id == id }) else { return }
        let targetCount = tasks.filter { $0.column == column }.count
        do {
            let updated = try await api.updateTask(id: id, patch: ["column": column, "order": targetCount])
            tasks[idx] = updated
        } catch {}
    }

    func deleteTask(id: String) async {
        do {
            try await api.deleteTask(id: id)
            tasks.removeAll { $0.id == id }
        } catch {}
    }

    func sendTask(id: String, session: String) async {
        do {
            try await api.sendTask(id: id, session: session)
            if let idx = tasks.firstIndex(where: { $0.id == id }) {
                let updated = try await api.updateTask(id: id, patch: ["column": "in-progress"])
                tasks[idx] = updated
            }
        } catch {}
    }
}
