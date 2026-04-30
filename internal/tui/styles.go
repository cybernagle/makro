package tui

import (
	"charm.land/lipgloss/v2"
)

// Color palette — neutral zinc base with a single teal accent.
var (
	colorBase      = lipgloss.Color("252") // light text
	colorDim       = lipgloss.Color("243") // muted text
	colorAccent    = lipgloss.Color("37")  // teal — borders, user messages, focus
	colorSecondary = lipgloss.Color("180") // warm gold — viewer title
	colorError     = lipgloss.Color("167") // soft red
	colorSuccess   = lipgloss.Color("142") // muted green
	colorPaneBg    = lipgloss.Color("234") // dark bg for panes
)

var (
	borderStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240")).
			Background(colorPaneBg)

	focusedBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorAccent).
				Background(colorPaneBg)

	chatTitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorAccent)

	viewerTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorSecondary)

	viewerContentStyle = lipgloss.NewStyle().
				Foreground(colorBase)

	chatInputStyle = lipgloss.NewStyle().
			Foreground(colorBase)

	userMsgStyle = lipgloss.NewStyle().
			Foreground(colorAccent).
			Bold(true)

	assistantMsgStyle = lipgloss.NewStyle().
				Foreground(colorBase)

	systemMsgStyle = lipgloss.NewStyle().
			Foreground(colorDim)

	toolCallStyle = lipgloss.NewStyle().
			Foreground(colorSecondary).
			Italic(true)

	statusStyle = lipgloss.NewStyle().
			Foreground(colorDim)

	statusBarStyle = lipgloss.NewStyle().
			Foreground(colorDim).
			Background(lipgloss.Color("236"))
)

// PaneStyles returns (border, title) styles based on focus state.
func PaneStyles(focused bool) (lipgloss.Style, lipgloss.Style) {
	if focused {
		return focusedBorderStyle, chatTitleStyle
	}
	return borderStyle, viewerTitleStyle
}
