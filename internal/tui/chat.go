package tui

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"github.com/naglezhang/fingersaver/internal/agent"
	"github.com/naglezhang/fingersaver/internal/util"
)

type ChatMessage struct {
	Role      string
	Content   string
	Streaming bool
	rendered  string
}

type CommandSuggestion struct {
	Name        string
	Description string
}

type Suggestion struct {
	Text        string
	Description string
}

// spinnerFrames are the animation frames for the working indicator.
const maxInputHistory = 1000

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type spinnerTickMsg time.Time

var (
	mdOnce     sync.Once
	mdRenderer *glamour.TermRenderer
)

func getMDRenderer() *glamour.TermRenderer {
	mdOnce.Do(func() {
		r, err := glamour.NewTermRenderer(
			glamour.WithStandardStyle("dark"),
			glamour.WithWordWrap(0),
		)
		if err != nil {
			log.Printf("[chat] glamour init error: %v", err)
			return
		}
		mdRenderer = r
	})
	return mdRenderer
}

type ChatModel struct {
	messages      []ChatMessage
	textInput     textinput.Model
	width         int
	height        int
	focused       bool
	history       *ChatHistory
	working       bool
	workingMsg    string
	spinnerFrame  int
	workStart     time.Time
	inputHistory  []string
	historyIdx    int
	targetSession string
	commands      []CommandSuggestion
	sessions      []string
	selectedSugg  int
	scrollOffset  int
	ctrlCCount    int
	lastCtrlC     time.Time
}

func NewChatModel() ChatModel {
	ti := textinput.New()
	ti.Prompt = "> "
	ti.SetVirtualCursor(false)
	ti.Focus()
	return ChatModel{
		messages:  []ChatMessage{},
		focused:   true,
		textInput: ti,
	}
}

func (c ChatModel) Init() tea.Cmd {
	return nil
}

// runeIdxToByte converts a rune index to a byte index in a string.
func runeIdxToByte(s string, runeIdx int) int {
	for i := range s {
		if runeIdx == 0 {
			return i
		}
		runeIdx--
	}
	return len(s)
}

// filterCommandSuggestions returns slash command suggestions matching prefix.
func filterCommandSuggestions(commands []CommandSuggestion, prefix string) []Suggestion {
	var result []Suggestion
	for _, cmd := range commands {
		if strings.HasPrefix(cmd.Name, prefix) {
			result = append(result, Suggestion{
				Text:        "/" + cmd.Name + " ",
				Description: cmd.Description,
			})
		}
	}
	return result
}

// currentSuggestions returns filtered suggestions based on the current input.
// Returns nil when no suggestions should be shown.
func (c ChatModel) currentSuggestions() []Suggestion {
	input := c.textInput.Value()

	if c.targetSession != "" {
		// In sticky session mode, only show / command suggestions.
		if strings.HasPrefix(input, "/") {
			return filterCommandSuggestions(c.commands, input[1:])
		}
		return nil
	}

	if strings.HasPrefix(input, "/") {
		return filterCommandSuggestions(c.commands, input[1:])
	}

	if strings.HasPrefix(input, "@") {
		prefix := input[1:]
		var result []Suggestion
		for _, s := range c.sessions {
			if strings.HasPrefix(s, prefix) {
				result = append(result, Suggestion{
					Text: "@" + s + " ",
				})
			}
		}
		return result
	}

	return nil
}

