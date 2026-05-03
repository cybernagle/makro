package tui

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
	"github.com/naglezhang/fingersaver/internal/agent"
	"github.com/naglezhang/fingersaver/internal/tmux"
)

type Focus int

const (
	FocusChat Focus = iota
	FocusViewer
)

type Layout int

const (
	LayoutDefault Layout = iota
	LayoutPhone
)

// AppModel is the root Bubbletea model managing the split-pane layout.
type AppModel struct {
	chat   ChatModel
	viewer ViewerModel

	focus          Focus
	width          int
	height         int
	layout         Layout
	layoutExplicit bool

	orchestrator *agent.Orchestrator
	tmuxClient   tmuxClient
	ctx          context.Context
	cancel       context.CancelFunc
	sendFn       func(tea.Msg)
	lastOutput   map[string]string
	lastSessions []string
}

type tmuxClient interface {
	Exec(cmd string) (string, error)
	State() *tmux.StateMirror
}

func NewAppModel(orch *agent.Orchestrator, tc tmuxClient) AppModel {
	ctx, cancel := context.WithCancel(context.Background())
	chat := NewChatModel()
	if orch != nil {
		var cmds []CommandSuggestion
		for _, c := range orch.Commands() {
			cmds = append(cmds, CommandSuggestion{Name: c.Name, Description: c.Description})
		}
		sort.Slice(cmds, func(i, j int) bool { return cmds[i].Name < cmds[j].Name })
		chat.SetCommands(cmds)
	}
	return AppModel{
		chat:         chat,
		viewer:       NewViewerModel(),
		focus:        FocusChat,
		orchestrator: orch,
		tmuxClient:   tc,
		ctx:          ctx,
		cancel:       cancel,
		lastOutput:   make(map[string]string),
	}
}

func (a AppModel) Init() tea.Cmd {
	return tickCmd()
}

func (a AppModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height
		if !a.layoutExplicit {
			if msg.Width < 80 {
				a.layout = LayoutPhone
			} else if a.layout == LayoutPhone {
				a.layout = LayoutDefault
			}
		}
		a.recalcSizes()

	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+o":
			if a.focus == FocusChat {
				a.focus = FocusViewer
				a.chat.SetFocused(false)
				a.viewer.SetFocused(true)
			} else {
				a.focus = FocusChat
				a.chat.SetFocused(true)
				a.viewer.SetFocused(false)
			}
			return a, nil
		case "ctrl+r":
			a.recalcSizes()
			return a, nil
		case "ctrl+d":
			a.cancel()
			return a, tea.Quit
		}

	case QuitRequestMsg:
		a.cancel()
		return a, tea.Quit

	case CancelRequestMsg:
		if a.orchestrator != nil {
			a.orchestrator.Cancel()
		}

	case SubmitMsg:
		// Handle layout commands locally.
		text := strings.TrimSpace(msg.Text)
		if text == "/layout phone" {
			a.layout = LayoutPhone
			a.layoutExplicit = true
			a.recalcSizes()
			return a, nil
		}
		if text == "/layout default" {
			a.layout = LayoutDefault
			a.layoutExplicit = true
			a.recalcSizes()
			return a, nil
		}
		if text == "/resize" {
			a.recalcSizes()
			return a, nil
		}
		// Auto-switch viewer to @mentioned session.
		if strings.HasPrefix(msg.Text, "@") {
			if fields := strings.Fields(msg.Text[1:]); len(fields) > 0 && fields[0] != "" {
				log.Printf("[app] @mention → viewer: %s", fields[0])
				a.viewer.SetActiveSession(fields[0])
				delete(a.lastOutput, fields[0])
			}
		}
		if a.orchestrator != nil {
			go a.processOrchestratorInput(msg.Text)
		}

	case tickMsg:
		cmds = append(cmds, a.pollTmux())
		cmds = append(cmds, tickCmd())

	case SessionListMsg:
		// Prune lastOutput entries for removed sessions.
		activeSet := make(map[string]struct{}, len(msg.Sessions))
		for _, s := range msg.Sessions {
			activeSet[s] = struct{}{}
		}
		for s := range a.lastOutput {
			if _, ok := activeSet[s]; !ok {
				delete(a.lastOutput, s)
			}
		}
		if sessionsChanged(a.lastSessions, msg.Sessions) {
			a.lastSessions = msg.Sessions
			a.chat.SetSessions(msg.Sessions)
			m2, cmd2 := a.viewer.Update(SessionListMsg{Sessions: msg.Sessions})
			a.viewer = m2.(ViewerModel)
			cmds = append(cmds, cmd2)
		}

	case SessionTargetMsg:
		log.Printf("[app] SessionTargetMsg → viewer: %s", msg.Name)
		a.viewer.SetActiveSession(msg.Name)
		delete(a.lastOutput, msg.Name)

	case combinedTmuxMsg:
		a.lastOutput[msg.session] = msg.output
		a.viewer.AppendOutput(msg.session, msg.output)
		if sessionsChanged(a.lastSessions, msg.sessions) {
			a.lastSessions = msg.sessions
			a.chat.SetSessions(msg.sessions)
			m2, cmd2 := a.viewer.Update(SessionListMsg{Sessions: msg.sessions})
			a.viewer = m2.(ViewerModel)
			cmds = append(cmds, cmd2)
		}

		return a, tea.Batch(cmds...)
	case OrchestratorEventMsg:
		// When switch_session tool completes, update viewer active session.
		if msg.Type == "tool_result" && msg.ToolName == "switch_session" && msg.Content != "" {
			a.viewer.SetActiveSession(msg.Content)
			delete(a.lastOutput, msg.Content)
		}
		m, cmd := a.chat.Update(msg)
		a.chat = m.(ChatModel)
		cmds = append(cmds, cmd)
		return a, tea.Batch(cmds...)

	case GuardianEventMsg:
		m, cmd := a.chat.Update(msg)
		a.chat = m.(ChatModel)
		cmds = append(cmds, cmd)
		return a, tea.Batch(cmds...)
	}

	// Route key and mouse events to focused pane.
	switch msg.(type) {
	case tea.KeyPressMsg, tea.MouseWheelMsg:
		if a.focus == FocusChat {
			m, cmd := a.chat.Update(msg)
			a.chat = m.(ChatModel)
			cmds = append(cmds, cmd)
		} else {
			m, cmd := a.viewer.Update(msg)
			a.viewer = m.(ViewerModel)
			cmds = append(cmds, cmd)
		}
	default:
		m, cmd := a.chat.Update(msg)
		a.chat = m.(ChatModel)
		cmds = append(cmds, cmd)

		m2, cmd2 := a.viewer.Update(msg)
		a.viewer = m2.(ViewerModel)
		cmds = append(cmds, cmd2)
	}

	return a, tea.Batch(cmds...)
}

