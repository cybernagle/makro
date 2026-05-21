package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
	"github.com/naglezhang/fingersaver/internal/agent"
	"github.com/naglezhang/fingersaver/internal/agent/tools"
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

	notifier tools.Notifier
	assessor tools.Assessor
	monitors map[string]context.CancelFunc // active monitors: session → cancel
}

type tmuxClient interface {
	Exec(cmd string) (string, error)
	State() *tmux.StateMirror
}

func NewAppModel(orch *agent.Orchestrator, tc tmuxClient, notifier tools.Notifier, assessor tools.Assessor) AppModel {
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
		notifier:     notifier,
		assessor:     assessor,
		monitors:     make(map[string]context.CancelFunc),
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
		}

	case QuitRequestMsg:
		a.cancel()
		return a, tea.Quit

	case CancelRequestMsg:
		if a.orchestrator != nil {
			a.orchestrator.Cancel()
		}

	case SendKeyMsg:
		session := a.chat.TargetSession()
		if session != "" {
			cmd := tmux.SendKeysCmd(session, msg.Key)
			a.tmuxClient.Exec(cmd)
			a.chat.AppendMessage("system", fmt.Sprintf("Sent %s to @%s", msg.Key, session))
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
		// &session: background monitor until idle. & alone lists active monitors.
		if strings.HasPrefix(text, "&") {
			sessionName := agent.ExtractMonitor(text)
			if sessionName != "" {
				a.startMonitor(sessionName)
			} else {
				a.listMonitors()
			}
			return a, nil
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

	case monitorDoneMsg:
		delete(a.monitors, msg.session)

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
		v.MouseMode = tea.MouseModeCellMotion
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
	v.MouseMode = tea.MouseModeCellMotion
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
	v.MouseMode = tea.MouseModeCellMotion
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
	source := "fingersaver"
	if name, _ := agent.ExtractMention(text); name != "" {
		source = name
	}
	events, err := a.orchestrator.ProcessInput(a.ctx, text)
	if err != nil {
		log.Printf("[tui] ProcessInput error: %v", err)
		a.forwardEvent(agent.OrchestratorEvent{
			Type:    agent.EventText,
			Content: fmt.Sprintf("Error: %v", err),
		}, source)
		a.forwardEvent(agent.OrchestratorEvent{Type: agent.EventDone}, source)
		return
	}
	count := 0
	doneSeen := false
	for e := range events {
		a.forwardEvent(e, source)
		count++
		if e.Type == agent.EventDone {
			doneSeen = true
		}
	}
	log.Printf("[tui] processOrchestratorInput done events=%d", count)
	if !doneSeen && a.sendFn != nil {
		a.sendFn(OrchestratorEventMsg{Type: "done", Source: source})
	}
}

func (a *AppModel) forwardEvent(e agent.OrchestratorEvent, source string) {
	if a.sendFn == nil {
		log.Printf("[tui] WARNING: sendFn is nil, dropping event type=%s", e.Type)
		return
	}
	a.sendFn(OrchestratorEventMsg{
		Type:     e.Type.String(),
		Content:  e.Content,
		ToolName: e.ToolName,
		Source:   source,
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

func (a *AppModel) SendChatMessage(role, content string) {
	if a.sendFn != nil {
		a.sendFn(ExternalChatMsg{Role: role, Content: content})
	}
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

// startMonitor launches a background goroutine that polls a session until idle.
// When a pending confirmation is detected, it auto-assesses and responds.
func (a *AppModel) startMonitor(sessionName string) {
	// Cancel existing monitor for this session if any.
	if cancel, ok := a.monitors[sessionName]; ok {
		cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.monitors[sessionName] = cancel
	a.chat.AppendMessage("system", fmt.Sprintf("Monitoring @%s until idle...", sessionName))

	go func() {
		defer func() {
			if a.sendFn != nil {
				a.sendFn(monitorDoneMsg{session: sessionName})
			}
		}()

		for {
			// Wait until idle or blocked.
			tool := tools.NewWaitUntilIdleTool(a.tmuxClient, a.notifier)
			result, err := tool.Execute(ctx, map[string]any{
				"session_name":    sessionName,
				"timeout_seconds": float64(300),
			})
			if err != nil {
				a.notify(fmt.Sprintf("Monitor @%s error: %v", sessionName, err))
				return
			}

			var parsed struct {
				Status string `json:"status"`
			}
			if jsonErr := json.Unmarshal([]byte(result), &parsed); jsonErr != nil {
				a.notify(fmt.Sprintf("Monitor @%s done: %s", sessionName, result))
				return
			}

			switch parsed.Status {
			case "idle":
				a.notify(fmt.Sprintf("Monitor @%s idle ✓", sessionName))
				return
			case "timeout":
				a.notify(fmt.Sprintf("Monitor @%s timeout — still running", sessionName))
				return
			case "agent_dead":
				a.notify(fmt.Sprintf("Monitor @%s: agent process exited — please check manually", sessionName))
				return
			case "error":
				a.notify(fmt.Sprintf("Monitor @%s error", sessionName))
				return
			case "blocked":
				// Assess the permission prompt and auto-respond.
				assessTool := tools.NewAssessConfirmationTool(a.tmuxClient, a.assessor)
				assessResult, assessErr := assessTool.Execute(ctx, map[string]any{
					"session_name": sessionName,
				})
				approve := false
				promptReason := ""
				if assessErr == nil {
					var ar struct {
						Decision string `json:"decision"`
						Reason   string `json:"reason"`
					}
					if json.Unmarshal([]byte(assessResult), &ar) == nil {
						approve = ar.Decision == "approve"
						promptReason = ar.Reason
					}
				}
				a.notify(fmt.Sprintf("Monitor @%s: auto-%s (%s)", sessionName,
					map[bool]string{true: "approving", false: "rejecting"}[approve], promptReason))
				resp := tools.NewRespondConfirmationTool(a.tmuxClient)
				respResult, respErr := resp.Execute(ctx, map[string]any{
					"session_name": sessionName,
					"approve":      approve,
				})
				if respErr != nil {
					a.notify(fmt.Sprintf("Monitor @%s respond error: %v", sessionName, respErr))
					return
				}
				_ = respResult
				continue
			default:
				a.notify(fmt.Sprintf("Monitor @%s done: %s", sessionName, result))
				return
			}
		}
	}()
}

func (a *AppModel) notify(content string) {
	if a.sendFn != nil {
		a.sendFn(ExternalChatMsg{Role: "system", Content: content})
	}
}

// listMonitors shows all active background monitors.
func (a *AppModel) listMonitors() {
	if len(a.monitors) == 0 {
		a.chat.AppendMessage("system", "No active monitors.")
		return
	}
	var names []string
	for name := range a.monitors {
		names = append(names, name)
	}
	sort.Strings(names)
	a.chat.AppendMessage("system", fmt.Sprintf("Active monitors: %s", strings.Join(names, ", ")))
}
