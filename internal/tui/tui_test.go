package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/naglezhang/fingersaver/internal/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChatModelAppend(t *testing.T) {
	c := NewChatModel()
	c.SetSize(80, 24)
	c.AppendMessage("user", "hello")
	c.AppendMessage("assistant", "hi there")

	assert.Len(t, c.messages, 2)
	assert.Equal(t, "hello", c.messages[0].Content)
	assert.Equal(t, "assistant", c.messages[1].Role)
}

func TestChatModelSubmit(t *testing.T) {
	c := NewChatModel()
	c.SetSize(80, 24)

	// Simulate typing.
	m, _ := c.Update(tea.KeyPressMsg{Code: 'h'})
	c = m.(ChatModel)
	assert.Equal(t, "h", c.input)

	// Simulate enter.
	m, cmd := c.Update(tea.KeyPressMsg{Code: 13}) // enter
	c = m.(ChatModel)
	assert.Empty(t, c.input)
	require.NotNil(t, cmd)

	// The command should produce a SubmitMsg.
	msg := cmd()
	submit, ok := msg.(SubmitMsg)
	require.True(t, ok)
	assert.Equal(t, "h", submit.Text)
}

func TestChatModelBackspace(t *testing.T) {
	c := NewChatModel()
	c.SetSize(80, 24)
	c.input = "abc"
	c.cursor = 3

	m, _ := c.Update(tea.KeyPressMsg{Code: 127}) // backspace
	c = m.(ChatModel)
	assert.Equal(t, "ab", c.input)
	assert.Equal(t, 2, c.cursor)
}

func TestChatModelView(t *testing.T) {
	c := NewChatModel()
	c.SetSize(80, 24)
	c.AppendMessage("user", "hello world")
	view := c.View()
	assert.Contains(t, view.Content, "hello world")
}

func TestViewerModelAppend(t *testing.T) {
	v := NewViewerModel()
	v.SetSize(80, 24)
	v.AppendOutput("auth", "line 1\n")
	v.AppendOutput("auth", "line 2\n")

	assert.Equal(t, "auth", v.ActiveSession())
	assert.Contains(t, v.sessions["auth"], "line 1")
	assert.Contains(t, v.sessions["auth"], "line 2")
}

func TestViewerModelView(t *testing.T) {
	v := NewViewerModel()
	v.SetSize(80, 24)
	v.AppendOutput("test", "output content")

	view := v.View()
	assert.Contains(t, view.Content, "output content")
}

func TestViewerModelSessionSwitch(t *testing.T) {
	v := NewViewerModel()
	v.AppendOutput("session-a", "content A")
	v.AppendOutput("session-b", "content B")

	assert.Equal(t, "session-a", v.ActiveSession())

	v.SetActiveSession("session-b")
	assert.Equal(t, "session-b", v.ActiveSession())
}

func TestViewerModelSessionListCleanup(t *testing.T) {
	v := NewViewerModel()
	v.AppendOutput("keep", "data")
	v.AppendOutput("remove", "data")

	m, _ := v.Update(SessionListMsg{Sessions: []string{"keep"}})
	v = m.(ViewerModel)

	assert.Contains(t, v.sessions, "keep")
	assert.NotContains(t, v.sessions, "remove")
}

func TestMapTmuxKey(t *testing.T) {
	assert.Equal(t, "Up", mapTmuxKey("up"))
	assert.Equal(t, "Down", mapTmuxKey("down"))
	assert.Equal(t, "a", mapTmuxKey("a"))
	assert.Equal(t, "Space", mapTmuxKey("space"))
	assert.Equal(t, "", mapTmuxKey("ctrl+c"))
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "short", util.Truncate("short", 10))
	assert.Equal(t, "0123456789...", util.Truncate("0123456789012345", 10))
}
