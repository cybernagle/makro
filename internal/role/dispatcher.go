package role

import (
	"context"
	"fmt"

	"github.com/naglezhang/makro/internal/agent/tools"
	"github.com/naglezhang/makro/internal/tmux"
)

// Dispatcher resolves a routing Decision to a concrete tmux session and sends
// the task there. It never sends raw keystrokes itself: it delegates to
// tools.DirectSend (the same path @mention uses), which runs the shared
// pre-send gates and Enter-loss recovery (send_to_session.go).
type Dispatcher struct {
	tc    tools.TmuxClient
	store *Store // optional; needed to look up the role's Session field
}

// NewDispatcher builds a Dispatcher. Call SetStore before Dispatch if the
// router decisions may reference roles whose Session field differs from their
// Name (e.g. Session="" to create on demand).
func NewDispatcher(tc tools.TmuxClient) *Dispatcher {
	return &Dispatcher{tc: tc}
}

// SetStore attaches the role store so Dispatch can resolve a Decision's
// session name (which may differ from the role name, or be empty).
func (d *Dispatcher) SetStore(s *Store) { d.store = s }

// Dispatch sends the task to the session bound to dec.RoleName.
//
// Session resolution:
//   - If the role's Session field is non-empty, use it directly.
//   - If empty, use the role's Name as the session name and create it on demand.
//   - If the named session doesn't exist, create it (best-effort; failure is
//     non-fatal — DirectSend will surface a clear error if the agent isn't
//     alive afterwards).
//
// NoRoles decisions (empty store) are a no-op: the caller routes the task to
// the Orchestrator's normal handleLLM path instead.
func (d *Dispatcher) Dispatch(ctx context.Context, dec Decision, task string) error {
	if dec.NoRoles {
		return nil
	}

	sessionName := dec.RoleName
	if d.store != nil {
		if r, ok := d.store.Get(dec.RoleName); ok {
			if r.Session != "" {
				sessionName = r.Session
			}
		}
	}

	// Best-effort session creation when missing. DirectSend handles the case
	// where the agent isn't alive by returning a descriptive error.
	if !d.tc.HasSession(sessionName) {
		if err := d.createSession(sessionName); err != nil {
			return fmt.Errorf("create session %q: %w", sessionName, err)
		}
	}

	if err := tools.DirectSend(d.tc, sessionName, task); err != nil {
		return fmt.Errorf("send to %q: %w", sessionName, err)
	}
	return nil
}

// createSession creates a tmux session by running the tmux new-session
// command string built by tmux.NewSessionCmd. Empty workdir/command = plain
// detached session.
func (d *Dispatcher) createSession(name string) error {
	if _, err := d.tc.Exec(tmux.NewSessionCmd(name, "", "")); err != nil {
		return err
	}
	return nil
}
