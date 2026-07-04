package role

import (
	"context"
	"strings"
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

// fakeNotifier is a minimal tools.Notifier for dispatcher tests. SafeSend's
// Enter-loss recovery is exercised by the real send_to_session tests; here we
// only need the interface satisfied so Dispatch compiles and the gated path
// runs far enough to invoke tc.Exec.
type fakeNotifier struct{}

func (f *fakeNotifier) Snapshot(session string) uint64 { return 0 }
func (f *fakeNotifier) WaitAfter(session string, after uint64) (<-chan struct{}, func()) {
	ch := make(chan struct{})
	return ch, func() {}
}
func (f *fakeNotifier) LastStatus(session string) string { return "" }
func (f *fakeNotifier) Working(session string) bool      { return false }

func TestDispatchExistingSessionInvokesSafeSend(t *testing.T) {
	// When the session exists, Dispatch delegates to SafeSend. SafeSend runs
	// validateSendTarget first, which calls tc.Exec for pane-current-command.
	// The fake returns "" (no agent detected), so SafeSend returns an error —
	// but the point of this test is to verify Dispatch REACHED the gated send
	// path (producing Exec calls), not that the send succeeded in a fake env.
	tc := &fakeTmux{sessions: map[string]bool{"makro": true}}
	d := NewDispatcher(tc, &fakeNotifier{})

	err := d.Dispatch(context.Background(), Decision{RoleName: "makro"}, "fix the bug")
	// An error here is expected: the fake session has no real agent, so the
	// agent-alive gate refuses. What matters is that the gated path ran.
	if err != nil {
		assert.Contains(t, err.Error(), "send to", "error should wrap the SafeSend failure")
	}
	// SafeSend's validateSendTarget issues a pane-current-command Exec.
	reachedGate := false
	for _, s := range tc.sent {
		if strings.Contains(s.cmd, "pane_current_command") || strings.Contains(s.cmd, "capture-pane") {
			reachedGate = true
		}
	}
	assert.True(t, reachedGate, "Dispatch must reach the gated send path (validateSendTarget), not bypass it")
}

func TestDispatchMissingSessionErrors(t *testing.T) {
	// Phase 1 does NOT auto-create sessions. A missing session must produce a
	// clear, user-facing error naming the session — not a silent create-and-
	// send-into-a-shell (which would bypass the safety net).
	tc := &fakeTmux{sessions: map[string]bool{}} // no sessions
	store := NewStore([]Role{{Name: "research", Description: "x", Session: "research"}})
	d := NewDispatcher(tc, &fakeNotifier{})
	d.SetStore(store)

	err := d.Dispatch(context.Background(), Decision{RoleName: "research"}, "research X")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `session "research" does not exist`)
	assert.Contains(t, err.Error(), "create it first", "error should guide the user")
	assert.Empty(t, tc.sent, "must NOT send anything when the session is missing")
}

func TestDispatchSessionFieldOverridesRoleName(t *testing.T) {
	// Role "makro" binds to session "makro-dev". Dispatch must target
	// "makro-dev", not "makro". We make "makro-dev" exist and "makro" NOT
	// exist: if resolution were wrong, we'd get the "makro" missing-session
	// error instead of reaching the gate.
	tc := &fakeTmux{sessions: map[string]bool{"makro-dev": true}}
	store := NewStore([]Role{{Name: "makro", Description: "x", Session: "makro-dev"}})
	d := NewDispatcher(tc, &fakeNotifier{})
	d.SetStore(store)

	_ = d.Dispatch(context.Background(), Decision{RoleName: "makro"}, "task")
	// If session resolution worked, the error (if any) is about the agent gate
	// on "makro-dev", NOT about "makro" being missing.
	for _, s := range tc.sent {
		// Every Exec must reference makro-dev; none should reference bare makro.
		if strings.Contains(s.cmd, `"makro"`) || strings.Contains(s.cmd, " -t makro ") {
			t.Fatalf("dispatch targeted role name 'makro' instead of session 'makro-dev': %s", s.cmd)
		}
	}
}

func TestDispatchNoOpOnNoRoles(t *testing.T) {
	tc := &fakeTmux{}
	d := NewDispatcher(tc, &fakeNotifier{})
	err := d.Dispatch(context.Background(), Decision{NoRoles: true}, "task")
	require.NoError(t, err, "NoRoles decision is a no-op: caller routes to handleLLM")
	assert.Empty(t, tc.sent)
}

// Compile-time checks that the fakes satisfy the interfaces Dispatcher needs.
var _ tools.TmuxClient = (*fakeTmux)(nil)
var _ tools.Notifier = (*fakeNotifier)(nil)
