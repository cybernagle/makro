package tui

import (
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	rw "github.com/mattn/go-runewidth"
)

// ViewerModel renders tmux session output in the right pane.
type ViewerModel struct {
	sessions     map[string]string // session name -> output buffer
	order        []string          // authoritative session list from SessionListMsg
	active       string            // currently displayed session
	width        int
	height       int
	focused      bool
	compact      bool // phone layout: filter noise lines
	scrollOffset int  // lines scrolled up from the bottom
}

func NewViewerModel() ViewerModel {
	return ViewerModel{
		sessions: make(map[string]string),
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

	case SessionListMsg:
		v.order = msg.Sessions
		v.pruneSessions(msg.Sessions)
		// If active session was removed, switch to the first remaining one.
		if v.active == "" || !v.sessionExists(v.active) {
			if len(msg.Sessions) > 0 {
				v.active = msg.Sessions[0]
			} else {
				v.active = ""
			}
		}

	case tea.KeyPressMsg:
		if !v.focused {
			return v, nil
		}
		v.handleKey(msg.String())

	case tea.MouseWheelMsg:
		if !v.focused {
			return v, nil
		}
		switch msg.Button {
		case tea.MouseWheelUp:
			v.scrollOffset++
		case tea.MouseWheelDown:
			if v.scrollOffset > 0 {
				v.scrollOffset--
			}
		}
	}

	return v, nil
}

func (v ViewerModel) View() tea.View {
	var b strings.Builder

	// Session tabs.
	if len(v.order) > 0 {
		b.WriteString(v.renderTabs())
		b.WriteString("\n")
	}

	content := v.sessions[v.active]
	lines := strings.Split(content, "\n")
	if v.compact {
		lines = filterNoiseLines(lines)
	}

	visibleHeight := v.height - 5
	if visibleHeight < 1 {
		visibleHeight = 1
	}

	// Apply scroll offset: show content from further up.
	offset := v.scrollOffset
	maxOff := len(lines) - visibleHeight
	if maxOff < 0 {
		maxOff = 0
	}
	if offset > maxOff {
		offset = maxOff
		v.scrollOffset = maxOff
	}

	start := len(lines) - visibleHeight - offset
	if start < 0 {
		start = 0
	}
	visible := lines[start:]

	contentW := v.width - 2
	if contentW < 1 {
		contentW = 1
	}
	renderedCount := 0
	for _, line := range visible {
		if v.compact {
			for _, wl := range wrapLineToWidth(line, contentW) {
				b.WriteString(viewerContentStyle.Render(wl))
				b.WriteString("\n")
				renderedCount++
			}
		} else {
			b.WriteString(viewerContentStyle.Render(truncateLineToWidth(line, contentW)))
			b.WriteString("\n")
			renderedCount++
		}
		if renderedCount >= visibleHeight {
			break
		}
	}

	for i := renderedCount; i < visibleHeight; i++ {
		b.WriteString("\n")
	}
	return tea.NewView(b.String())
}

// isNoiseLine returns true for lines that carry no useful content.
func isNoiseLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return true
	}
	// Pure separator lines: box-drawing, dashes, equals, tildes.
	for _, r := range trimmed {
		if 0x2500 <= r && r <= 0x257F {
			continue
		}
		if r == '─' || r == '━' || r == '│' || r == '┃' {
			continue
		}
		if r == '-' || r == '=' || r == '~' || r == '*' {
			continue
		}
		return false
	}
	return true
}

func filterNoiseLines(lines []string) []string {
	filtered := make([]string, 0, len(lines))
	for _, l := range lines {
		if !isNoiseLine(l) {
			filtered = append(filtered, l)
		}
	}
	return filtered
}

func (v *ViewerModel) renderTabs() string {
	var parts []string
	for _, s := range v.order {
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
	case "[":
		v.switchSession(-1)
	case "]":
		v.switchSession(1)
	}
}

func (v *ViewerModel) switchSession(dir int) {
	if len(v.order) == 0 {
		return
	}
	idx := 0
	for i, s := range v.order {
		if s == v.active {
			idx = i
			break
		}
	}
	idx += dir
	if idx < 0 {
		idx = len(v.order) - 1
	} else if idx >= len(v.order) {
		idx = 0
	}
	v.active = v.order[idx]
}

func (v *ViewerModel) sessionExists(name string) bool {
	for _, s := range v.order {
		if s == name {
			return true
		}
	}
	return false
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
func (v *ViewerModel) SetCompact(c bool)         { v.compact = c }
func (v *ViewerModel) ActiveSession() string     { return v.active }
func (v *ViewerModel) SetActiveSession(s string) { v.active = s }

func (v *ViewerModel) AppendOutput(session, content string) {
	// capture-pane returns the full screen each time; replace, don't append.
	v.sessions[session] = content
	if session == v.active && v.scrollOffset > 0 {
		v.scrollOffset = 0
	}
	if v.active == "" {
		v.active = session
	}
}

// truncateLineToWidth truncates a line to maxW visual columns, preserving
// ANSI escape sequences.
func truncateLineToWidth(s string, maxW int) string {
	var b strings.Builder
	w := 0
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' {
			j := i + 1
			if j < len(s) && s[j] == '[' {
				j++
				for j < len(s) && s[j] >= 0x20 && s[j] <= 0x3F {
					j++
				}
				if j < len(s) && s[j] >= 0x40 && s[j] <= 0x7E {
					j++
				}
			} else if j < len(s) {
				j++
			}
			b.WriteString(s[i:j])
			i = j
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		rw := rw.RuneWidth(r)
		if w+rw > maxW {
			break
		}
		b.WriteRune(r)
		w += rw
		i += size
	}
	for w < maxW {
		b.WriteByte(' ')
		w++
	}
	return b.String()
}

func wrapLineToWidth(s string, maxW int) []string {
	var lines []string
	var b strings.Builder
	w := 0
	i := 0
	for i < len(s) {
		// Copy ANSI escape sequences verbatim.
		if s[i] == '\x1b' {
			j := i + 1
			if j < len(s) && s[j] == '[' {
				j++
				for j < len(s) && s[j] >= 0x20 && s[j] <= 0x3F {
					j++
				}
				if j < len(s) && s[j] >= 0x40 && s[j] <= 0x7E {
					j++
				}
			} else if j < len(s) {
				j++
			}
			b.WriteString(s[i:j])
			i = j
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		rw := rw.RuneWidth(r)
		if w+rw > maxW {
			// Pad current line to exact width and start a new line.
			for w < maxW {
				b.WriteByte(' ')
				w++
			}
			lines = append(lines, b.String())
			b.Reset()
			w = 0
		}
		b.WriteRune(r)
		w += rw
		i += size
	}
	// Pad final line.
	for w < maxW {
		b.WriteByte(' ')
		w++
	}
	lines = append(lines, b.String())
	return lines
}
