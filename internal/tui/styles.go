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

	guardianMsgStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("184"))

	toolCallStyle = lipgloss.NewStyle().
			Foreground(colorSecondary).
			Italic(true)

	statusStyle = lipgloss.NewStyle().
			Foreground(colorDim)

	suggestionStyle = lipgloss.NewStyle().
			Foreground(colorDim)

	suggestionHighlightStyle = lipgloss.NewStyle().
					Foreground(colorAccent).
					Bold(true)
)

// BorderStyle returns the border style based on focus state.
func BorderStyle(focused bool) lipgloss.Style {
	if focused {
		return focusedBorderStyle
	}
	return borderStyle
}
