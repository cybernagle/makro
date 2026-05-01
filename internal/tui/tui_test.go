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

	// The command should produce a batch with SubmitMsg.
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	require.True(t, ok, "expected BatchMsg, got %T", msg)
	require.Len(t, batch, 2)

	// First command in batch should be SubmitMsg.
	submitMsg := batch[0]()
	submit, ok := submitMsg.(SubmitMsg)
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
	// AppendOutput replaces (capture-pane returns full screen each time).
	assert.Equal(t, "line 2\n", v.sessions["auth"])
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

func TestTruncate(t *testing.T) {
	assert.Equal(t, "short", util.Truncate("short", 10))
	assert.Equal(t, "0123456789...", util.Truncate("0123456789012345", 10))
}

// --- Chat input history navigation ---

func TestChatModelHistoryNavigation(t *testing.T) {
	c := NewChatModel()
	c.SetSize(80, 24)

	// Submit two messages.
	c.input = "first"
	m, _ := c.Update(tea.KeyPressMsg{Code: 13}) // enter
	c = m.(ChatModel)
	c.working = false // reset working state for next submit

	c.input = "second"
	m, _ = c.Update(tea.KeyPressMsg{Code: 13}) // enter
	c = m.(ChatModel)
	c.working = false

	assert.Equal(t, "", c.input)
	assert.Equal(t, 2, c.historyIdx)

	// Up once -> "second"
	m, _ = c.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	c = m.(ChatModel)
	assert.Equal(t, "second", c.input)

	// Up again -> "first"
	m, _ = c.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	c = m.(ChatModel)
	assert.Equal(t, "first", c.input)

	// Up at boundary -> stays "first"
	m, _ = c.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	c = m.(ChatModel)
	assert.Equal(t, "first", c.input)

	// Down -> "second"
	m, _ = c.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	c = m.(ChatModel)
	assert.Equal(t, "second", c.input)

	// Down at end -> cleared
	m, _ = c.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	c = m.(ChatModel)
	assert.Equal(t, "", c.input)
}

func TestChatModelBlockSubmitWhileWorking(t *testing.T) {
	c := NewChatModel()
	c.SetSize(80, 24)
	c.input = "hello"
	c.working = true

	m, cmd := c.Update(tea.KeyPressMsg{Code: 13}) // enter
	c = m.(ChatModel)
	assert.Equal(t, "hello", c.input) // input not cleared
	assert.Nil(t, cmd)
}

// --- Multibyte rune editing ---

func TestChatModelMultibyteInput(t *testing.T) {
	c := NewChatModel()
	c.SetSize(80, 24)

	// Simulate typing a multibyte character (Chinese: 你)
	m, _ := c.Update(tea.KeyPressMsg{Text: "你"})
	c = m.(ChatModel)
	assert.Equal(t, "你", c.input)
	assert.Equal(t, 1, c.cursor)

	// Backspace removes the whole rune
	m, _ = c.Update(tea.KeyPressMsg{Code: 127})
	c = m.(ChatModel)
	assert.Equal(t, "", c.input)
	assert.Equal(t, 0, c.cursor)
}

func TestChatModelCursorInMiddleOfMultibyte(t *testing.T) {
	c := NewChatModel()
	c.SetSize(80, 24)

	// Type "abc"
	for _, ch := range "abc" {
		m, _ := c.Update(tea.KeyPressMsg{Code: ch})
		c = m.(ChatModel)
	}
	assert.Equal(t, "abc", c.input)
	assert.Equal(t, 3, c.cursor)

	// Move cursor left twice to position 1
	m, _ := c.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	c = m.(ChatModel)
	assert.Equal(t, 2, c.cursor)
	m, _ = c.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	c = m.(ChatModel)
	assert.Equal(t, 1, c.cursor)

	// Insert multibyte char
	m, _ = c.Update(tea.KeyPressMsg{Text: "你"})
	c = m.(ChatModel)
	assert.Equal(t, "a你bc", c.input)
	assert.Equal(t, 2, c.cursor)
}

// --- Viewer session switching with brackets ---

func TestViewerModelBracketSwitching(t *testing.T) {
	v := NewViewerModel()
	v.focused = true
	v.order = []string{"alpha", "beta", "gamma"}
	v.active = "alpha"

	// ] switches forward
	m, _ := v.Update(tea.KeyPressMsg{Text: "]"})
	v = m.(ViewerModel)
	assert.Equal(t, "beta", v.ActiveSession())

	// ] again
	m, _ = v.Update(tea.KeyPressMsg{Text: "]"})
	v = m.(ViewerModel)
	assert.Equal(t, "gamma", v.ActiveSession())

	// ] wraps to start
	m, _ = v.Update(tea.KeyPressMsg{Text: "]"})
	v = m.(ViewerModel)
	assert.Equal(t, "alpha", v.ActiveSession())

	// [ goes backward
	m, _ = v.Update(tea.KeyPressMsg{Text: "["})
	v = m.(ViewerModel)
	assert.Equal(t, "gamma", v.ActiveSession())
}

