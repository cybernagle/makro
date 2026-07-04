package role

import (
	"context"
	"testing"

	"github.com/naglezhang/makro/internal/agent/tools"
	"github.com/naglezhang/makro/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEndToEnd simulates the full Phase-1 flow: a TOML config → Store →
// Router (with a fake provider that always picks "makro") → Dispatcher →
// fake tmux. Verifies that a routed task produces a tmux send to the makro
// session without calling the real LLM or real tmux.
func TestEndToEnd(t *testing.T) {
	// 1. Load a config (inline, mimicking roles.toml).
	roles := []Role{
		{Name: "makro", Description: "Makro project dev", Session: "makro"},
		{Name: "juli", Description: "juli project dev", Session: "juli"},
		{Name: "default", Description: "fallback"},
	}
	store := NewStore(roles)

	// 2. Router with a fake provider that picks makro at 0.9 confidence.
	fp := &fakeProvider{resp: &llm.CompleteResult{
		Content: `{"role":"makro","confidence":0.9,"reason":"matches"}`,
	}}
	rt := NewRouter(store, fp)

	// 3. Dispatcher with a fake tmux that has the makro session alive.
	tc := &fakeTmux{sessions: map[string]bool{"makro": true}}
	d := NewDispatcher(tc)
	d.SetStore(store)

	// 4. Route + dispatch.
	dec, err := rt.Route(context.Background(), "fix the orchestrator bug")
	require.NoError(t, err)
	require.False(t, dec.NoRoles)
	require.Equal(t, "makro", dec.RoleName)

	// Verify the router made exactly one LLM call with a prompt naming makro.
	require.Equal(t, 1, fp.calls)
	assert.Contains(t, systemContent(fp.last), "makro")

	// Dispatch must produce a tmux send to the makro session.
	require.NoError(t, d.Dispatch(context.Background(), dec, "fix the orchestrator bug"))
	assert.NotEmpty(t, tc.sent, "dispatcher must have sent to tmux")
}

func systemContent(msgs []llm.Message) string {
	for _, m := range msgs {
		if m.Role == llm.RoleSystem {
			return m.Content
		}
	}
	return ""
}

// Compile-time check that we satisfy the tools.TmuxClient interface.
var _ tools.TmuxClient = (*fakeTmux)(nil)
