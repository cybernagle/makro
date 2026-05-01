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

// AppModel is the root Bubbletea model managing the split-pane layout.
type AppModel struct {
	chat   ChatModel
	viewer ViewerModel

	focus  Focus
	width  int
	height int

	orchestrator *agent.Orchestrator
	tmuxClient   tmuxClient
	ctx          context.Context
	cancel       context.CancelFunc
	sendFn       func(tea.Msg)
	lastOutput   map[string]string
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
	return tea.Batch(tickCmd(), cursorBlinkCmd())
}

func (a AppModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height
		chatW := msg.Width * 2 / 5
		viewerW := msg.Width - chatW - 2
		a.chat.SetSize(chatW, msg.Height)
		a.viewer.SetSize(viewerW, msg.Height)

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
		case "ctrl+c":
			a.cancel()
			return a, tea.Quit
		}

	case SubmitMsg:
		// Auto-switch viewer to @mentioned session.
		if strings.HasPrefix(msg.Text, "@") {
			if fields := strings.Fields(msg.Text[1:]); len(fields) > 0 && fields[0] != "" {
				a.viewer.SetActiveSession(fields[0])
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
		a.chat.SetSessions(msg.Sessions)

	case combinedTmuxMsg:
		a.lastOutput[msg.session] = msg.output
		a.viewer.AppendOutput(msg.session, msg.output)
		a.chat.SetSessions(msg.sessions)
		m2, cmd2 := a.viewer.Update(SessionListMsg{Sessions: msg.sessions})
		a.viewer = m2.(ViewerModel)
		cmds = append(cmds, cmd2)

		return a, tea.Batch(cmds...)
	case OrchestratorEventMsg:
		// When switch_session tool completes, update viewer active session.
		if msg.Type == "tool_result" && msg.ToolName == "switch_session" && msg.Content != "" {
			a.viewer.SetActiveSession(msg.Content)
		}
		m, cmd := a.chat.Update(msg)
		a.chat = m.(ChatModel)
		cmds = append(cmds, cmd)
		return a, tea.Batch(cmds...)
	}

	// Route key events to focused pane.
	if kmsg, ok := msg.(tea.KeyPressMsg); ok {
		if a.focus == FocusChat {
			m, cmd := a.chat.Update(kmsg)
			a.chat = m.(ChatModel)
			cmds = append(cmds, cmd)
		} else {
			m, cmd := a.viewer.Update(kmsg)
			a.viewer = m.(ViewerModel)
			cmds = append(cmds, cmd)
		}
	} else {
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
		return v
	}

	chatStyle := BorderStyle(a.focus == FocusChat)
	chatW := a.width * 2 / 5
	chatView := a.chat.View()
	chatContent := fmt.Sprintf("%s\n%s", chatTitleStyle.Render("Chat"), chatView.Content)
	chatPane := chatStyle.Width(chatW).Height(a.height).Render(chatContent)

	viewerStyle := BorderStyle(a.focus == FocusViewer)
	viewerW := a.width - chatW - 2
	viewerView := a.viewer.View()
	viewerContent := fmt.Sprintf("%s\n%s",
		viewerTitleStyle.Render(fmt.Sprintf("Sessions %s", a.viewer.ActiveSession())),
		viewerView.Content,
	)
	viewerPane := viewerStyle.Width(viewerW).Height(a.height).Render(viewerContent)

	// Trim panes to exactly terminal height — lipgloss may wrap over-wide
	// content lines, producing more lines than Height() specifies.
	chatPane = trimToLines(chatPane, a.height)
	viewerPane = trimToLines(viewerPane, a.height)

	joined := lipgloss.JoinHorizontal(lipgloss.Top, chatPane, viewerPane)

	v := tea.NewView(joined)
	v.AltScreen = true
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
	if maxLines == 1 {
		return lines[0]
	}
	// Reserve first and last lines (borders), trim inner content from top.
	inner := lines[1 : len(lines)-1]
	budget := maxLines - 2
	if len(inner) > budget {
		inner = inner[len(inner)-budget:]
	}
	result := make([]string, 0, 2+len(inner))
	result = append(result, lines[0])
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
	if a.sendFn != nil {
		a.sendFn(OrchestratorEventMsg{
			Type:     e.Type.String(),
			Content:  e.Content,
			ToolName: e.ToolName,
		})
	}
}

func (a AppModel) pollTmux() tea.Cmd {
	if a.tmuxClient == nil {
		return nil
	}
	return func() tea.Msg {
		state := a.tmuxClient.State()
		if state == nil {
			return nil
		}
		sessions := state.Sessions()
		names := make([]string, len(sessions))
		for i, s := range sessions {
			names[i] = s.Name
		}

		// Capture output from active session.
		active := a.viewer.ActiveSession()
		if active != "" {
			cmd := tmux.CapturePaneCmd(active)
			last := a.lastOutput[active]
			if out, err := a.tmuxClient.Exec(cmd); err == nil && out != "" && out != last {
				return combinedTmuxMsg{
					sessions: names,
					output:   out,
					session:  active,
				}
			}
		}

		return SessionListMsg{Sessions: names}
	}
}

type combinedTmuxMsg struct {
	sessions []string
	output   string
	session  string
}

func (a *AppModel) SetSendFn(fn func(tea.Msg)) {
	a.sendFn = fn
}

func (a *AppModel) SetChatHistory(h *ChatHistory) {
	a.chat.SetHistory(h)
	a.chat.LoadHistory()
}
