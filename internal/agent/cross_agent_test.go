package agent

import (
	"context"
	"testing"

	"github.com/naglezhang/fingersaver/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCrossAgentRelay(t *testing.T) {
	mc := newMockTmuxClient()
	mc.results["capture-pane -t auth -p -S -100"] = "Auth service started on port 8080\nAll tests passed"
	mc.results["send-keys -t frontend 'Summary: auth tests passed'"] = ""

	mp := &mockProvider{
		responses: [][]llm.StreamEvent{
			{
				{Type: llm.EventTextDelta, Text: "Auth tests passed"},
			},
		},
	}
	orch := NewOrchestrator(mp, mc, NewHookManager(), AllTools(mc))

	err := orch.CrossAgentRelay(context.Background(), "auth", "frontend",
		"Summarize the output from this agent in one line.")
	require.NoError(t, err)

	// Verify the send-keys command was executed.
	// Debug: print all executed commands.
	t.Logf("Executed commands: %v", mc.executed)

	// Verify send-keys was called with the summary.
	found := false
	for _, cmd := range mc.executed {
		if len(cmd) >= 18 && cmd[:18] == "send-keys -t front" && cmd != "send-keys -t frontend Enter" {
			found = true
			assert.Contains(t, cmd, "Auth tests passed")
		}
	}
	assert.True(t, found, "expected send-keys command for frontend session")
}

func TestCrossAgentRelayMissingTool(t *testing.T) {
	mc := newMockTmuxClient()
	orch := NewOrchestrator(nil, mc, NewHookManager(), nil)

	err := orch.CrossAgentRelay(context.Background(), "a", "b", "summarize")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}
