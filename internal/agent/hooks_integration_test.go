package agent

import (
	"context"
	"fmt"
	"testing"

	"github.com/naglezhang/fingersaver/internal/agent/tools"
	"github.com/naglezhang/fingersaver/internal/llm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOrchestratorAfterToolCallHook(t *testing.T) {
	mc := newMockTmuxClient()
	mc.results["list-sessions"] = "session1\nsession2"
	mp := &mockProvider{
		responses: [][]llm.StreamEvent{
			{
				{Type: llm.EventToolCallStart, ToolCallID: "tc_1", ToolCallName: "list_sessions"},
				{Type: llm.EventToolCallDelta, ToolCallID: "tc_1", ArgumentsDelta: "{}"},
			},
			{
				{Type: llm.EventTextDelta, Text: "Modified result received."},
			},
		},
	}
	hm := NewHookManager()
	hm.Register(Hook{
		Type: HookAfterToolCall,
		Name: "modifier",
		Handler: func(ctx context.Context, payload any) (any, error) {
			return AfterToolCallResult{ModifiedResult: "HOOK_MODIFIED"}, nil
		},
	})

	orch := NewOrchestrator(mp, mc, hm, tools.AllTools(mc, nil, "/tmp"))

	events, err := orch.ProcessInput(context.Background(), "list sessions")
	require.NoError(t, err)

	var toolResults []OrchestratorEvent
	for e := range events {
		if e.Type == EventToolResult {
			toolResults = append(toolResults, e)
		}
	}

	require.Len(t, toolResults, 1)
	assert.Equal(t, "HOOK_MODIFIED", toolResults[0].ToolResult)
}

func TestOrchestratorBeforeToolCallBlock(t *testing.T) {
	mc := newMockTmuxClient()
	mp := &mockProvider{
		responses: [][]llm.StreamEvent{
			{
				{Type: llm.EventToolCallStart, ToolCallID: "tc_1", ToolCallName: "list_sessions"},
				{Type: llm.EventToolCallDelta, ToolCallID: "tc_1", ArgumentsDelta: "{}"},
			},
			{
				{Type: llm.EventTextDelta, Text: "Tool was blocked."},
			},
		},
	}
	hm := NewHookManager()
	hm.Register(Hook{
		Type: HookBeforeToolCall,
		Name: "blocker",
		Handler: func(ctx context.Context, payload any) (any, error) {
			return BeforeToolCallResult{Block: true, Reason: "forbidden"}, nil
		},
	})

	orch := NewOrchestrator(mp, mc, hm, tools.AllTools(mc, nil, "/tmp"))

	events, err := orch.ProcessInput(context.Background(), "list sessions")
	require.NoError(t, err)

	var toolResults []OrchestratorEvent
	for e := range events {
		if e.Type == EventToolResult {
			toolResults = append(toolResults, e)
		}
	}

	require.Len(t, toolResults, 1)
	assert.Contains(t, toolResults[0].ToolResult, "Blocked")
	assert.Contains(t, toolResults[0].ToolResult, "forbidden")
}

func TestOrchestratorHooksAccessor(t *testing.T) {
	mc := newMockTmuxClient()
	hm := NewHookManager()
	orch := NewOrchestrator(nil, mc, hm, nil)
	assert.Equal(t, hm, orch.Hooks())
}

func TestOrchestratorAgentStopHook(t *testing.T) {
	hm := NewHookManager()
	stopped := false

	hm.Register(Hook{
		Type: HookAgentStop,
		Name: "notify",
		Handler: func(ctx context.Context, payload any) (any, error) {
			stopped = true
			return nil, nil
		},
	})

	_, err := hm.Fire(context.Background(), HookAgentStop, "session-auth")
	require.NoError(t, err)
	assert.True(t, stopped)
}

func TestOrchestratorPermissionHook(t *testing.T) {
	hm := NewHookManager()
	var requestPayload any

	hm.Register(Hook{
		Type: HookPermissionRequest,
		Name: "ask-user",
		Handler: func(ctx context.Context, payload any) (any, error) {
			requestPayload = payload
			return nil, fmt.Errorf("user denied")
		},
	})

	_, err := hm.Fire(context.Background(), HookPermissionRequest, "write file.go")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "denied")
	assert.Equal(t, "write file.go", requestPayload)
}
