package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// Internal message types for the TUI.

// SubmitMsg is sent when the user submits chat input.
type SubmitMsg struct{ Text string }

// OrchestratorEventMsg wraps an orchestrator event for the chat pane.
type OrchestratorEventMsg struct {
	Type     string // "text", "tool_call", "tool_result", "done"
	Content  string
	ToolName string
}

// SessionListMsg carries the current session list.
type SessionListMsg struct{ Sessions []string }

// SessionTargetMsg is sent when the user selects a session via @ autocomplete.
type SessionTargetMsg struct{ Name string }

// FocusSwitchMsg toggles pane focus.
type FocusSwitchMsg struct{}

// QuitRequestMsg is sent when the user requests quit (e.g. double Ctrl+C).
type QuitRequestMsg struct{}

// CancelRequestMsg is sent when the user cancels the current in-progress tool call chain.
type CancelRequestMsg struct{}

// AgentStatusMsg reports agent status change.
type AgentStatusMsg struct {
	Session string
	Status  string // "ready", "working", "completed", "error"
}

// GuardianEventMsg carries a guardian assessment result.
type GuardianEventMsg struct {
	Session string
	Content string
}

// tickMsg is used for periodic tmux polling.
type tickMsg struct{}

// tickCmd returns a command that produces a tickMsg after polling interval.
func tickCmd() tea.Cmd {
	return tea.Tick(500, func(time.Time) tea.Msg {
		return tickMsg{}
	})
}
