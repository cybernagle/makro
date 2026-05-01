package tui

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"github.com/naglezhang/fingersaver/internal/util"
)

type ChatMessage struct {
	Role      string
	Content   string
	Streaming bool
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
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type spinnerTickMsg time.Time
type cursorBlinkMsg time.Time

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
	input         string
	width         int
	height        int
	focused       bool
	cursor        int // rune index (not byte index)
	history       *ChatHistory
	working       bool
	workingMsg    string
	spinnerFrame  int
	workStart     time.Time
	inputHistory  []string
	historyIdx    int
	cursorVisible bool
	targetSession string
	commands      []CommandSuggestion
	sessions      []string
	selectedSugg  int
}

func NewChatModel() ChatModel {
	return ChatModel{
		messages:      []ChatMessage{},
		focused:       true,
		cursorVisible: true,
	}
}

func (c ChatModel) Init() tea.Cmd {
	return cursorBlinkCmd()
}

// cursorByteIdx converts a rune index to a byte index in the input string.
func (c ChatModel) cursorByteIdx() int {
	return runeIdxToByte(c.input, c.cursor)
}

// runeCount returns the number of runes in the input.
func (c ChatModel) runeCount() int {
	return utf8.RuneCountInString(c.input)
}

func runeIdxToByte(s string, runeIdx int) int {
	for i := range s {
		if runeIdx == 0 {
			return i
		}
		runeIdx--
	}
	return len(s)
}

func cursorBlinkCmd() tea.Cmd {
	return tea.Tick(530*time.Millisecond, func(t time.Time) tea.Msg { return cursorBlinkMsg(t) })
}

// currentSuggestions returns filtered suggestions based on the current input.
// Returns nil when no suggestions should be shown.
func (c ChatModel) currentSuggestions() []Suggestion {
	if c.targetSession != "" {
		return nil
	}

	if strings.HasPrefix(c.input, "/") {
		prefix := c.input[1:]
		var result []Suggestion
		for _, cmd := range c.commands {
			if strings.HasPrefix(cmd.Name, prefix) {
				result = append(result, Suggestion{
					Text:        "/" + cmd.Name + " ",
					Description: cmd.Description,
				})
			}
		}
		return result
	}

	if strings.HasPrefix(c.input, "@") {
		prefix := c.input[1:]
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

// extractMention extracts @session-name from input and returns
// the session name and remaining text.
func extractMention(input string) (sessionName string, text string) {
	input = strings.TrimSpace(input)
	if !strings.HasPrefix(input, "@") {
		return "", input
	}
	rest := input[1:]
	name, remaining, found := strings.Cut(rest, " ")
	if !found {
		return name, ""
	}
	return name, strings.TrimSpace(remaining)
}

func (c ChatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		c.width = msg.Width
		c.height = msg.Height

	case cursorBlinkMsg:
		if c.focused && !c.working {
			c.cursorVisible = !c.cursorVisible
			return c, cursorBlinkCmd()
		}
		c.cursorVisible = true
		return c, cursorBlinkCmd()

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
		c.cursorVisible = true

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
			case "tab", "enter":
				if c.selectedSugg >= len(suggs) {
					c.selectedSugg = len(suggs) - 1
				}
				s := suggs[c.selectedSugg]
				// For @ completions, set sticky target instead of filling input.
				if strings.HasPrefix(s.Text, "@") {
					name := strings.TrimSpace(strings.TrimPrefix(s.Text, "@"))
					if name != "" {
						c.targetSession = name
						c.input = ""
						c.cursor = 0
						c.selectedSugg = 0
						return c, nil
					}
				}
				c.input = s.Text
				c.cursor = utf8.RuneCountInString(c.input)
				c.selectedSugg = 0
				return c, nil
			case "esc":
				c.selectedSugg = 0
				return c, nil
			}
		}

		switch msg.String() {
		case "enter":
			if c.working || strings.TrimSpace(c.input) == "" {
				return c, nil
			}
			text := c.input
			// Prepend sticky session target if set.
			if c.targetSession != "" {
				text = "@" + c.targetSession + " " + text
			}
			// Extract and set sticky session from @mention in input.
			if c.targetSession == "" && strings.HasPrefix(strings.TrimSpace(c.input), "@") {
				sessionName, _ := extractMention(text)
				if sessionName != "" {
					c.targetSession = sessionName
				}
			}
			c.inputHistory = append(c.inputHistory, c.input)
			c.historyIdx = len(c.inputHistory)
			c.input = ""
			c.cursor = 0
			c.working = true
			c.workingMsg = ""
			c.workStart = time.Now()
			c.spinnerFrame = 0
			c.appendMessage("user", text)
			return c, tea.Batch(
				func() tea.Msg { return SubmitMsg{Text: text} },
				tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg { return spinnerTickMsg(t) }),
			)
		case "backspace":
			if c.cursor > 0 {
				pos := c.cursorByteIdx()
				prevPos := runeIdxToByte(c.input, c.cursor-1)
				c.input = c.input[:prevPos] + c.input[pos:]
				c.cursor--
			} else if c.cursor == 0 && c.input == "" && c.targetSession != "" {
				c.targetSession = ""
			}
		case "delete":
			if c.cursor < c.runeCount() {
				pos := c.cursorByteIdx()
				nextPos := runeIdxToByte(c.input, c.cursor+1)
				c.input = c.input[:pos] + c.input[nextPos:]
			}
		case "left":
			if c.cursor > 0 {
				c.cursor--
			}
		case "right":
			if c.cursor < c.runeCount() {
				c.cursor++
			}
		case "up":
			if c.historyIdx > 0 {
				c.historyIdx--
				c.input = c.inputHistory[c.historyIdx]
				c.cursor = c.runeCount()
			}
		case "down":
			if c.historyIdx < len(c.inputHistory)-1 {
				c.historyIdx++
				c.input = c.inputHistory[c.historyIdx]
			} else if c.historyIdx < len(c.inputHistory) {
				c.historyIdx = len(c.inputHistory)
				c.input = ""
			}
			c.cursor = c.runeCount()
		case "esc":
			if c.targetSession != "" {
				c.targetSession = ""
				return c, nil
			}
		case "ctrl+c":
			return c, tea.Quit
		default:
			text := msg.Text
			if text == "" {
				s := msg.String()
				if s == "space" {
					text = " "
				} else if utf8.RuneCountInString(s) == 1 {
					text = s
				}
			}
			if text != "" {
				pos := c.cursorByteIdx()
				c.input = c.input[:pos] + text + c.input[pos:]
				c.cursor += utf8.RuneCountInString(text)
				c.selectedSugg = 0
			}
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
	}

	return c, nil
}

