import SwiftUI

struct KanbanView: View {
    @StateObject private var vm = KanbanViewModel()
    @State private var showAddSheet = false
    @State private var appeared = false

    private var totalCount: Int { vm.todoTasks.count + vm.inProgressTasks.count + vm.doneTasks.count }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    ColumnSection(title: "Todo", count: vm.todoTasks.count, tint: DS.Ink.zinc, tasks: vm.todoTasks, vm: vm)
                    ColumnSection(title: "In Progress", count: vm.inProgressTasks.count, tint: DS.Ink.amber, tasks: vm.inProgressTasks, vm: vm)
                    ColumnSection(title: "Done", count: vm.doneTasks.count, tint: DS.Ink.mint, tasks: vm.doneTasks, vm: vm)
                }
                .padding(.horizontal, 16)
                .padding(.top, 12)
                .padding(.bottom, 24)
                .opacity(appeared ? 1 : 0)
                .offset(y: appeared ? 0 : 8)
                .animation(DS.spring, value: appeared)
            }
            .background(DS.Canvas.app.ignoresSafeArea())
            .navigationTitle("")
            .toolbar {
                ToolbarItem(placement: .principal) {
                    HStack(spacing: 6) {
                        Text("Tasks")
                            .font(DS.display(18, .semibold))
                            .tracking(-0.3)
                            .foregroundStyle(.primary)
                        Text("\(totalCount)")
                            .font(DS.mono(13, .semibold))
                            .foregroundStyle(.tertiary)
                            .padding(.horizontal, 7)
                            .padding(.vertical, 2)
                            .background(DS.Canvas.inset)
                            .clipShape(Capsule())
                    }
                }
                ToolbarItem(placement: .navigationBarTrailing) {
                    Button { showAddSheet = true } label: {
                        Image(systemName: "plus")
                            .font(.system(size: 15, weight: .semibold))
                            .foregroundStyle(DS.Ink.mint)
                    }
                }
            }
            .sheet(isPresented: $showAddSheet) {
                AddTaskSheet(vm: vm)
            }
            .task { await vm.loadTasks(); appeared = true }
            .refreshable { await vm.loadTasks() }
        }
    }
}

// MARK: - Section (collapsible, full-width)

private struct ColumnSection: View {
    let title: String
    let count: Int
    let tint: Color
    let tasks: [KanbanTask]
    @ObservedObject var vm: KanbanViewModel
    @State private var collapsed = false

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Button {
                withAnimation(DS.snappy) { collapsed.toggle() }
            } label: {
                HStack(spacing: 8) {
                    Circle()
                        .fill(tint)
                        .frame(width: 6, height: 6)
                    Text(title.uppercased())
                        .font(DS.micro(10, .semibold))
                        .foregroundStyle(.secondary)
                    Spacer()
                    Text(String(format: "%02d", count))
                        .font(DS.mono(11, .semibold))
                        .foregroundStyle(tint)
                        .padding(.horizontal, 8)
                        .padding(.vertical, 2)
                        .background(tint.opacity(0.12))
                        .clipShape(Capsule())
                    Image(systemName: collapsed ? "chevron.right" : "chevron.down")
                        .font(.system(size: 10, weight: .bold))
                        .foregroundStyle(.tertiary)
                }
            }
            .buttonStyle(.plain)

            if !collapsed {
                if tasks.isEmpty {
                    Text("—")
                        .font(DS.mono(14))
                        .foregroundStyle(.tertiary)
                        .frame(maxWidth: .infinity, minHeight: 48)
                        .background(DS.Canvas.inset.opacity(0.4))
                        .clipShape(RoundedRectangle(cornerRadius: DS.R.md, style: .continuous))
                } else {
                    LazyVStack(spacing: 8) {
                        ForEach(Array(tasks.enumerated()), id: \.element.id) { idx, task in
                            TaskCard(task: task, index: idx, vm: vm)
                        }
                    }
                    .transition(.opacity.combined(with: .scale(scale: 0.98, anchor: .top)))
                }
            }
        }
        .padding(14)
        .background(DS.Canvas.card.opacity(0.5))
        .clipShape(RoundedRectangle(cornerRadius: DS.R.lg, style: .continuous))
        .glassBorder(DS.R.lg)
    }
}

// MARK: - Task card

private struct TaskCard: View {
    let task: KanbanTask
    let index: Int
    @ObservedObject var vm: KanbanViewModel

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .top, spacing: 8) {
                Text(String(format: "%02d", index + 1))
                    .font(DS.mono(10, .semibold))
                    .foregroundStyle(.tertiary)
                    .padding(.top, 2)
                Text(task.title)
                    .font(DS.display(15, .semibold))
                    .foregroundStyle(.primary)
                    .lineLimit(2)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }

            if !task.content.isEmpty {
                Text(task.content)
                    .font(DS.mono(11, .regular))
                    .foregroundStyle(.secondary)
                    .lineLimit(4)
                    .padding(.leading, 22)
            }
        }
        .padding(12)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(DS.Canvas.card)
        .clipShape(RoundedRectangle(cornerRadius: DS.R.md, style: .continuous))
        .glassBorder(DS.R.md)
        .contextMenu {
            Button(role: .destructive) {
                Task { await vm.deleteTask(id: task.id) }
            } label: {
                Label("Delete", systemImage: "trash")
            }
        }
    }
}

// MARK: - Add task sheet

private struct AddTaskSheet: View {
    @ObservedObject var vm: KanbanViewModel
    @State private var title = ""
    @State private var content = ""
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 18) {
                    VStack(alignment: .leading, spacing: 6) {
                        Text("TITLE")
                            .font(DS.micro(10, .semibold))
                            .foregroundStyle(.secondary)
                        TextField("What needs doing?", text: $title)
                            .font(DS.display(16, .regular))
                            .padding(.horizontal, 14)
                            .padding(.vertical, 12)
                            .background(DS.Canvas.inset)
                            .clipShape(RoundedRectangle(cornerRadius: DS.R.md, style: .continuous))
                            .glassBorder(DS.R.md)
                    }

                    VStack(alignment: .leading, spacing: 6) {
                        Text("DETAILS")
                            .font(DS.micro(10, .semibold))
                            .foregroundStyle(.secondary)
                        TextField("Optional context", text: $content, axis: .vertical)
                            .font(DS.mono(13, .regular))
                            .lineLimit(3...6)
                            .padding(.horizontal, 14)
                            .padding(.vertical, 12)
                            .background(DS.Canvas.inset)
                            .clipShape(RoundedRectangle(cornerRadius: DS.R.md, style: .continuous))
                            .glassBorder(DS.R.md)
                    }
                }
                .padding(16)
            }
            .background(DS.Canvas.app.ignoresSafeArea())
            .navigationTitle("New Task")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .navigationBarLeading) {
                    Button("Cancel") { dismiss() }
                        .foregroundStyle(.secondary)
                }
                ToolbarItem(placement: .navigationBarTrailing) {
                    Button {
                        guard !title.isEmpty else { return }
                        Task {
                            await vm.createTask(title: title, content: content)
                            dismiss()
                        }
                    } label: {
                        Text("Create")
                            .font(DS.text(15, .semibold))
                            .foregroundStyle(DS.Ink.mint)
                            .opacity(title.isEmpty ? 0.4 : 1.0)
                    }
                    .disabled(title.isEmpty)
                }
            }
        }
    }
}