func (c ChatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		c.width = msg.Width
		c.height = msg.Height

	case spinnerTickMsg:
		if c.working {
			c.spinnerFrame = (c.spinnerFrame + 1) % len(spinnerFrames)
			return c, tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg { return spinnerTickMsg(t) })
		}
		return c, nil

	case tea.KeyPressMsg:
		if !c.focused {
			return c, nil
		}

		// Suggestion navigation when suggestions are visible.
		suggs := c.currentSuggestions()
		if len(suggs) > 0 {
			switch msg.String() {
			case "up":
				if c.selectedSugg > 0 {
					c.selectedSugg--
				}
				return c, nil
			case "down":
				if c.selectedSugg < len(suggs)-1 {
					c.selectedSugg++
				}
				return c, nil
			case "tab":
				if c.selectedSugg >= len(suggs) {
					c.selectedSugg = len(suggs) - 1
				}
				s := suggs[c.selectedSugg]
				// For @ completions, set sticky target instead of filling input.
				if strings.HasPrefix(s.Text, "@") {
					name := strings.TrimSpace(strings.TrimPrefix(s.Text, "@"))
					if name != "" {
						c.targetSession = name
						c.textInput.Reset()
						c.selectedSugg = 0
						log.Printf("[chat] Tab @%s -> SessionTargetMsg", name)
						return c, func() tea.Msg { return SessionTargetMsg{Name: name} }
					}
				}
				c.textInput.SetValue(s.Text)
				c.selectedSugg = 0
				return c, nil
			case "esc":
				c.selectedSugg = 0
				return c, nil
			}
		}

		switch msg.String() {
		case "enter":
			input := c.textInput.Value()
			if c.working || strings.TrimSpace(input) == "" {
				return c, nil
			}
			trimmed := strings.TrimSpace(input)
			// Local-only commands: skip sticky prefix and working state.
			if strings.HasPrefix(trimmed, "/layout ") || trimmed == "/resize" {
				c.inputHistory = append(c.inputHistory, input)
				c.historyIdx = len(c.inputHistory)
				c.textInput.Reset()
				c.trimHistory()
				return c, func() tea.Msg { return SubmitMsg{Text: trimmed} }
			}
			text := input
			// Prepend sticky session target only if input does not already start with @.
			if c.targetSession != "" && !strings.HasPrefix(trimmed, "@") && !strings.HasPrefix(trimmed, "/") {
				text = "@" + c.targetSession + " " + text
			}
			// Extract and set sticky session from @mention in input.
			if strings.HasPrefix(trimmed, "@") {
				sessionName, _ := agent.ExtractMention(text)
				if sessionName != "" {
					c.targetSession = sessionName
				}
			}
			c.inputHistory = append(c.inputHistory, input)
			c.historyIdx = len(c.inputHistory)
			c.textInput.Reset()
			c.trimHistory()
			c.working = true
			c.workingMsg = ""
			c.workStart = time.Now()
			c.spinnerFrame = 0
			c.appendMessage("user", text)
			return c, tea.Batch(
				func() tea.Msg { return SubmitMsg{Text: text} },
				tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg { return spinnerTickMsg(t) }),
			)
		case "up":
			if c.historyIdx > 0 {
				c.historyIdx--
				c.textInput.SetValue(c.inputHistory[c.historyIdx])
			}
		case "down":
			if c.historyIdx < len(c.inputHistory)-1 {
				c.historyIdx++
				c.textInput.SetValue(c.inputHistory[c.historyIdx])
			} else if c.historyIdx < len(c.inputHistory) {
				c.historyIdx = len(c.inputHistory)
				c.textInput.Reset()
			}
		case "ctrl+c":
			if c.working {
				return c, func() tea.Msg { return CancelRequestMsg{} }
			}
			if time.Since(c.lastCtrlC) > 2*time.Second {
				c.ctrlCCount = 0
			}
			c.lastCtrlC = time.Now()
			c.ctrlCCount++
			if c.ctrlCCount >= 2 {
				return c, func() tea.Msg { return QuitRequestMsg{} }
			}
			if c.targetSession != "" {
				c.targetSession = ""
			}
			c.appendMessage("system", "Ctrl+C again to quit")
			return c, nil
		case "esc":
			if c.targetSession != "" {
				c.targetSession = ""
			}
			return c, nil
		case "ctrl+d":
			return c, tea.Quit
		default:
			c.scrollOffset = 0
			c.selectedSugg = 0
			var cmd tea.Cmd
			c.textInput, cmd = c.textInput.Update(msg)
			return c, cmd
		}

	case OrchestratorEventMsg:
		switch msg.Type {
		case "text":
			if n := len(c.messages); n > 0 && c.messages[n-1].Role == "assistant" && c.messages[n-1].Streaming {
				c.messages[n-1].Content += msg.Content
			} else {
				c.appendStreamingMessage("assistant", msg.Content)
			}
		case "tool_call":
			c.flushStreamingToHistory()
			c.working = true
			c.workingMsg = fmt.Sprintf("Calling %s", msg.ToolName)
		case "tool_result":
			c.flushStreamingToHistory()
			c.appendMessage("system", fmt.Sprintf("[%s] %s", msg.ToolName, util.Truncate(msg.Content, 200)))
			c.working = true
			c.workingMsg = ""
		case "done":
			c.flushStreamingToHistory()
			c.working = false
			c.workingMsg = ""
		}

	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			c.scrollOffset++
		case tea.MouseWheelDown:
			if c.scrollOffset > 0 {
				c.scrollOffset--
			}
		}
		maxOff := len(c.messages) - max(c.height-2, 1)
		if maxOff < 0 {
			maxOff = 0
		}
		if c.scrollOffset > maxOff {
			c.scrollOffset = maxOff
		}
	}

	return c, nil
}

