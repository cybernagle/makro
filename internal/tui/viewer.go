package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// ViewerModel renders tmux session output in the right pane.
type ViewerModel struct {
	sessions   map[string]string // session name -> output buffer
	active     string            // currently displayed session
	width      int
	height     int
	focused    bool
	scrollback int // max lines to keep per session
}

func NewViewerModel() ViewerModel {
	return ViewerModel{
		sessions:   make(map[string]string),
		scrollback: 10000,
	}
}

func (v ViewerModel) Init() tea.Cmd {
	return nil
}

func (v ViewerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width = msg.Width
		v.height = msg.Height

	case TmuxOutputMsg:
		buf := v.sessions[msg.Session]
		buf += msg.Content
		// Trim to scrollback limit.
		if lines := strings.Split(buf, "\n"); len(lines) > v.scrollback {
			buf = strings.Join(lines[len(lines)-v.scrollback:], "\n")
		}
		v.sessions[msg.Session] = buf
		// Auto-switch to session with new output.
		if v.active == "" {
			v.active = msg.Session
		}

	case SessionListMsg:
		// Remove buffers for sessions that no longer exist.
		activeSet := make(map[string]bool, len(msg.Sessions))
		for _, s := range msg.Sessions {
			activeSet[s] = true
		}
		for name := range v.sessions {
			if !activeSet[name] {
				delete(v.sessions, name)
			}
		}
		// Auto-select first session if none active.
		if v.active == "" && len(msg.Sessions) > 0 {
			v.active = msg.Sessions[0]
		}

	case tea.KeyPressMsg:
		if !v.focused {
			return v, nil
		}
		// Forward keystrokes to tmux (handled by AppModel).
	}
	return v, nil
}

func (v ViewerModel) View() tea.View {
	var content string
	if v.active != "" {
		content = v.sessions[v.active]
	}

	// Trim to visible area.
	lines := strings.Split(content, "\n")
	visibleHeight := v.height - 4
	if visibleHeight < 1 {
		visibleHeight = 1
	}

	// Show the last N lines.
	start := len(lines) - visibleHeight
	if start < 0 {
		start = 0
	}
	visible := lines[start:]

	var b strings.Builder
	for _, l := range visible {
		b.WriteString(viewerContentStyle.Render(l))
		b.WriteString("\n")
	}

	// Fill remaining space.
	for i := len(visible); i < visibleHeight; i++ {
		b.WriteString("\n")
	}

	return tea.NewView(b.String())
}

func (v *ViewerModel) SetFocused(f bool)         { v.focused = f }
func (v *ViewerModel) SetSize(w, h int)          { v.width = w; v.height = h }
func (v *ViewerModel) ActiveSession() string     { return v.active }
func (v *ViewerModel) SetActiveSession(s string) { v.active = s }

func (v *ViewerModel) AppendOutput(session, content string) {
	buf := v.sessions[session]
	buf += content
	if lines := strings.Split(buf, "\n"); len(lines) > v.scrollback {
		buf = strings.Join(lines[len(lines)-v.scrollback:], "\n")
	}
	v.sessions[session] = buf
	if v.active == "" {
		v.active = session
	}
}