func TestViewerModelActiveResetOnRemove(t *testing.T) {
	v := NewViewerModel()
	v.AppendOutput("a", "output-a")
	v.AppendOutput("b", "output-b")
	v.order = []string{"a", "b"}
	v.active = "b"

	// Session "b" is removed
	m, _ := v.Update(SessionListMsg{Sessions: []string{"a"}})
	v = m.(ViewerModel)
	assert.Equal(t, "a", v.ActiveSession())
	assert.NotContains(t, v.sessions, "b")
}

func TestViewerModelTabsShowAllSessions(t *testing.T) {
	v := NewViewerModel()
	v.AppendOutput("captured", "output")
	v.order = []string{"captured", "pending"}
	v.active = "captured"

	view := v.View()
	assert.Contains(t, view.Content, "captured")
	assert.Contains(t, view.Content, "pending")
}

// --- Streaming message aggregation ---

func TestChatModelStreamingAggregation(t *testing.T) {
	c := NewChatModel()
	c.SetSize(80, 24)

	// First text delta starts a streaming assistant message.
	m, _ := c.Update(OrchestratorEventMsg{Type: "text", Content: "Hello"})
	c = m.(ChatModel)
	assert.Len(t, c.messages, 1)
	assert.Equal(t, "Hello", c.messages[0].Content)
	assert.True(t, c.messages[0].Streaming)

	// Second delta appends to the same message.
	m, _ = c.Update(OrchestratorEventMsg{Type: "text", Content: " world"})
	c = m.(ChatModel)
	assert.Len(t, c.messages, 1)
	assert.Equal(t, "Hello world", c.messages[0].Content)
	assert.True(t, c.messages[0].Streaming)

	// Tool call flushes streaming.
	m, _ = c.Update(OrchestratorEventMsg{Type: "tool_call", ToolName: "list_sessions"})
	c = m.(ChatModel)
	assert.False(t, c.messages[0].Streaming)
	assert.True(t, c.working)

	// Tool result adds system message.
	m, _ = c.Update(OrchestratorEventMsg{Type: "tool_result", ToolName: "list_sessions", Content: "session1"})
	c = m.(ChatModel)
	assert.True(t, c.working)

	// Done stops working.
	m, _ = c.Update(OrchestratorEventMsg{Type: "done"})
	c = m.(ChatModel)
	assert.False(t, c.working)
}

func TestChatModelStreamingFlushOnDone(t *testing.T) {
	c := NewChatModel()
	c.SetSize(80, 24)

	// Start streaming text.
	m, _ := c.Update(OrchestratorEventMsg{Type: "text", Content: "response"})
	c = m.(ChatModel)
	assert.True(t, c.messages[0].Streaming)

	// Done flushes to history.
	m, _ = c.Update(OrchestratorEventMsg{Type: "done"})
	c = m.(ChatModel)
	assert.False(t, c.messages[0].Streaming)
}

// --- / command autocomplete ---

func TestChatModelSlashSuggestions(t *testing.T) {
	c := NewChatModel()
	c.SetSize(80, 24)
	c.SetCommands([]CommandSuggestion{
		{Name: "create", Description: "Create session"},
		{Name: "switch", Description: "Switch session"},
		{Name: "kill", Description: "Kill session"},
	})

	// Type "/" — should show all commands.
	c.input = "/"
	suggs := c.currentSuggestions()
	assert.Len(t, suggs, 3)
	assert.Equal(t, "/create ", suggs[0].Text)
}

func TestChatModelSlashFilterByPrefix(t *testing.T) {
	c := NewChatModel()
	c.SetSize(80, 24)
	c.SetCommands([]CommandSuggestion{
		{Name: "create", Description: "Create session"},
		{Name: "switch", Description: "Switch session"},
		{Name: "kill", Description: "Kill session"},
	})

	// Type "/c" -> should only suggest "create".
	c.input = "/c"
	c.cursor = 2
	suggs := c.currentSuggestions()
	require.Len(t, suggs, 1)
	assert.Equal(t, "/create ", suggs[0].Text)
}