func (a AppModel) View() tea.View {
	if a.width == 0 {
		v := tea.NewView("Loading...")
		v.AltScreen = true
		v.MouseMode = tea.MouseModeAllMotion
		return v
	}
	if a.layout == LayoutPhone {
		return a.viewPhone()
	}
	return a.viewDefault()
}

func (a AppModel) viewDefault() tea.View {
	chatStyle := BorderStyle(a.focus == FocusChat)
	chatW := max(a.width*2/5, 20)
	chatView := a.chat.View()
	chatContent := fmt.Sprintf("%s\n%s", chatTitleStyle.Render("Chat"), chatView.Content)
	chatPane := chatStyle.Width(chatW).Height(a.height).Render(chatContent)

	viewerStyle := BorderStyle(a.focus == FocusViewer)
	viewerW := max(a.width-chatW-2, 20)
	viewerView := a.viewer.View()
	viewerContent := fmt.Sprintf("%s\n%s",
		viewerTitleStyle.Render(fmt.Sprintf("Sessions %s", a.viewer.ActiveSession())),
		viewerView.Content,
	)
	viewerPane := viewerStyle.Width(viewerW).Height(a.height).Render(viewerContent)

	chatPane = trimToLines(chatPane, a.height)
	viewerPane = trimToLines(viewerPane, a.height)

	joined := lipgloss.JoinHorizontal(lipgloss.Top, chatPane, viewerPane)

	v := tea.NewView(joined)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeAllMotion
	return v
}

func (a AppModel) viewPhone() tea.View {
	viewerH := a.height * 3 / 5
	chatH := a.height - viewerH

	viewerStyle := BorderStyle(a.focus == FocusViewer)
	viewerView := a.viewer.View()
	viewerContent := fmt.Sprintf("%s\n%s",
		viewerTitleStyle.Render(fmt.Sprintf("Sessions %s", a.viewer.ActiveSession())),
		viewerView.Content,
	)
	viewerPane := viewerStyle.Width(a.width).Height(viewerH).Render(viewerContent)
	viewerPane = trimToLines(viewerPane, viewerH)

	chatStyle := BorderStyle(a.focus == FocusChat)
	chatView := a.chat.View()
	chatContent := fmt.Sprintf("%s\n%s", chatTitleStyle.Render("Chat"), chatView.Content)
	chatPane := chatStyle.Width(a.width).Height(chatH).Render(chatContent)
	chatPane = trimToLines(chatPane, chatH)

	// Normalize widths so borders align perfectly when stacked.
	targetW := max(lipgloss.Width(viewerPane), lipgloss.Width(chatPane))
	if lipgloss.Width(viewerPane) < targetW {
		pad := strings.Repeat(" ", targetW-lipgloss.Width(viewerPane))
		viewerPane += pad
	}
	if lipgloss.Width(chatPane) < targetW {
		pad := strings.Repeat(" ", targetW-lipgloss.Width(chatPane))
		chatPane += pad
	}

	joined := lipgloss.JoinVertical(lipgloss.Left, viewerPane, chatPane)

	v := tea.NewView(joined)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeAllMotion
	return v
}

