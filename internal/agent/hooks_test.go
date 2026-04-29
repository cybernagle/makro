package agent

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHookRegisterAndFire(t *testing.T) {
	hm := NewHookManager()
	called := false

	hm.Register(Hook{
		Type: HookAgentStop,
		Name: "test-hook",
		Handler: func(ctx context.Context, payload any) (any, error) {
			called = true
			return nil, nil
		},
	})

	_, err := hm.Fire(context.Background(), HookAgentStop, nil)
	require.NoError(t, err)
	assert.True(t, called)
}

func TestHookMultipleFiresInOrder(t *testing.T) {
	hm := NewHookManager()
	var order []int

	hm.Register(Hook{
		Type: HookAfterToolCall,
		Name: "first",
		Handler: func(ctx context.Context, payload any) (any, error) {
			order = append(order, 1)
			return nil, nil
		},
	})
	hm.Register(Hook{
		Type: HookAfterToolCall,
		Name: "second",
		Handler: func(ctx context.Context, payload any) (any, error) {
			order = append(order, 2)
			return nil, nil
		},
	})

	_, err := hm.Fire(context.Background(), HookAfterToolCall, nil)
	require.NoError(t, err)
	assert.Equal(t, []int{1, 2}, order)
}

func TestHookBeforeToolCallBlocks(t *testing.T) {
	hm := NewHookManager()

	hm.Register(Hook{
		Type: HookBeforeToolCall,
		Name: "blocker",
		Handler: func(ctx context.Context, payload any) (any, error) {
			return BeforeToolCallResult{Block: true, Reason: "forbidden"}, nil
		},
	})

	result, err := hm.Fire(context.Background(), HookBeforeToolCall, nil)
	require.NoError(t, err)
	btr, ok := result.(BeforeToolCallResult)
	require.True(t, ok)
	assert.True(t, btr.Block)
	assert.Equal(t, "forbidden", btr.Reason)
}

func TestHookBeforeToolCallAllows(t *testing.T) {
	hm := NewHookManager()

	hm.Register(Hook{
		Type: HookBeforeToolCall,
		Name: "allow",
		Handler: func(ctx context.Context, payload any) (any, error) {
			return BeforeToolCallResult{Block: false}, nil
		},
	})

	result, err := hm.Fire(context.Background(), HookBeforeToolCall, "test")
	require.NoError(t, err)
	btr, ok := result.(BeforeToolCallResult)
	require.True(t, ok)
	assert.False(t, btr.Block)
}

func TestHookError(t *testing.T) {
	hm := NewHookManager()

	hm.Register(Hook{
		Type: HookAgentStop,
		Name: "failing",
		Handler: func(ctx context.Context, payload any) (any, error) {
			return nil, fmt.Errorf("boom")
		},
	})

	_, err := hm.Fire(context.Background(), HookAgentStop, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

func TestHookNoHooks(t *testing.T) {
	hm := NewHookManager()

	result, err := hm.Fire(context.Background(), HookAgentStop, "payload")
	require.NoError(t, err)
	assert.Equal(t, "payload", result)
}

func TestHookTypeString(t *testing.T) {
	assert.Equal(t, "before_tool_call", HookBeforeToolCall.String())
	assert.Equal(t, "after_tool_call", HookAfterToolCall.String())
	assert.Equal(t, "agent_stop", HookAgentStop.String())
	assert.Equal(t, "permission_request", HookPermissionRequest.String())
}

func TestHooksByType(t *testing.T) {
	hm := NewHookManager()
	hm.Register(Hook{Type: HookAgentStop, Name: "a"})
	hm.Register(Hook{Type: HookAgentStop, Name: "b"})
	hm.Register(Hook{Type: HookBeforeToolCall, Name: "c"})

	assert.Len(t, hm.HooksByType(HookAgentStop), 2)
	assert.Len(t, hm.HooksByType(HookBeforeToolCall), 1)
	assert.Len(t, hm.HooksByType(HookAfterToolCall), 0)
}
