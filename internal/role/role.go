// Package role defines named roles that bind classes of work to the tmux
// sessions that handle them. A role is the unit of routing: when a task
// arrives, the Router picks the role whose description best matches, and the
// Dispatcher sends the task to that role's session.
//
// Phase 1: roles are A-level only — declared in ~/.makro/roles.toml by the
// user. B-level (runtime-created) roles and graduation are Phase 2.
package role

import (
	"fmt"
	"strings"
)

// ClearAfter values. Only manual is implemented in Phase 1; marker and
// guardian are reserved enum values for Phase 3 (reviewer auto-clear).
const (
	ClearAfterManual   = "manual"
	ClearAfterMarker   = "marker"
	ClearAfterGuardian = "guardian"
)

// Role binds a class of work to the session that handles it.
//
// Metadata is the forward-compatibility hook (spec §12): Phase 1 reads and
// writes nothing in it. Future telemetry fields (call-count, score, version)
// live there first, then get promoted to first-class fields once stable. The
// loader populates it from any unknown TOML keys, so adding fields later
// never breaks an existing roles.toml.
type Role struct {
	Name        string         `toml:"name"`
	Description string         `toml:"description"`
	Session     string         `toml:"session"`     // tmux session name; "" = create on demand
	ClearAfter  string         `toml:"clear_after"` // manual|marker|guardian; default manual
	StateFile   string         `toml:"state_file"`  // optional; {project} substituted
	Metadata    map[string]any `toml:"-"`           // reserved; see package doc
}

// Validate checks the required fields and identifier-safe name.
func (r *Role) Validate() error {
	if r.Name == "" {
		return fmt.Errorf("role name is required")
	}
	if r.Description == "" {
		return fmt.Errorf("role description is required")
	}
	if !isIdentifierSafe(r.Name) {
		return fmt.Errorf("role name must be identifier-safe (letters, digits, _, -); got %q", r.Name)
	}
	return nil
}

// applyDefaults fills zero-value optional fields. Called by the loader after
// parsing, so callers always see a fully-formed Role.
func (r *Role) applyDefaults() {
	if r.ClearAfter == "" {
		r.ClearAfter = ClearAfterManual
	}
}

// ResolveStateFile substitutes {project} in StateFile. Returns "" (no error)
// when StateFile is empty — the caller treats empty as "no state file".
func (r *Role) ResolveStateFile(project string) (string, error) {
	if r.StateFile == "" {
		return "", nil
	}
	return strings.ReplaceAll(r.StateFile, "{project}", project), nil
}

// isIdentifierSafe returns true for names usable as a role identifier and as
// a path component (no slashes, spaces, or shell metacharacters).
func isIdentifierSafe(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}
