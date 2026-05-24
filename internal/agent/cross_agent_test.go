package agent

import (
	"context"
	"testing"

	"github.com/naglezhang/makro/internal/agent/tools"
	"github.com/naglezhang/makro/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCrossAgentRelay(t *testing.T) {
	mc := newMockTmuxClient()
	mc.results["capture-pane -t auth -p -S -100"] = "Auth service started on port 8080\nAll tests passed"
	mc.results["send-keys -t frontend 'Summary: auth tests passed'"] = ""
	mc.results["list-panes -t frontend -F #{pane_current_command}"] = "claude"

	mp := &mockProvider{
		responses: [][]llm.StreamEvent{
			{
				{Type: llm.EventTextDelta, Text: "Auth tests passed"},
			},
		},
	}
	orch := NewOrchestrator(mp, mc, NewHookManager(), tools.AllTools(mc, nil, "/tmp", nil))

	err := orch.CrossAgentRelay(context.Background(), "auth", "frontend",
		"Summarize the output from this agent in one line.")
	require.NoError(t, err)

	t.Logf("Executed commands: %v", mc.executed)

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
