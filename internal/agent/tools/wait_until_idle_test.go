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

func (n *mockNotifier) Notify() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.seq++
	for id, ch := range n.waiters {
		close(ch)
		delete(n.waiters, id)
	}
}

func TestWaitUntilIdleTool(t *testing.T) {
	mc := newMockTmuxClient()
	mc.results[fmt.Sprintf("capture-pane -t %s -p -S -", "worker")] = "⏺ Done. All tests pass.\n❯ "

	tool := NewWaitUntilIdleTool(mc, nil)
	result, err := tool.Execute(context.Background(), map[string]any{
		"session_name":    "worker",
		"timeout_seconds": float64(10),
	})
	require.NoError(t, err)
	assert.Contains(t, result, `"idle"`)
	assert.Contains(t, result, `"waited_seconds"`)
}

func TestWaitUntilIdleTimeout(t *testing.T) {
	mc := newMockTmuxClient()
	// Busy output — never idle.
	mc.results[fmt.Sprintf("capture-pane -t %s -p -S -", "busy")] = "⏺ Running… go test ./..."

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
	mc.results[fmt.Sprintf("capture-pane -t %s -p -S -", "stuck")] = "⏺ Running… go test ./..."

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

func TestWaitUntilIdleClearsStaleNotificationBeforeWaiting(t *testing.T) {
	mc := newMockTmuxClient()
	mc.results[fmt.Sprintf("capture-pane -t %s -p -S -", "busy")] = "⏺ Running… go test ./..."
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
	mc.results[cmd] = "⏺ Running… go test ./..."
	notifier := newMockNotifier()

	go func() {
		time.Sleep(50 * time.Millisecond)
		mc.mu.Lock()
		mc.results[cmd] = "⏺ Done. All tests pass.\n❯ "
		mc.mu.Unlock()
		notifier.Notify()
	}()

	tool := NewWaitUntilIdleTool(mc, notifier)
	result, err := tool.Execute(context.Background(), map[string]any{
		"session_name":    sessionName,
		"timeout_seconds": float64(5),
	})
	require.NoError(t, err)
	assert.Contains(t, result, `"idle"`)
}
