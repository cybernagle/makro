package tools

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWaitUntilIdleTool(t *testing.T) {
	mc := newMockTmuxClient()
	mc.results[fmt.Sprintf("capture-pane -t %s -p -S -", "worker")] = "⏺ Done. All tests pass.\n❯ "

	tool := NewWaitUntilIdleTool(mc)
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

	tool := NewWaitUntilIdleTool(mc)
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

	tool := NewWaitUntilIdleTool(mc)
	result, err := tool.Execute(ctx, map[string]any{
		"session_name":    "stuck",
		"timeout_seconds": float64(10),
	})
	require.NoError(t, err)
	assert.Contains(t, result, `"error"`)
}

func TestWaitUntilIdleMissingSession(t *testing.T) {
	mc := newMockTmuxClient()
	tool := NewWaitUntilIdleTool(mc)
	_, err := tool.Execute(context.Background(), map[string]any{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "session_name is required")
}