func (c ChatModel) View() tea.View {
	targetLines := c.height - 3

	// Render all content into lines, then trim from top to fit.
	var contentLines []string

	for _, m := range c.messages {
		var rendered string
		switch m.Role {
		case "user":
			rendered = userMsgStyle.Render("> " + m.Content)
		case "assistant":
			if m.Streaming {
				rendered = assistantMsgStyle.Render(m.Content)
			} else {
				rendered = assistantMsgStyle.Render(renderMarkdown(m.Content))
			}
		case "system":
			rendered = systemMsgStyle.Render(m.Content)
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

	maxContentLines := targetLines - reserveLines
	if maxContentLines < 1 {
		maxContentLines = 1
	}
	if len(contentLines) > maxContentLines {
		contentLines = contentLines[len(contentLines)-maxContentLines:]
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
	prefix := "> "
	if c.targetSession != "" {
		prefix = statusStyle.Render("@" + c.targetSession + " (Esc) > ")
	}
	if c.input == "" && c.targetSession == "" && !c.working {
		displayInput := ""
		if len(c.sessions) == 0 {
			displayInput = statusStyle.Render("Type /create <name> to start a session...")
		} else {
			displayInput = statusStyle.Render("Type @ to mention session, / for commands...")
		}
		contentLines = append(contentLines, chatInputStyle.Render(prefix+displayInput))
	} else {
		cursorLine := prefix + c.input
		if c.focused && !c.working {
			pos := c.cursorByteIdx()
			before := c.input[:pos]
			after := c.input[pos:]
			ch := "█"
			if !c.cursorVisible {
				ch = " "
			}
			cursorLine = prefix + before + ch + after
		}
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

func (c *ChatModel) SetFocused(f bool) {
	c.focused = f
	if f {
		c.cursorVisible = true
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