func (c ChatModel) View() tea.View {
	targetLines := c.height - 3

	// Render all content into lines, then trim from top to fit.
	var contentLines []string

	for i := range c.messages {
		m := &c.messages[i]
		var rendered string
		switch m.Role {
		case "user":
			if m.rendered == "" {
				m.rendered = userMsgStyle.Render("> " + m.Content)
			}
			rendered = m.rendered
		case "assistant":
			if m.Streaming {
				rendered = assistantMsgStyle.Render(m.Content)
			} else {
				if m.rendered == "" {
					m.rendered = assistantMsgStyle.Render(renderMarkdown(m.Content))
				}
				rendered = m.rendered
			}
		case "system":
			if m.rendered == "" {
				m.rendered = systemMsgStyle.Render(m.Content)
			}
			rendered = m.rendered
		case "guardian":
			if m.rendered == "" {
				m.rendered = guardianMsgStyle.Render(m.Content)
			}
			rendered = m.rendered
		}
		lines := strings.Split(rendered, "\n")
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		contentLines = append(contentLines, lines...)
		contentLines = append(contentLines, "")
	}

	if c.working {
		elapsed := time.Since(c.workStart)
		frame := spinnerFrames[c.spinnerFrame]
		label := c.workingMsg
		if label == "" {
			label = "Thinking"
		}
		contentLines = append(contentLines,
			toolCallStyle.Render(fmt.Sprintf("%s %s %.0fs", frame, label, elapsed.Seconds())),
			"",
		)
	}

	// Reserve lines for input and optional suggestions.
	suggs := c.currentSuggestions()
	suggLines := len(suggs)
	if suggLines > 5 {
		suggLines = 5
	}
	reserveLines := 1 + suggLines // input + suggestions
	if len(c.sessions) > 0 {
		reserveLines++ // session status bar
	}

	maxContentLines := targetLines - reserveLines
	if maxContentLines < 1 {
		maxContentLines = 1
	}
	if len(contentLines) > maxContentLines {
		offset := c.scrollOffset
		maxOff := len(contentLines) - maxContentLines
		if offset > maxOff {
			offset = maxOff
		}
		if offset < 0 {
			offset = 0
		}
		start := len(contentLines) - maxContentLines - offset
		if start < 0 {
			start = 0
		}
		end := start + maxContentLines
		if end > len(contentLines) {
			end = len(contentLines)
		}
		contentLines = contentLines[start:end]
	}
	for len(contentLines) < maxContentLines {
		contentLines = append(contentLines, "")
	}

	// Render suggestions.
	if len(suggs) > 0 {
		maxShow := 5
		if len(suggs) < maxShow {
			maxShow = len(suggs)
		}
		for i := 0; i < maxShow; i++ {
			s := suggs[i]
			line := "  " + s.Text
			if s.Description != "" {
				line += "  " + s.Description
			}
			if i == c.selectedSugg {
				contentLines = append(contentLines, suggestionHighlightStyle.Render(line))
			} else {
				contentLines = append(contentLines, suggestionStyle.Render(line))
			}
		}
	}

	// Session status bar.
	if len(c.sessions) > 0 {
		bar := statusStyle.Render("sessions: " + strings.Join(c.sessions, " | "))
		contentLines = append(contentLines, bar)
	}

	// Input line.
	input := c.textInput.Value()
	cursor := c.textInput.Position()

	prefix := "> "
	if c.targetSession != "" {
		prefix = statusStyle.Render("@" + c.targetSession + " > ")
	}
	cursorCh := " "
	if c.focused {
		cursorCh = "█"
	}

	if input == "" && c.targetSession == "" && !c.working {
		hint := ""
		if len(c.sessions) == 0 {
			hint = statusStyle.Render(" Type /create <name> to start a session...")
		} else {
			hint = statusStyle.Render(" Type @ to mention session, / for commands...")
		}
		contentLines = append(contentLines, chatInputStyle.Render(prefix+cursorCh+hint))
	} else {
		pos := runeIdxToByte(input, cursor)
		cursorLine := prefix + input[:pos] + cursorCh + input[pos:]
		contentLines = append(contentLines, chatInputStyle.Render(cursorLine))
	}

	output := strings.Join(contentLines, "\n")
	return tea.NewView(output)
}

