package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/naglezhang/makro/internal/tmux"
	"github.com/naglezhang/makro/internal/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockTmuxClient implements TmuxClient for testing.
type mockTmuxClient struct {
	mu       sync.Mutex
	executed []string
	results  map[string]string
	errors   map[string]error
	state    *tmux.StateMirror
}

func newMockTmuxClient() *mockTmuxClient {
	sm := tmux.NewStateMirror()
	return &mockTmuxClient{
		results: make(map[string]string),
		errors:  make(map[string]error),
		state:   sm,
	}
}

func (m *mockTmuxClient) Exec(cmd string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.executed = append(m.executed, cmd)
	if err, ok := m.errors[cmd]; ok {
		return "", err
	}
	if result, ok := m.results[cmd]; ok {
		return result, nil
	}
	return "", nil
}

func (m *mockTmuxClient) State() *tmux.StateMirror {
	return m.state
}

func (m *mockTmuxClient) lastCmd() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.executed) == 0 {
		return ""
	}
	return m.executed[len(m.executed)-1]
}

func TestListSessionsTool(t *testing.T) {
	mc := newMockTmuxClient()
	mc.results["list-sessions"] = "session1\nsession2"

	tool := NewListSessionsTool(mc)
	result, err := tool.Execute(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, "session1\nsession2", result)
}

func TestListSessionsEmpty(t *testing.T) {
	mc := newMockTmuxClient()
	mc.results["list-sessions"] = ""

	tool := NewListSessionsTool(mc)
	result, err := tool.Execute(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, "No sessions found.", result)
}

func TestCreateSessionTool(t *testing.T) {
	mc := newMockTmuxClient()

	tool := NewCreateSessionTool(mc)
	result, err := tool.Execute(context.Background(), map[string]any{
		"name":        "test-session",
		"working_dir": "/tmp",
	})
	require.NoError(t, err)
	assert.Contains(t, result, "test-session")
	assert.Contains(t, mc.lastCmd(), "new-session")
}

