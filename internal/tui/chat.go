package tui

import (
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/glamour/v2"
	tea "charm.land/bubbletea/v2"
	"github.com/naglezhang/fingersaver/internal/util"
)

type ChatMessage struct {
	Role      string
	Content   string
	Streaming bool
}

// spinnerFrames are the animation frames for the working indicator.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type spinnerTickMsg time.Time
type cursorBlinkMsg time.Time

func (c ChatModel) newMDRenderer() *glamour.TermRenderer {
	w := c.width - 4 // pane inner width minus border and padding
	if w < 40 {
		w = 40
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(w),
	)
	if err != nil {
		log.Printf("[chat] glamour init error: %v", err)
		return nil
	}
	return r
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
		switch msg.String() {
		case "enter":
			if strings.TrimSpace(c.input) == "" {
				return c, nil
			}
			text := c.input
			c.inputHistory = append(c.inputHistory, text)
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
		case "ctrl+c":
			return c, tea.Quit
		default:
			s := msg.String()
			if s == "space" {
				s = " "
			}
			if s != "" {
				pos := c.cursorByteIdx()
				c.input = c.input[:pos] + s + c.input[pos:]
				c.cursor += utf8.RuneCountInString(s)
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
			// Skip markdown rendering for streaming messages to avoid
			// unstable output from glamour on incomplete markdown.
			if m.Streaming {
				rendered = assistantMsgStyle.Render(m.Content)
			} else {
				rendered = assistantMsgStyle.Render(c.renderMarkdown(m.Content))
			}
		case "system":
			rendered = systemMsgStyle.Render(m.Content)
		}
		lines := strings.Split(rendered, "\n")
		// Drop trailing empty element from split if rendered ended with \n.
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

	// Reserve 1 line for input. Trim from top to show latest content.
	maxContentLines := targetLines - 1
	if maxContentLines < 1 {
		maxContentLines = 1
	}
	if len(contentLines) > maxContentLines {
		contentLines = contentLines[len(contentLines)-maxContentLines:]
	}

	// Pad to fill available space.
	for len(contentLines) < maxContentLines {
		contentLines = append(contentLines, "")
	}

	log.Printf("[chat/view] height=%d targetLines=%d contentLines=%d/%d maxContent=%d working=%v msgs=%d",
		c.height, targetLines, len(contentLines), maxContentLines, maxContentLines, c.working, len(c.messages))

	cursorLine := "> " + c.input
	if c.focused && !c.working {
		pos := c.cursorByteIdx()
		before := c.input[:pos]
		after := c.input[pos:]
		ch := "█"
		if !c.cursorVisible {
			ch = " "
		}
		cursorLine = "> " + before + ch + after
	}
	contentLines = append(contentLines, chatInputStyle.Render(cursorLine))

	output := strings.Join(contentLines, "\n")
	outputLines := strings.Count(output, "\n") + 1
	log.Printf("[chat/view] outputLines=%d targetLines=%d", outputLines, targetLines)
	return tea.NewView(output)
}

func (c ChatModel) renderMarkdown(text string) string {
	r := c.newMDRenderer()
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