func renderMarkdown(text string) string {
	r := getMDRenderer()
	if r == nil {
		return text
	}
	rendered, err := r.Render(text)
	if err != nil {
		return text
	}
	return strings.TrimSpace(rendered)
}

func (c *ChatModel) trimHistory() {
	if len(c.inputHistory) > maxInputHistory {
		trimmed := len(c.inputHistory) - maxInputHistory
		c.inputHistory = c.inputHistory[trimmed:]
		c.historyIdx -= trimmed
		if c.historyIdx < 0 {
			c.historyIdx = 0
		}
		if c.historyIdx > len(c.inputHistory) {
			c.historyIdx = len(c.inputHistory)
		}
	}
}

func (c *ChatModel) SetFocused(f bool) {
	c.focused = f
	if f {
		c.textInput.Focus()
	} else {
		c.textInput.Blur()
	}
}

func (c *ChatModel) AppendMessage(role, content string) {
	c.appendMessage(role, content)
}

func (c *ChatModel) appendMessage(role, content string) {
	c.messages = append(c.messages, ChatMessage{Role: role, Content: content})
	if c.history != nil {
		c.history.Append(role, content)
	}
}

func (c *ChatModel) appendStreamingMessage(role, content string) {
	c.messages = append(c.messages, ChatMessage{Role: role, Content: content, Streaming: true})
}

func (c *ChatModel) flushStreamingToHistory() {
	if n := len(c.messages); n > 0 && c.messages[n-1].Streaming {
		c.messages[n-1].Streaming = false
		if c.history != nil {
			c.history.Append(c.messages[n-1].Role, c.messages[n-1].Content)
		}
	}
}

func (c *ChatModel) SetSize(w, h int) {
	c.width = w
	c.height = h
}

func (c *ChatModel) InvalidateRenderCache() {
	for i := range c.messages {
		c.messages[i].rendered = ""
	}
}

func (c *ChatModel) SetHistory(h *ChatHistory) {
	c.history = h
}

func (c *ChatModel) LoadHistory() error {
	if c.history == nil {
		return nil
	}
	msgs, err := c.history.Load()
	if err != nil {
		return err
	}
	c.messages = msgs
	return nil
}

func (c *ChatModel) SetCommands(cmds []CommandSuggestion) {
	c.commands = cmds
}

func (c *ChatModel) SetSessions(sessions []string) {
	c.sessions = sessions
}

func (c *ChatModel) TargetSession() string {
	return c.targetSession
}