// trimToLines truncates s to at most maxLines lines while preserving the
// first and last lines (rendered pane border/header and border/footer).
func trimToLines(s string, maxLines int) string {
	if maxLines <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= maxLines {
		return s
	}
	if maxLines <= 2 {
		return strings.Join(lines[:maxLines], "\n")
	}
	// Reserve first 2 lines (border + title) and last line (border), trim inner content from top.
	inner := lines[2 : len(lines)-1]
	budget := maxLines - 3
	if len(inner) > budget {
		inner = inner[len(inner)-budget:]
	}
	result := make([]string, 0, 3+len(inner))
	result = append(result, lines[0], lines[1])
	result = append(result, inner...)
	result = append(result, lines[len(lines)-1])
	return strings.Join(result, "\n")
}

func (a *AppModel) processOrchestratorInput(text string) {
	log.Printf("[tui] processOrchestratorInput start textLen=%d", len(text))
	events, err := a.orchestrator.ProcessInput(a.ctx, text)
	if err != nil {
		log.Printf("[tui] ProcessInput error: %v", err)
		a.forwardEvent(agent.OrchestratorEvent{
			Type:    agent.EventText,
			Content: fmt.Sprintf("Error: %v", err),
		})
		a.forwardEvent(agent.OrchestratorEvent{Type: agent.EventDone})
		return
	}
	count := 0
	doneSeen := false
	for e := range events {
		a.forwardEvent(e)
		count++
		if e.Type == agent.EventDone {
			doneSeen = true
		}
	}
	log.Printf("[tui] processOrchestratorInput done events=%d", count)
	// Ensure done event if stream closed without one.
	if !doneSeen && a.sendFn != nil {
		a.sendFn(OrchestratorEventMsg{Type: "done"})
	}
}

func (a *AppModel) forwardEvent(e agent.OrchestratorEvent) {
	if a.sendFn == nil {
		log.Printf("[tui] WARNING: sendFn is nil, dropping event type=%s", e.Type)
		return
	}
	a.sendFn(OrchestratorEventMsg{
		Type:     e.Type.String(),
		Content:  e.Content,
		ToolName: e.ToolName,
	})
}

func (a AppModel) pollTmux() tea.Cmd {
	if a.tmuxClient == nil {
		return nil
	}
	// Snapshot values before entering goroutine to avoid concurrent map access.
	lastOutputSnap := make(map[string]string, len(a.lastOutput))
	for k, v := range a.lastOutput {
		lastOutputSnap[k] = v
	}
	activeSnap := a.viewer.ActiveSession()
	lastSessionsSnap := a.lastSessions

	return func() tea.Msg {
		state := a.tmuxClient.State()
		if state == nil {
			return nil
		}
		sessions := state.Sessions()
		names := make([]string, 0, len(sessions))
		for _, s := range sessions {
			if s != nil {
				names = append(names, s.Name)
			}
		}
		sort.Strings(names)

		// Capture output from active session.
		if activeSnap != "" {
			cmd := tmux.CapturePaneCmd(activeSnap)
			last := lastOutputSnap[activeSnap]
			if out, err := a.tmuxClient.Exec(cmd); err == nil && out != "" && strings.TrimSpace(out) != strings.TrimSpace(last) {
				return combinedTmuxMsg{
					sessions: names,
					output:   out,
					session:  activeSnap,
				}
			}
		}

		// Only send update if sessions actually changed.
		if !sessionsChanged(lastSessionsSnap, names) {
			return nil
		}
		return SessionListMsg{Sessions: names}
	}
}

type combinedTmuxMsg struct {
	sessions []string
	output   string
	session  string
}

func sessionsChanged(a, b []string) bool {
	if len(a) != len(b) {
		return true
	}
	for i := range a {
		if a[i] != b[i] {
			return true
		}
	}
	return false
}

func (a *AppModel) SetSendFn(fn func(tea.Msg)) {
	a.sendFn = fn
}

func (a *AppModel) SetLayout(l Layout) {
	a.layout = l
	a.layoutExplicit = true
	a.recalcSizes()
}

func (a *AppModel) recalcSizes() {
	if a.width == 0 || a.height == 0 {
		return
	}
	a.chat.InvalidateRenderCache()
	if a.layout == LayoutPhone {
		viewerH := a.height * 3 / 5
		chatH := a.height - viewerH
		a.chat.SetSize(a.width, chatH)
		a.viewer.SetSize(a.width, viewerH)
		a.viewer.SetCompact(true)
	} else {
		chatW := a.width * 2 / 5
		viewerW := a.width - chatW - 2
		a.chat.SetSize(chatW, a.height)
		a.viewer.SetSize(viewerW, a.height)
		a.viewer.SetCompact(false)
	}
}

func (a *AppModel) SetChatHistory(h *ChatHistory) {
	a.chat.SetHistory(h)
	a.chat.LoadHistory()
}
