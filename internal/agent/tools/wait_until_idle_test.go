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
	mu         sync.Mutex
	waitCalls  int
	clearCalls int
	stale      bool
	ch         chan struct{}
}

func newMockNotifier() *mockNotifier {
	return &mockNotifier{ch: make(chan struct{})}
}

func (n *mockNotifier) WaitCh(session string) <-chan struct{} {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.waitCalls++
	if n.stale {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return n.ch
}

func (n *mockNotifier) Clear(session string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.clearCalls++
	n.stale = false
	if n.ch == nil {
		n.ch = make(chan struct{})
	}
}

func (n *mockNotifier) Notify() {
	n.mu.Lock()
	defer n.mu.Unlock()
	select {
	case <-n.ch:
	default:
		close(n.ch)
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
	notifier.stale = true

	tool := NewWaitUntilIdleTool(mc, notifier)
	result, err := tool.Execute(context.Background(), map[string]any{
		"session_name":    "busy",
		"timeout_seconds": float64(1),
	})
	require.NoError(t, err)
	assert.Contains(t, result, `"timeout"`)

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	assert.Equal(t, 1, notifier.waitCalls, "stale notifications should be cleared before registering a waiter")
	assert.GreaterOrEqual(t, notifier.clearCalls, 2, "wait should clear notifier state at start and on exit")
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