func TestCreateSessionMissingName(t *testing.T) {
	mc := newMockTmuxClient()
	tool := NewCreateSessionTool(mc)
	_, err := tool.Execute(context.Background(), map[string]any{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")
}

func TestSwitchSessionTool(t *testing.T) {
	mc := newMockTmuxClient()
	mc.state.Apply(tmux.Notification{Type: tmux.NotifSessionChanged, SessionID: "$0", SessionName: "target"})

	tool := NewSwitchSessionTool(mc)
	result, err := tool.Execute(context.Background(), map[string]any{
		"name": "target",
	})
	require.NoError(t, err)
	assert.Equal(t, "target", result)
}

func TestSwitchSessionNotFound(t *testing.T) {
	mc := newMockTmuxClient()
	tool := NewSwitchSessionTool(mc)
	_, err := tool.Execute(context.Background(), map[string]any{
		"name": "nonexistent",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestKillSessionTool(t *testing.T) {
	mc := newMockTmuxClient()

	tool := NewKillSessionTool(mc)
	result, err := tool.Execute(context.Background(), map[string]any{
		"name": "doomed",
	})
	require.NoError(t, err)
	assert.Contains(t, result, "killed")
	assert.Contains(t, mc.lastCmd(), "kill-session")
}

func TestSendToSessionTool(t *testing.T) {
	mc := newMockTmuxClient()
	mc.results[fmt.Sprintf("list-panes -t %s -F #{pane_current_command}", "target")] = "claude"

	tool := NewSendToSessionTool(mc)
	result, err := tool.Execute(context.Background(), map[string]any{
		"name":    "target",
		"message": "echo hello",
	})
	require.NoError(t, err)
	assert.Contains(t, result, "Sent to")
}

func TestSendToSessionMissingArgs(t *testing.T) {
	mc := newMockTmuxClient()
	tool := NewSendToSessionTool(mc)
	_, err := tool.Execute(context.Background(), map[string]any{"name": "x"})
	assert.Error(t, err)
}

func TestReadSessionOutputTool(t *testing.T) {
	mc := newMockTmuxClient()
	allCmd := fmt.Sprintf("capture-pane -t %s -p -S -", "reader")
	rangeCmd := fmt.Sprintf("capture-pane -t %s -p -S -%d -E -%d", "reader", 501, 0)
	mc.results[allCmd] = "line1\nline2\nline3"
	mc.results[rangeCmd] = "line1\nline2\nline3"

	tool := NewReadSessionOutputTool(mc)
	result, err := tool.Execute(context.Background(), map[string]any{
		"name": "reader",
	})
	require.NoError(t, err)
	assert.Contains(t, result, "line1")
	assert.Contains(t, result, `"total_lines":3`)
	assert.Contains(t, result, `"has_more":false`)
}

func TestReadSessionOutputPaging(t *testing.T) {
	mc := newMockTmuxClient()
	var lines []string
	for i := 1; i <= 10; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	allOutput := strings.Join(lines, "\n")
	mc.results[fmt.Sprintf("capture-pane -t %s -p -S -", "pager")] = allOutput
	// Range command for page 1 (lines=3, offset=0): capture 4 lines from end.
	mc.results[fmt.Sprintf("capture-pane -t %s -p -S -%d -E -%d", "pager", 4, 0)] = allOutput

	tool := NewReadSessionOutputTool(mc)

	// Page 1: last 3 lines (offset=0, lines=3)
	result, err := tool.Execute(context.Background(), map[string]any{
		"name":   "pager",
		"lines":  float64(3),
		"offset": float64(0),
	})
	require.NoError(t, err)
	assert.Contains(t, result, "line 10")
	assert.Contains(t, result, `"total_lines":10`)
	assert.Contains(t, result, `"has_more":true`)

	// Page 2: lines 4-6 from end (offset=3, lines=3)
	result, err = tool.Execute(context.Background(), map[string]any{
		"name":   "pager",
		"lines":  float64(3),
		"offset": float64(3),
	})
	require.NoError(t, err)
	assert.Contains(t, result, "line 7")
	assert.Contains(t, result, `"has_more":true`)
}

func TestReadSessionOutputEmpty(t *testing.T) {
	mc := newMockTmuxClient()
	tool := NewReadSessionOutputTool(mc)
	result, err := tool.Execute(context.Background(), map[string]any{
		"name": "empty",
	})
	require.NoError(t, err)
	assert.Contains(t, result, `"total_lines":0`)
	assert.Contains(t, result, `"has_more":false`)
}

func TestAllToolsCount(t *testing.T) {
	mc := newMockTmuxClient()
	ts := AllTools(mc, nil, "/tmp", nil)
	assert.Len(t, ts, 18)
}

func TestReadStructuredOutputTool(t *testing.T) {
	mc := newMockTmuxClient()
	mc.results[fmt.Sprintf("capture-pane -t %s -p -S -", "dev")] = "⏺ I'll fix the auth bug.\n⏺ Read file: internal/auth/handler.go\n> check status\n⏺ All tests pass."

	tool := NewReadStructuredOutputTool(mc)
	result, err := tool.Execute(context.Background(), map[string]any{"name": "dev"})
	require.NoError(t, err)
	assert.Contains(t, result, `"status"`)
	assert.Contains(t, result, `"thinking"`)
	assert.Contains(t, result, `internal/auth/handler.go`)
	assert.Contains(t, result, `check status`)
}

func TestParseStructuredOutputWaitingInput(t *testing.T) {
	raw := "⏺ Do you want to proceed with this change?\n❯ 1. Yes\n  2. No"
	out := parseStructuredOutput(raw)
	assert.Equal(t, "waiting_input", out.Status)
}

func TestParseStructuredOutputError(t *testing.T) {
	raw := "⏺ Running… go test ./...\nFAIL: TestAuth\nError: test failed"
	out := parseStructuredOutput(raw)
	assert.True(t, len(out.Errors) > 0)
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "short", util.Truncate("short", 10))
	assert.Equal(t, "a very long string i...", util.Truncate("a very long string indeed", 20))
}
