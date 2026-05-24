package tools

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockNotifier struct {
	mu        sync.Mutex
	waitCalls int
	cancelled int
	nextID    uint64
	seq       uint64
	status    string
	waiters   map[uint64]chan struct{}
}

func newMockNotifier() *mockNotifier {
	return &mockNotifier{waiters: make(map[uint64]chan struct{})}
}

func (n *mockNotifier) Snapshot(session string) uint64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.seq
}

func (n *mockNotifier) WaitAfter(session string, after uint64) (<-chan struct{}, func()) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.waitCalls++
	if n.seq > after {
		ch := make(chan struct{})
		close(ch)
		return ch, func() {}
	}
	n.nextID++
	id := n.nextID
	ch := make(chan struct{})
	n.waiters[id] = ch
	return ch, func() {
		n.mu.Lock()
		defer n.mu.Unlock()
		n.cancelled++
		delete(n.waiters, id)
	}
}

func (n *mockNotifier) LastStatus(session string) string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.status
}

func (n *mockNotifier) Notify(status string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.seq++
	n.status = status
	for id, ch := range n.waiters {
		close(ch)
		delete(n.waiters, id)
	}
}

func TestWaitUntilIdleTool(t *testing.T) {
	mc := newMockTmuxClient()
	sessionName := "worker"
	mc.results[fmt.Sprintf("capture-pane -t %s -p -S -", sessionName)] = "Done. All tests pass.\n❯ "
	mc.results[fmt.Sprintf("list-panes -t %s -F #{pane_current_command}", sessionName)] = "claude"
	mc.results[fmt.Sprintf("list-panes -t %s -F #{pane_pid}", sessionName)] = "12345"
	notifier := newMockNotifier()

	go func() {
		time.Sleep(50 * time.Millisecond)
		notifier.Notify("done")
	}()

	tool := NewWaitUntilIdleTool(mc, notifier)
	result, err := tool.Execute(context.Background(), map[string]any{
		"session_name":    sessionName,
		"timeout_seconds": float64(10),
	})
	require.NoError(t, err)
	assert.Contains(t, result, `"idle"`)
	assert.Contains(t, result, `"waited_seconds"`)
}

func TestWaitUntilIdlePermissionHook(t *testing.T) {
	mc := newMockTmuxClient()
	sessionName := "worker"
	mc.results[fmt.Sprintf("capture-pane -t %s -p -S -", sessionName)] = "Do you want to proceed?\n❯ 1. Yes\n❯ 2. No"
	mc.results[fmt.Sprintf("list-panes -t %s -F #{pane_current_command}", sessionName)] = "claude"
	mc.results[fmt.Sprintf("list-panes -t %s -F #{pane_pid}", sessionName)] = "12345"
	notifier := newMockNotifier()

	go func() {
		time.Sleep(50 * time.Millisecond)
		notifier.Notify("permission")
	}()

	tool := NewWaitUntilIdleTool(mc, notifier)
	result, err := tool.Execute(context.Background(), map[string]any{
		"session_name":    sessionName,
		"timeout_seconds": float64(5),
	})
	require.NoError(t, err)
	assert.Contains(t, result, `"blocked"`)
}

func TestWaitUntilIdleTimeout(t *testing.T) {
	mc := newMockTmuxClient()
	mc.results[fmt.Sprintf("capture-pane -t %s -p -S -", "busy")] = "Running..."
	mc.results[fmt.Sprintf("list-panes -t %s -F #{pane_current_command}", "busy")] = "claude"
	mc.results[fmt.Sprintf("list-panes -t %s -F #{pane_pid}", "busy")] = "12345"

	tool := NewWaitUntilIdleTool(mc, nil)
	result, err := tool.Execute(context.Background(), map[string]any{
		"session_name":    "busy",
		"timeout_seconds": float64(1),
	})
	require.NoError(t, err)
	assert.Contains(t, result, `"timeout"`)
}

func TestWaitUntilIdleCancel(t *testing.T) {
	mc := newMockTmuxClient()
	mc.results[fmt.Sprintf("capture-pane -t %s -p -S -", "stuck")] = "Running..."

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tool := NewWaitUntilIdleTool(mc, nil)
	result, err := tool.Execute(ctx, map[string]any{
		"session_name":    "stuck",
		"timeout_seconds": float64(10),
	})
	require.NoError(t, err)
	assert.Contains(t, result, `"error"`)
}

func TestWaitUntilIdleMissingSession(t *testing.T) {
	mc := newMockTmuxClient()
	tool := NewWaitUntilIdleTool(mc, nil)
	_, err := tool.Execute(context.Background(), map[string]any{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "session_name is required")
}

func TestWaitUntilIdleNoHookFiresAgentDead(t *testing.T) {
	mc := newMockTmuxClient()
	mc.results[fmt.Sprintf("capture-pane -t %s -p -S -", "dead")] = "some output"
	mc.results[fmt.Sprintf("list-panes -t %s -F #{pane_current_command}", "dead")] = "zsh"
	mc.results[fmt.Sprintf("list-panes -t %s -F #{pane_pid}", "dead")] = "99999"

	// No hook fires and agent process dead -> returns agent_dead.
	tool := NewWaitUntilIdleTool(mc, nil)
	result, err := tool.Execute(context.Background(), map[string]any{
		"session_name":    "dead",
		"timeout_seconds": float64(5),
	})
	require.NoError(t, err)
	assert.Contains(t, result, `"agent_dead"`)
}

func TestWaitUntilIdleClearsStaleNotificationBeforeWaiting(t *testing.T) {
	mc := newMockTmuxClient()
	mc.results[fmt.Sprintf("capture-pane -t %s -p -S -", "busy")] = "Running..."
	mc.results[fmt.Sprintf("list-panes -t %s -F #{pane_current_command}", "busy")] = "claude"
	mc.results[fmt.Sprintf("list-panes -t %s -F #{pane_pid}", "busy")] = "12345"
	notifier := newMockNotifier()
	notifier.seq = 1

	tool := NewWaitUntilIdleTool(mc, notifier)
	result, err := tool.Execute(context.Background(), map[string]any{
		"session_name":    "busy",
		"timeout_seconds": float64(1),
	})
	require.NoError(t, err)
	assert.Contains(t, result, `"timeout"`)

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	assert.Equal(t, 1, notifier.waitCalls, "stale notifications should be ignored by snapshotting before registering a waiter")
	assert.Equal(t, 1, notifier.cancelled, "waiter should be cleaned up on exit")
}

func TestWaitUntilIdleReturnsIdleOnNotification(t *testing.T) {
	mc := newMockTmuxClient()
	sessionName := "worker"
	cmd := fmt.Sprintf("capture-pane -t %s -p -S -", sessionName)
	mc.results[cmd] = "Running..."
	mc.results[fmt.Sprintf("list-panes -t %s -F #{pane_current_command}", sessionName)] = "claude"
	mc.results[fmt.Sprintf("list-panes -t %s -F #{pane_pid}", sessionName)] = "12345"
	notifier := newMockNotifier()

	go func() {
		time.Sleep(50 * time.Millisecond)
		mc.mu.Lock()
		mc.results[cmd] = "Done. All tests pass.\n❯ "
		mc.mu.Unlock()
		notifier.Notify("done")
	}()

	tool := NewWaitUntilIdleTool(mc, notifier)
	result, err := tool.Execute(context.Background(), map[string]any{
		"session_name":    sessionName,
		"timeout_seconds": float64(5),
	})
	require.NoError(t, err)
	assert.Contains(t, result, `"idle"`)
}