func TestChatModelAtSuggestions(t *testing.T) {
	c := NewChatModel()
	c.SetSize(80, 24)
	c.SetSessions([]string{"auth", "api", "worker"})

	// Type "@" -> all sessions.
	c.input = "@"
	c.cursor = 1
	suggs := c.currentSuggestions()
	require.Len(t, suggs, 3)
	assert.Equal(t, "@auth ", suggs[0].Text)

	// Type "@a" -> filtered.
	c.input = "@a"
	c.cursor = 2
	suggs = c.currentSuggestions()
	require.Len(t, suggs, 2)
}

// --- Tab completion for @ sets sticky session ---

func TestChatModelAtTabSetsStickySession(t *testing.T) {
	c := NewChatModel()
	c.SetSize(80, 24)
	c.SetSessions([]string{"auth"})

	c.input = "@a"
	c.cursor = 2
	c.selectedSugg = 0

	suggs := c.currentSuggestions()
	require.Len(t, suggs, 1)

	// Tab selects the @auth suggestion and sets sticky target.
	m, _ := c.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	c = m.(ChatModel)

	assert.Equal(t, "auth", c.targetSession)
	assert.Equal(t, "", c.input)
	assert.Equal(t, 0, c.cursor)
}

// --- Sticky session behavior ---

func TestChatModelStickySessionPrepends(t *testing.T) {
	c := NewChatModel()
	c.SetSize(80, 24)
	c.targetSession = "auth"
	c.input = "check status"

	m, cmd := c.Update(tea.KeyPressMsg{Code: 13}) // enter
	c = m.(ChatModel)
	require.NotNil(t, cmd)
	assert.Equal(t, "@auth check status", c.messages[0].Content)
	assert.Empty(t, c.input)
}

func TestChatModelStickySessionClearedByBackspace(t *testing.T) {
	c := NewChatModel()
	c.SetSize(80, 24)
	c.targetSession = "auth"
	c.input = ""
	c.cursor = 0

	// Backspace on empty input clears targetSession.
	m, _ := c.Update(tea.KeyPressMsg{Code: 127}) // backspace
	c = m.(ChatModel)
	assert.Equal(t, "", c.targetSession)
}

func TestChatModelStickySessionClearedByEscape(t *testing.T) {
	c := NewChatModel()
	c.SetSize(80, 24)
	c.targetSession = "auth"

	m, _ := c.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	c = m.(ChatModel)
	assert.Equal(t, "", c.targetSession)
}

func TestChatModelNoSuggestionsWhenStickySet(t *testing.T) {
	c := NewChatModel()
	c.SetSize(80, 24)
	c.SetSessions([]string{"auth"})
	c.targetSession = "auth"
	c.input = "@"
	c.cursor = 1

	suggs := c.currentSuggestions()
	assert.Nil(t, suggs)
}

// --- ExtractMention ---

func TestExtractMention(t *testing.T) {
	name, text := extractMention("@auth hello")
	assert.Equal(t, "auth", name)
	assert.Equal(t, "hello", text)

	name, text = extractMention("@auth")
	assert.Equal(t, "auth", name)
	assert.Equal(t, "", text)

	name, text = extractMention("no mention")
	assert.Equal(t, "", name)
	assert.Equal(t, "no mention", text)
}

// --- Suggestion navigation ---

func TestChatModelSuggestionUpDown(t *testing.T) {
	c := NewChatModel()
	c.SetSize(80, 24)
	c.SetCommands([]CommandSuggestion{
		{Name: "create", Description: "Create"},
		{Name: "switch", Description: "Switch"},
		{Name: "kill", Description: "Kill"},
	})
	c.input = "/"
	c.cursor = 1
	c.selectedSugg = 0

	// Down -> index 1.
	m, _ := c.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	c = m.(ChatModel)
	assert.Equal(t, 1, c.selectedSugg)

	// Down -> index 2.
	m, _ = c.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	c = m.(ChatModel)
	assert.Equal(t, 2, c.selectedSugg)

	// Up -> index 1.
	m, _ = c.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	c = m.(ChatModel)
	assert.Equal(t, 1, c.selectedSugg)
}

func TestChatModelSuggestionTabCompletes(t *testing.T) {
	c := NewChatModel()
	c.SetSize(80, 24)
	c.SetCommands([]CommandSuggestion{
		{Name: "create", Description: "Create"},
	})
	c.input = "/"
	c.cursor = 1
	c.selectedSugg = 0

	// Tab completes to "/create ".
	m, _ := c.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	c = m.(ChatModel)
	assert.Equal(t, "/create ", c.input)
	assert.Equal(t, 8, c.cursor)
}
