package role

import (
	"context"
	"testing"

	"github.com/naglezhang/makro/internal/agent/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeTmux records Exec calls and reports session existence.
type fakeTmux struct {
	sessions map[string]bool
	sent     []sendLog
}

type sendLog struct {
	cmd string
}

func (f *fakeTmux) Exec(cmd string) (string, error) {
	f.sent = append(f.sent, sendLog{cmd: cmd})
	return "", nil
}
func (f *fakeTmux) HasSession(name string) bool { return f.sessions[name] }

func TestDispatchExistingSession(t *testing.T) {
	tc := &fakeTmux{sessions: map[string]bool{"makro": true}}
	d := NewDispatcher(tc)

	err := d.Dispatch(context.Background(), Decision{RoleName: "makro"}, "fix the bug")
	require.NoError(t, err)
	// Dispatcher should have delegated to send_to_session's send path.
	assert.NotEmpty(t, tc.sent, "expected at least one tmux send command")
}

func TestDispatchCreatesMissingSession(t *testing.T) {
	// Role with empty Session = create on demand. Dispatcher must create it.
	tc := &fakeTmux{sessions: map[string]bool{}}
	store := NewStore([]Role{{Name: "research", Description: "x", Session: ""}})
	d := NewDispatcher(tc)
	d.SetStore(store)

	err := d.Dispatch(context.Background(), Decision{RoleName: "research"}, "research X")
	require.NoError(t, err)
}

func TestDispatchNoOpOnNoRoles(t *testing.T) {
	tc := &fakeTmux{}
	d := NewDispatcher(tc)
	err := d.Dispatch(context.Background(), Decision{NoRoles: true}, "task")
	require.NoError(t, err, "NoRoles decision is a no-op: caller routes to handleLLM")
	assert.Empty(t, tc.sent)
}

// Compile-time check that fakeTmux satisfies tools.TmuxClient.
var _ tools.TmuxClient = (*fakeTmux)(nil)
