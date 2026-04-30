package agent

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/naglezhang/fingersaver/internal/util"

	"github.com/naglezhang/fingersaver/internal/tmux"
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
	assert.Contains(t, result, "Switched")
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
	mc.results[fmt.Sprintf("capture-pane -t %s -p", "reader")] = "line1\nline2\nline3"

	tool := NewReadSessionOutputTool(mc)
	result, err := tool.Execute(context.Background(), map[string]any{
		"name": "reader",
	})
	require.NoError(t, err)
	assert.Contains(t, result, "line1")
}

func TestReadSessionOutputEmpty(t *testing.T) {
	mc := newMockTmuxClient()
	tool := NewReadSessionOutputTool(mc)
	result, err := tool.Execute(context.Background(), map[string]any{
		"name": "empty",
	})
	require.NoError(t, err)
	assert.Equal(t, "(empty)", result)
}

func TestAllToolsCount(t *testing.T) {
	mc := newMockTmuxClient()
	tools := AllTools(mc)
	assert.Len(t, tools, 6)
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "short", util.Truncate("short", 10))
	assert.Equal(t, "a very long string i...", util.Truncate("a very long string indeed", 20))
}
