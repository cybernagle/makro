package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

type ChatMessage struct {
	Role    string
	Content string
}

type ChatModel struct {
	messages []ChatMessage
	input    string
	width    int
	height   int
	focused  bool
	cursor   int
	history  *ChatHistory
}

func NewChatModel() ChatModel {
	return ChatModel{
		messages: []ChatMessage{},
		focused:  true,
	}
}

func (c ChatModel) Init() tea.Cmd {
	return nil
}

func (c ChatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		c.width = msg.Width
		c.height = msg.Height

	case tea.KeyPressMsg:
		if !c.focused {
			return c, nil
		}
		switch msg.String() {
		case "enter":
			if strings.TrimSpace(c.input) == "" {
				return c, nil
			}
			text := c.input
			c.input = ""
			c.cursor = 0
			c.appendMessage("user", text)
			return c, func() tea.Msg { return SubmitMsg{Text: text} }
		case "backspace":
			if c.cursor > 0 && len(c.input) > 0 {
				c.input = c.input[:c.cursor-1] + c.input[c.cursor:]
				c.cursor--
			}
		case "delete":
			if c.cursor < len(c.input) {
				c.input = c.input[:c.cursor] + c.input[c.cursor+1:]
			}
		case "left":
			if c.cursor > 0 {
				c.cursor--
			}
		case "right":
			if c.cursor < len(c.input) {
				c.cursor++
			}
		case "ctrl+c":
			return c, tea.Quit
		default:
			if msg.Code != 0 && msg.String() != "" && len(msg.String()) == 1 {
				c.input = c.input[:c.cursor] + msg.String() + c.input[c.cursor:]
				c.cursor++
			}
		}

	case OrchestratorEventMsg:
		if msg.Type == "text" {
			c.appendMessage("assistant", msg.Content)
		} else if msg.Type == "tool_result" {
			c.appendMessage("system", fmt.Sprintf("[%s] %s", msg.ToolName, truncate(msg.Content, 200)))
		}
	}

	return c, nil
}

func (c ChatModel) View() tea.View {
	var b strings.Builder

	msgHeight := c.height - 6
	if msgHeight < 3 {
		msgHeight = 3
	}

	visible := c.visibleMessages(msgHeight)
	for _, m := range visible {
		switch m.Role {
		case "user":
			b.WriteString(userMsgStyle.Render("> " + m.Content))
		case "assistant":
			b.WriteString(assistantMsgStyle.Render(m.Content))
		case "system":
			b.WriteString(systemMsgStyle.Render(m.Content))
		}
		b.WriteString("\n")
	}

	linesUsed := len(visible)
	for i := linesUsed; i < msgHeight; i++ {
		b.WriteString("\n")
	}

	b.WriteString(strings.Repeat("─", max(c.width-2, 1)) + "\n")

	prompt := "> "
	cursorLine := prompt + c.input
	b.WriteString(chatInputStyle.Render(cursorLine))

	return tea.NewView(b.String())
}

func (c ChatModel) visibleMessages(maxLines int) []ChatMessage {
	if len(c.messages) == 0 {
		return nil
	}
	var result []ChatMessage
	lines := 0
	for i := len(c.messages) - 1; i >= 0; i-- {
		msgLines := max(1, len(c.messages[i].Content)/max(c.width-4, 1)+1)
		if lines+msgLines > maxLines && len(result) > 0 {
			break
		}
		lines += msgLines
		result = append([]ChatMessage{c.messages[i]}, result...)
	}
	return result
}

func (c *ChatModel) SetFocused(f bool) {
	c.focused = f
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

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
