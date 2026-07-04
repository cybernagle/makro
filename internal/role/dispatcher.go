package role

import (
	"context"
	"fmt"

	"github.com/naglezhang/makro/internal/agent/tools"
)

// Dispatcher resolves a routing Decision to a concrete tmux session and sends
// the task there. It never sends raw keystrokes itself: it delegates to
// tools.SafeSend, which runs the same pre-send gates (agent-alive, no pending
// Yes/No dialog) and Enter-loss recovery as the send_to_session tool — the
// safety net the role-routing spec (§3.3, §6) mandates for autonomous sends.
type Dispatcher struct {
	tc       tools.TmuxClient
	notifier tools.Notifier
	store    *Store // optional; needed to look up the role's Session field
}

// NewDispatcher builds a Dispatcher. notifier is required: SafeSend uses it
// for first-send Enter-loss recovery on Claude Code sessions. Call SetStore
// before Dispatch so the role's Session field can override the role name.
func NewDispatcher(tc tools.TmuxClient, notifier tools.Notifier) *Dispatcher {
	return &Dispatcher{tc: tc, notifier: notifier}
}

// SetStore attaches the role store so Dispatch can resolve a Decision's
// session name (which may differ from the role name, or be empty).
func (d *Dispatcher) SetStore(s *Store) { d.store = s }

// Dispatch sends the task to the session bound to dec.RoleName.
//
// Session resolution:
//   - If the role's Session field is non-empty, use it directly.
//   - Otherwise the session name defaults to the role's Name.
//
// The session MUST already exist and have a coding agent running. Phase 1
// does NOT auto-create sessions: a freshly created tmux session runs the
// user's default shell, not a coding agent, so sending into it would either
// execute the task as a shell command (unsafe) or fail the agent-alive gate.
// Auto-creation with agent launch is a Phase 2 concern. If the session is
// missing, Dispatch returns a descriptive error naming the session to create.
//
// NoRoles decisions (empty store) are a no-op: the caller routes the task to
// the Orchestrator's normal handleLLM path instead.
func (d *Dispatcher) Dispatch(ctx context.Context, dec Decision, task string) error {
	if dec.NoRoles {
		return nil
	}

	sessionName := dec.RoleName
	if d.store != nil {
		if r, ok := d.store.Get(dec.RoleName); ok && r.Session != "" {
			sessionName = r.Session
		}
	}

	// Refuse to send to a session that doesn't exist. We deliberately do not
	// auto-create: see the method doc above.
	if !d.tc.HasSession(sessionName) {
		return fmt.Errorf("role %q session %q does not exist; create it first (e.g. start the coding agent in a tmux session named %q)", dec.RoleName, sessionName, sessionName)
	}

	if err := tools.SafeSend(ctx, d.tc, d.notifier, sessionName, task); err != nil {
		return fmt.Errorf("send to %q: %w", sessionName, err)
	}
	return nil
}
