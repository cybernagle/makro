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
	scrollback int
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
		v.appendAndTrim(msg.Session, msg.Content)

	case SessionListMsg:
		v.pruneSessions(msg.Sessions)
		if v.active == "" && len(msg.Sessions) > 0 {
			v.active = msg.Sessions[0]
		}

	case tea.KeyPressMsg:
		if !v.focused {
			return v, nil
		}
		v.handleKey(msg.String())
	}

	return v, nil
}

func (v ViewerModel) View() tea.View {
	var b strings.Builder

	// Session tabs.
	if len(v.sessionList()) > 0 {
		b.WriteString(v.renderTabs())
		b.WriteString("\n")
	}

	content := v.sessions[v.active]
	lines := strings.Split(content, "\n")
	visibleHeight := v.height - 5
	if visibleHeight < 1 {
		visibleHeight = 1
	}

	start := len(lines) - visibleHeight
	if start < 0 {
		start = 0
	}
	visible := lines[start:]

	// Render entire block once.
	b.WriteString(viewerContentStyle.Render(strings.Join(visible, "\n")))

	for i := len(visible); i < visibleHeight; i++ {
		b.WriteString("\n")
	}

	return tea.NewView(b.String())
}

func (v *ViewerModel) renderTabs() string {
	sessions := v.sessionList()
	var parts []string
	for _, s := range sessions {
		if s == v.active {
			parts = append(parts, viewerTitleStyle.Render("["+s+"]"))
		} else {
			parts = append(parts, statusStyle.Render(" "+s+" "))
		}
	}
	return strings.Join(parts, " ")
}

func (v *ViewerModel) handleKey(key string) {
	switch key {
	case "[", "left":
		v.switchSession(-1)
	case "]", "right":
		v.switchSession(1)
	}
}

func (v *ViewerModel) switchSession(dir int) {
	sessions := v.sessionList()
	if len(sessions) == 0 {
		return
	}
	idx := 0
	for i, s := range sessions {
		if s == v.active {
			idx = i
			break
		}
	}
	idx += dir
	if idx < 0 {
		idx = len(sessions) - 1
	} else if idx >= len(sessions) {
		idx = 0
	}
	v.active = sessions[idx]
}

func (v *ViewerModel) sessionList() []string {
	if len(v.sessions) == 0 {
		return nil
	}
	// SessionListMsg prunes removed sessions, so map keys are current.
	names := make([]string, 0, len(v.sessions))
	for name := range v.sessions {
		names = append(names, name)
	}
	return names
}

func (v *ViewerModel) appendAndTrim(session, content string) {
	buf := v.sessions[session] + content
	if lines := strings.Split(buf, "\n"); len(lines) > v.scrollback {
		buf = strings.Join(lines[len(lines)-v.scrollback:], "\n")
	}
	v.sessions[session] = buf
	if v.active == "" {
		v.active = session
	}
}

func (v *ViewerModel) pruneSessions(active []string) {
	activeSet := make(map[string]bool, len(active))
	for _, s := range active {
		activeSet[s] = true
	}
	for name := range v.sessions {
		if !activeSet[name] {
			delete(v.sessions, name)
		}
	}
}

func (v *ViewerModel) SetFocused(f bool)         { v.focused = f }
func (v *ViewerModel) SetSize(w, h int)          { v.width = w; v.height = h }
func (v *ViewerModel) ActiveSession() string     { return v.active }
func (v *ViewerModel) SetActiveSession(s string) { v.active = s }

func (v *ViewerModel) AppendOutput(session, content string) {
	v.appendAndTrim(session, content)
}
