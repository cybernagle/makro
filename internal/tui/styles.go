package tui

import (
	"charm.land/lipgloss/v2"
)

var (
	// Base styles.
	borderStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240"))

	focusedBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("86")) // cyan-green when focused

	// Chat pane.
	chatTitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("86"))

	chatInputStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("254"))

	userMsgStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("86"))

	assistantMsgStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("254"))

	systemMsgStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("243"))

	// Viewer pane.
	viewerTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("213")) // pink

	viewerContentStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("252"))

	// Status bar.
	statusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("243"))
)

// PaneStyles returns styles for a pane based on focus state.
func PaneStyles(focused bool) (lipgloss.Style, lipgloss.Style) {
	if focused {
		return focusedBorderStyle, chatTitleStyle
	}
	return borderStyle, chatTitleStyle
}
