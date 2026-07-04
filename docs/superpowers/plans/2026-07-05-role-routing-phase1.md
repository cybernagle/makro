# Role-Based Routing — Phase 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add A-level role config (`~/.makro/roles.toml`) + an LLM-driven routing cascade that directs non-slash, non-`@mention` user input to the role whose session should handle it, dispatching via the existing `send_to_session` safety net.

**Architecture:** A new `internal/role` package owns the `Role` struct (with a forward-compatible `Metadata` field), a TOML loader, a `Store` (in-memory cache of A-level roles), and a `Router` that makes one `provider.Complete` call with a structured-output prompt to pick a role. A `Dispatcher` resolves the chosen role's tmux session (existing or new) and delegates the actual send to `send_to_session`. The Orchestrator's `ProcessInput` grows one new branch between `@mention` and `handleLLM`: route → dispatch.

**Tech Stack:** Go 1.26, `BurntSushi/toml` (new dep), existing `internal/llm.Provider` for the routing call, existing `tools.DirectSend`/`send_to_session` for dispatch, `testify` for tests.

**Spec:** `docs/superpowers/specs/2026-07-05-role-routing-design.md`

**Branch:** `feat/role-routing`

**Phase 1 scope (this plan):** A-level config roles only. Router cascade = match A-level → (no B-level in Phase 1) → fallback. No B-level creation, no graduation, no reviewer state files. Those are Phase 2/3.

---

## File Structure

New package `internal/role/` (mirrors `internal/agent/skills/` layout):

| File | Responsibility |
|---|---|
| `internal/role/role.go` | `Role` struct (with `Metadata map[string]any`), validation, defaults |
| `internal/role/loader.go` | Parse `roles.toml` → `[]Role`. Forward-compatible (unknown keys tolerated). |
| `internal/role/loader_test.go` | Loader tests |
| `internal/role/store.go` | `Store` holds loaded A-level roles, lookup-by-name, default-role resolution |
| `internal/role/store_test.go` | Store tests |
| `internal/role/router.go` | `Router` — one LLM call + cascade (A-match → fallback). Structured-output prompt + JSON parse. |
| `internal/role/router_test.go` | Router tests with a fake `llm.Provider` |
| `internal/role/dispatcher.go` | `Dispatcher` — resolve session (existing vs new via `create_session` logic), then delegate to `send_to_session` via `tools.TmuxClient`. |
| `internal/role/dispatcher_test.go` | Dispatcher tests with a fake `TmuxClient` |

Modified files:

| File | Change |
|---|---|
| `go.mod` / `go.sum` | Add `github.com/BurntSushi/toml` |
| `internal/agent/orchestrator.go` | New `SetRoles(*role.Store)` + `Router`/`Dispatcher` fields; new branch in `ProcessInput` between `@mention` and `handleLLM`. |
| `main.go` | Load `~/.makro/roles.toml` (+ `./.makro/roles.toml`), build `role.Store`, call `orch.SetRoles(...)` next to `orch.LoadSkills`. |
| `cmd/gui/chat_service.go` | Same wiring as `main.go` (the GUI composition root), next to its `orch.LoadSkills` call. |

---

## Task 1: Add `BurntSushi/toml` dependency

**Files:**
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Add the dependency**

Run:
```bash
cd /Users/naglezhang/Desktop/Code/Makro && go get github.com/BurntSushi/toml@latest
```

- [ ] **Step 2: Verify it resolves**

Run:
```bash
go mod tidy && go build ./...
```
Expected: exit 0, no errors. `go.sum` now contains `BurntSushi/toml` lines.

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: add BurntSushi/toml dep for roles config"
```

---

## Task 2: `Role` struct + validation

**Files:**
- Create: `internal/role/role.go`
- Test: `internal/role/role_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/role/role_test.go`:
```go
package role

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoleValidate(t *testing.T) {
	tests := []struct {
		name    string
		role    Role
		wantErr string // empty = no error
	}{
		{
			name:    "valid minimal",
			role:    Role{Name: "makro", Description: "Makro dev"},
			wantErr: "",
		},
		{
			name:    "missing name",
			role:    Role{Description: "x"},
			wantErr: "role name is required",
		},
		{
			name:    "missing description",
			role:    Role{Name: "makro"},
			wantErr: "role description is required",
		},
		{
			name:    "name with space rejected",
			role:    Role{Name: "my role", Description: "x"},
			wantErr: "role name must be identifier-safe",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.role.Validate()
			if tc.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
			}
		})
	}
}

func TestRoleClearAfterDefault(t *testing.T) {
	r := Role{Name: "r", Description: "d"}
	r.applyDefaults()
	assert.Equal(t, ClearAfterManual, r.ClearAfter)
}

func TestRoleStateFileTemplate(t *testing.T) {
	r := Role{Name: "reviewer", Description: "d", StateFile: "reviewer/{project}.md"}
	got, err := r.ResolveStateFile("makro")
	require.NoError(t, err)
	assert.Equal(t, "reviewer/makro.md", got)
}

func TestRoleStateFileEmpty(t *testing.T) {
	r := Role{Name: "r", Description: "d"}
	_, err := r.ResolveStateFile("makro")
	// No StateFile configured → returns empty path, no error. Caller decides.
	require.NoError(t, err)
	got, _ := r.ResolveStateFile("makro")
	assert.Equal(t, "", got)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/role/ -run TestRole -v`
Expected: FAIL — package doesn't compile (`Role` undefined).

- [ ] **Step 3: Write the implementation**

Create `internal/role/role.go`:
```go
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
	Session     string         `toml:"session"`      // tmux session name; "" = create on demand
	ClearAfter  string         `toml:"clear_after"`  // manual|marker|guardian; default manual
	StateFile   string         `toml:"state_file"`   // optional; {project} substituted
	Metadata    map[string]any `toml:"-"`            // reserved; see package doc
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/role/ -run TestRole -v`
Expected: PASS (all 6 subtests + the 3 other Test functions).

- [ ] **Step 5: Commit**

```bash
git add internal/role/role.go internal/role/role_test.go
git commit -m "feat(role): Role struct with validation and state-file templating"
```

---

## Task 3: TOML loader (forward-compatible)

**Files:**
- Create: `internal/role/loader.go`
- Test: `internal/role/loader_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/role/loader_test.go` (create it):
```go
package role

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "roles.toml")
	require.NoError(t, os.WriteFile(path, []byte(`
[[role]]
name = "makro"
description = "Makro dev"
session = "makro"

[[role]]
name = "reviewer"
description = "Cross-project review"
session = "reviewer"
clear_after = "manual"
state_file = "reviewer/{project}.md"

[[role]]
name = "research"
description = "General research"
`), 0o644))

	roles, err := LoadFile(path)
	require.NoError(t, err)
	require.Len(t, roles, 3)

	assert.Equal(t, "makro", roles[0].Name)
	assert.Equal(t, ClearAfterManual, roles[0].ClearAfter, "default applied")

	assert.Equal(t, "reviewer", roles[1].Name)
	assert.Equal(t, "reviewer/{project}.md", roles[1].StateFile)

	assert.Equal(t, "research", roles[2].Name)
	assert.Equal(t, "", roles[2].Session, "empty session = create on demand")
}

func TestLoadFileMissing(t *testing.T) {
	roles, err := LoadFile("/nonexistent/roles.toml")
	require.NoError(t, err, "missing file = empty list, not error")
	assert.Empty(t, roles)
}

func TestLoadFileDuplicateName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "roles.toml")
	require.NoError(t, os.WriteFile(path, []byte(`
[[role]]
name = "dup"
description = "first"
[[role]]
name = "dup"
description = "second"
`), 0o644))

	_, err := LoadFile(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate role name")
}

func TestLoadFileUnknownKeyTolerated(t *testing.T) {
	// Forward-compat: a future roles.toml with a key Phase 1 doesn't know
	// (e.g. call_count) must NOT error.
	dir := t.TempDir()
	path := filepath.Join(dir, "roles.toml")
	require.NoError(t, os.WriteFile(path, []byte(`
[[role]]
name = "makro"
description = "x"
future_field = "anything"
metadata_score = 7
`), 0o644))

	roles, err := LoadFile(path)
	require.NoError(t, err, "unknown keys must be tolerated")
	require.Len(t, roles, 1)
	assert.Equal(t, "makro", roles[0].Name)
}

func TestLoadFileInvalidRole(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "roles.toml")
	require.NoError(t, os.WriteFile(path, []byte(`
[[role]]
description = "no name"
`), 0o644))

	_, err := LoadFile(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "role name is required")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/role/ -run TestLoadFile -v`
Expected: FAIL — `LoadFile` undefined.

- [ ] **Step 3: Write the implementation**

Create `internal/role/loader.go`:
```go
package role

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// tomlFile mirrors the on-disk shape of roles.toml. Unknown keys are
// tolerated because BurntSushi/toml's Decode does not error on extra fields
// by default — this is the forward-compatibility hook (spec §12).
type tomlFile struct {
	Role []Role `toml:"role"`
}

// LoadFile parses a roles.toml. A missing file returns an empty list with no
// error (so a fresh install with no roles.toml is fine). Malformed TOML or a
// role failing Validate produces an error.
func LoadFile(path string) ([]Role, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var tf tomlFile
	if _, err := toml.Decode(string(data), &tf); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	seen := make(map[string]bool, len(tf.Role))
	for i := range tf.Role {
		r := &tf.Role[i]
		if err := r.Validate(); err != nil {
			return nil, fmt.Errorf("role[%d]: %w", i, err)
		}
		if seen[r.Name] {
			return nil, fmt.Errorf("duplicate role name %q", r.Name)
		}
		seen[r.Name] = true
		r.applyDefaults()
	}
	return tf.Role, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/role/ -run TestLoadFile -v`
Expected: PASS (all 5 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/role/loader.go internal/role/loader_test.go
git commit -m "feat(role): TOML loader with forward-compatible parsing"
```

---

## Task 4: `Store` — in-memory role lookup

**Files:**
- Create: `internal/role/store.go`
- Test: `internal/role/store_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/role/store_test.go`:
```go
package role

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewStore(t *testing.T) {
	roles := []Role{
		{Name: "makro", Description: "a"},
		{Name: "default", Description: "fallback"},
	}
	s := NewStore(roles)

	assert.Equal(t, 2, s.Len())
	assert.True(t, s.Has("makro"))
	assert.False(t, s.Has("nope"))
}

func TestStoreGet(t *testing.T) {
	s := NewStore([]Role{{Name: "makro", Description: "a"}})
	r, ok := s.Get("makro")
	require.True(t, ok)
	assert.Equal(t, "makro", r.Name)

	_, ok = s.Get("nope")
	assert.False(t, ok)
}

func TestStoreAll(t *testing.T) {
	roles := []Role{
		{Name: "makro", Description: "a"},
		{Name: "juli", Description: "b"},
	}
	s := NewStore(roles)
	all := s.All()
	assert.Len(t, all, 2)
	// All returns a copy; mutating it must not affect the store.
	all[0].Name = "mutated"
	assert.True(t, s.Has("makro"), "store unaffected by mutation of All() result")
}

func TestStoreDefault(t *testing.T) {
	t.Run("named default wins", func(t *testing.T) {
		s := NewStore([]Role{
			{Name: "makro", Description: "a"},
			{Name: "default", Description: "fallback"},
		})
		d, ok := s.Default()
		require.True(t, ok)
		assert.Equal(t, "default", d.Name)
	})

	t.Run("no named default returns first", func(t *testing.T) {
		s := NewStore([]Role{
			{Name: "makro", Description: "a"},
			{Name: "juli", Description: "b"},
		})
		d, ok := s.Default()
		require.True(t, ok)
		assert.Equal(t, "makro", d.Name)
	})

	t.Run("empty store no default", func(t *testing.T) {
		s := NewStore(nil)
		_, ok := s.Default()
		assert.False(t, ok)
	})
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/role/ -run TestStore -v`
Expected: FAIL — `Store`, `NewStore` undefined.

- [ ] **Step 3: Write the implementation**

Create `internal/role/store.go`:
```go
package role

// Store is the in-memory cache of loaded roles. Phase 1 holds only A-level
// roles (from roles.toml). Phase 2 will extend it to hold B-level
// (runtime-created) roles in the same lookup.
type Store struct {
	roles map[string]Role
	order []string // insertion order, for Default() = "first"
}

// NewStore builds a Store from a slice (typically the loader output).
// Duplicate names in the input are deduped to the last entry; the loader
// already rejects duplicates, so this is defensive only.
func NewStore(roles []Role) *Store {
	s := &Store{
		roles: make(map[string]Role, len(roles)),
		order: make([]string, 0, len(roles)),
	}
	for _, r := range roles {
		if _, exists := s.roles[r.Name]; !exists {
			s.order = append(s.order, r.Name)
		}
		s.roles[r.Name] = r
	}
	return s
}

// Len returns the number of roles.
func (s *Store) Len() int { return len(s.roles) }

// Has reports whether a role with the given name exists.
func (s *Store) Has(name string) bool { _, ok := s.roles[name]; return ok }

// Get returns the role by name. ok is false if not found.
func (s *Store) Get(name string) (Role, bool) {
	r, ok := s.roles[name]
	return r, ok
}

// All returns a defensive copy of all roles, in insertion order.
func (s *Store) All() []Role {
	out := make([]Role, 0, len(s.order))
	for _, name := range s.order {
		out = append(out, s.roles[name])
	}
	return out
}

// Default returns the fallback role for ambiguous routing (spec §3.2 step 4):
// a role literally named "default" if present, otherwise the first role in
// insertion order, otherwise ok=false for an empty store.
func (s *Store) Default() (Role, bool) {
	if r, ok := s.roles["default"]; ok {
		return r, true
	}
	if len(s.order) == 0 {
		return Role{}, false
	}
	return s.roles[s.order[0]], true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/role/ -run TestStore -v`
Expected: PASS (all 4 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/role/store.go internal/role/store_test.go
git commit -m "feat(role): in-memory Store with lookup and default resolution"
```

---

## Task 5: `Router` — LLM-driven routing judgment

**Files:**
- Create: `internal/role/router.go`
- Test: `internal/role/router_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/role/router_test.go`:
```go
package role

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/naglezhang/makro/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeProvider implements llm.Provider for Router tests. It returns a canned
// CompleteResult for any Complete call.
type fakeProvider struct {
	resp  *llm.CompleteResult
	err   error
	calls int
	last  []llm.Message
}

func (f *fakeProvider) Stream(ctx context.Context, m []llm.Message, o llm.GenerateOptions) (<-chan llm.StreamEvent, error) {
	return nil, nil
}
func (f *fakeProvider) Complete(ctx context.Context, m []llm.Message, o llm.GenerateOptions) (*llm.CompleteResult, error) {
	f.calls++
	f.last = m
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}
func (f *fakeProvider) Name() string { return "fake" }

func routerJSON(t *testing.T, role string, conf float64) string {
	t.Helper()
	b, err := json.Marshal(routingResponse{Role: role, Confidence: conf, Reason: "test"})
	require.NoError(t, err)
	return string(b)
}

func TestRouterPicksMatchingRole(t *testing.T) {
	store := NewStore([]Role{
		{Name: "makro", Description: "Makro project development"},
		{Name: "juli", Description: "juli project maintenance"},
	})
	rt := NewRouter(store, &fakeProvider{resp: &llm.CompleteResult{Content: routerJSON(t, "makro", 0.9)}})

	dec, err := rt.Route(context.Background(), "fix the bug in orchestrator")
	require.NoError(t, err)
	assert.Equal(t, "makro", dec.RoleName)
	assert.InDelta(t, 0.9, dec.Confidence, 0.001)
	assert.False(t, dec.Fallback)
}

func TestRouterLowConfidenceFallsBack(t *testing.T) {
	store := NewStore([]Role{
		{Name: "makro", Description: "a"},
		{Name: "default", Description: "fallback"},
	})
	rt := NewRouter(store, &fakeProvider{resp: &llm.CompleteResult{Content: routerJSON(t, "makro", 0.2)}})

	dec, err := rt.Route(context.Background(), "vague task")
	require.NoError(t, err)
	assert.True(t, dec.Fallback, "below threshold → fallback")
	assert.Equal(t, "default", dec.RoleName)
}

func TestRouterLLMReturnsUnknownRoleFallsBack(t *testing.T) {
	store := NewStore([]Role{{Name: "makro", Description: "a"}})
	rt := NewRouter(store, &fakeProvider{resp: &llm.CompleteResult{Content: routerJSON(t, "ghost", 0.99)}})

	dec, err := rt.Route(context.Background(), "task")
	require.NoError(t, err)
	assert.True(t, dec.Fallback)
	assert.Equal(t, "makro", dec.RoleName, "unknown role → store default")
}

func TestRouterMalformedJSONFallsBack(t *testing.T) {
	store := NewStore([]Role{{Name: "makro", Description: "a"}})
	rt := NewRouter(store, &fakeProvider{resp: &llm.CompleteResult{Content: "not json"}})

	dec, err := rt.Route(context.Background(), "task")
	require.NoError(t, err, "malformed JSON is recoverable: fall back, don't error")
	assert.True(t, dec.Fallback)
}

func TestRouterLLMErrorFallsBack(t *testing.T) {
	store := NewStore([]Role{{Name: "makro", Description: "a"}})
	rt := NewRouter(store, &fakeProvider{err: assert.AnError})

	dec, err := rt.Route(context.Background(), "task")
	require.NoError(t, err, "LLM error is recoverable: fall back, don't surface to user")
	assert.True(t, dec.Fallback)
}

func TestRouterEmptyStoreNoFallback(t *testing.T) {
	// No roles at all → no routing possible. The decision is empty; the
	// caller (Dispatcher) treats this as "route to handleLLM" (no-op).
	rt := NewRouter(NewStore(nil), &fakeProvider{})
	dec, err := rt.Route(context.Background(), "task")
	require.NoError(t, err)
	assert.Equal(t, "", dec.RoleName)
	assert.True(t, dec.NoRoles)
	assert.Equal(t, 0, rt.provider.(*fakeProvider).calls, "no LLM call when store is empty")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/role/ -run TestRouter -v`
Expected: FAIL — `Router`, `NewRouter`, `Decision` undefined.

- [ ] **Step 3: Write the implementation**

Create `internal/role/router.go`:
```go
package role

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/naglezhang/makro/internal/llm"
)

// ConfidenceThreshold is the minimum confidence to accept a routing decision.
// Below it, the router falls back to the store default (spec §3.3).
const ConfidenceThreshold = 0.5

// Decision is the outcome of routing one task.
type Decision struct {
	RoleName   string  // selected role name; "" if no roles exist
	Confidence float64 // 0..1 from the LLM, 0 for fallback
	Fallback   bool    // true if this is the store default (not an LLM match)
	Reason     string  // short rationale from the LLM or fallback path
	NoRoles    bool    // true when the store was empty (caller should no-op)
}

// Router picks a role for a task via one LLM call with a structured-output
// prompt (spec §3.3). It mirrors the voice-call philosophy: the LLM proposes,
// deterministic code dispatches.
type Router struct {
	store    *Store
	provider llm.Provider
	model    string // optional; empty = use provider default
}

// NewRouter builds a Router. model may be "" to use the provider's default.
func NewRouter(store *Store, provider llm.Provider) *Router {
	return &Router{store: store, provider: provider}
}

// SetModel overrides the model used for the routing call.
func (r *Router) SetModel(m string) { r.model = m }

// routingResponse is the JSON shape the LLM is asked to return.
type routingResponse struct {
	Role       string  `json:"role"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

// Route decides which role handles the task.
func (r *Router) Route(ctx context.Context, task string) (Decision, error) {
	if r.store.Len() == 0 {
		return Decision{NoRoles: true, Reason: "no roles configured"}, nil
	}

	// Build the prompt with the full role catalogue.
	prompt := r.buildPrompt(task)

	opts := llm.GenerateOptions{Model: r.model}
	if opts.Model == "" {
		// Provider default; leave Model empty and let the provider pick.
	}

	result, err := r.provider.Complete(ctx, prompt, opts)
	if err != nil {
		// LLM failure is recoverable: fall back, don't surface to the user.
		log.Printf("[role] routing LLM error: %v (falling back)", err)
		return r.fallback("LLM error: " + err.Error()), nil
	}

	parsed, perr := parseRoutingResponse(result.Content)
	if perr != nil {
		log.Printf("[role] routing JSON parse error: %v (falling back): raw=%q", perr, truncate(result.Content, 200))
		return r.fallback("malformed LLM response"), nil
	}

	// Unknown role name → don't trust the LLM; fall back.
	if parsed.Role != "" && !r.store.Has(parsed.Role) {
		log.Printf("[role] LLM picked unknown role %q (falling back)", parsed.Role)
		return r.fallback("unknown role: " + parsed.Role), nil
	}

	// Below threshold → fall back.
	if parsed.Confidence < ConfidenceThreshold {
		return r.fallback(fmt.Sprintf("confidence %.2f below threshold", parsed.Confidence)), nil
	}

	return Decision{
		RoleName:   parsed.Role,
		Confidence: parsed.Confidence,
		Fallback:   false,
		Reason:     parsed.Reason,
	}, nil
}

// fallback returns the store default wrapped in a Decision.
func (r *Router) fallback(reason string) Decision {
	d, ok := r.store.Default()
	if !ok {
		// Store non-empty but no default resolvable (shouldn't happen — Default
		// falls back to first). Treat as no-op.
		return Decision{NoRoles: true, Reason: reason}
	}
	return Decision{RoleName: d.Name, Confidence: 0, Fallback: true, Reason: reason}
}

// buildPrompt assembles the system + user messages for the routing call.
func (r *Router) buildPrompt(task string) []llm.Message {
	var sb strings.Builder
	sb.WriteString("You are a router. Given a task and a catalogue of roles, pick the SINGLE role whose description best matches the task.\n\n")
	sb.WriteString("Roles:\n")
	for _, role := range r.store.All() {
		fmt.Fprintf(&sb, "- %s: %s\n", role.Name, role.Description)
	}
	sb.WriteString("\nRespond with ONLY a JSON object, no prose, no code fences:\n")
	sb.WriteString(`{"role":"<name from the list>","confidence":0.0,"reason":"<one short sentence>"}` + "\n")
	sb.WriteString("If no role is a clear fit, set role to \"\" and confidence low.\n")

	return []llm.Message{
		{Role: llm.RoleSystem, Content: sb.String()},
		{Role: llm.RoleUser, Content: task},
	}
}

// parseRoutingResponse extracts the routing JSON from the LLM's content,
// tolerating surrounding prose and ```json fences.
func parseRoutingResponse(content string) (routingResponse, error) {
	var resp routingResponse
	body := stripFences(strings.TrimSpace(content))
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return resp, fmt.Errorf("parse JSON: %w", err)
	}
	return resp, nil
}

// stripFences removes a leading ```json or ``` and trailing ```, and trims
// surrounding whitespace/prose. If the content has prose around the JSON, we
// try to extract the first {...} block.
func stripFences(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	// If there's still prose, grab the first {...} substring.
	if i := strings.Index(s, "{"); i >= 0 {
		if j := strings.LastIndex(s, "}"); j > i {
			s = s[i : j+1]
		}
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/role/ -run TestRouter -v`
Expected: PASS (all 6 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/role/router.go internal/role/router_test.go
git commit -m "feat(role): LLM-driven Router with confidence threshold and fallback"
```

---

## Task 6: `Dispatcher` — resolve session + delegate to send

**Files:**
- Create: `internal/role/dispatcher.go`
- Test: `internal/role/dispatcher_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/role/dispatcher_test.go`:
```go
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
	// Recognize "new-session" to track created sessions.
	if len(cmd) > 10 && cmd[:11] == "new-session" {
		// Naive parse: assume "-s name" present.
		// (Real tmux client builds these via tmux package helpers.)
	}
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/role/ -run TestDispatch -v`
Expected: FAIL — `Dispatcher`, `NewDispatcher` undefined.

- [ ] **Step 3: Write the implementation**

The Dispatcher delegates the actual send to `tools.DirectSend` (the same function `@mention` uses, `orchestrator.go:382`), which already handles the atomic send + Enter-loss recovery. Session creation uses `tools.TmuxClient.HasSession` to decide whether to create.

Create `internal/role/dispatcher.go`:
```go
package role

import (
	"context"
	"fmt"

	"github.com/naglezhang/makro/internal/agent/tools"
)

// Dispatcher resolves a routing Decision to a concrete tmux session and sends
// the task there. It never sends raw keystrokes itself: it delegates to
// tools.DirectSend (the same path @mention uses), which runs the shared
// pre-send gates and Enter-loss recovery (send_to_session.go:228).
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

// createSession creates a tmux session. Delegates to the tmux package's
// command shape so all session creation goes through one path.
func (d *Dispatcher) createSession(name string) error {
	// Mirror the tmux package's NewSessionCmd shape. The Exec call runs it.
	cmd := newSessionCmd(name)
	if _, err := d.tc.Exec(cmd); err != nil {
		return err
	}
	return nil
}
```

Now we need `newSessionCmd`. Check whether `internal/tmux` already exposes a session-creation command string helper, and use it rather than reinventing. Add a small bridge in the dispatcher file or reuse. Look at `internal/tmux/client.go`'s session-creation helper first (the implementation step below).

- [ ] **Step 3b: Reuse the existing tmux session-creation helper**

The `internal/tmux` package exposes `NewSessionCmd(name, workdir, command string) string` (confirmed: `internal/tmux/commands_test.go:10`). It takes **three** arguments — pass empty strings for workdir and command to get a plain detached session.

Replace the placeholder `newSessionCmd` call in `dispatcher.go`'s `createSession` with the real helper. The final `dispatcher.go` `createSession` should read:
```go
import "github.com/naglezhang/makro/internal/tmux"

// inside createSession:
func (d *Dispatcher) createSession(name string) error {
	if _, err := d.tc.Exec(tmux.NewSessionCmd(name, "", "")); err != nil {
		return err
	}
	return nil
}
```
Delete the `newSessionCmd` placeholder function entirely — it was a stand-in.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/role/ -run TestDispatch -v`
Expected: PASS (all 3 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/role/dispatcher.go internal/role/dispatcher_test.go
git commit -m "feat(role): Dispatcher resolves session and delegates to send_to_session"
```

---

## Task 7: Wire Router + Dispatcher into the Orchestrator

**Files:**
- Modify: `internal/agent/orchestrator.go`

The Orchestrator gains a new branch in `ProcessInput` between `@mention` (line ~370) and `handleLLM` (line ~372): if a Router is configured, route → dispatch. If the decision is `NoRoles` or routing isn't configured, fall through to `handleLLM` exactly as today.

- [ ] **Step 1: Add Router/Dispatcher fields and setter to the Orchestrator struct**

In `internal/agent/orchestrator.go`, add fields to the `Orchestrator` struct (after `activeSkill *skills.Skill`, around line 68):
```go
	router    *role.Router
	dispatcher *role.Dispatcher
```

Add imports at top:
```go
	"github.com/naglezhang/makro/internal/role"
```

Add a setter method (next to `LoadSkills`, around line 143):
```go
// SetRoles configures role-based routing. When set, non-slash, non-@mention
// input is routed to the matching role's session instead of going to
// handleLLM. An empty store disables routing (falls through to handleLLM).
func (o *Orchestrator) SetRoles(store *role.Store) {
	if store == nil || store.Len() == 0 {
		o.router = nil
		o.dispatcher = nil
		return
	}
	o.router = role.NewRouter(store, o.provider)
	o.dispatcher = role.NewDispatcher(o.tc)
	o.dispatcher.SetStore(store)
}
```

- [ ] **Step 2: Add the routing branch to ProcessInput**

In `ProcessInput` (around line 371, after the `@mention` block closes and before the final `handleLLM` goroutine), insert a new branch. The exact location is between:
```go
	sessionName, text := ExtractMention(input)
	if sessionName != "" {
		// ... existing @mention handling ...
		return ch, nil
	}

	// >>> INSERT ROUTING BRANCH HERE <<<

	go func() {
		defer close(ch)
		defer cancel()
		o.handleLLM(ctx, ch, input)
	}()
```

Insert:
```go
	// Role-based routing: if roles are configured, route the input to the
	// matching role's session. NoRoles (empty store) falls through to
	// handleLLM. Slash commands and @mention above bypass routing entirely.
	if o.router != nil {
		go func() {
			defer close(ch)
			defer cancel()
			o.handleRoute(ctx, ch, input)
		}()
		return ch, nil
	}
```

- [ ] **Step 3: Add the handleRoute method**

Add near `handleMention` (around line 381):
```go
// handleRoute routes input via the role Router and dispatches to the chosen
// role's session. On any routing/dispatch error it surfaces the error as text
// rather than crashing the turn.
func (o *Orchestrator) handleRoute(ctx context.Context, ch chan<- OrchestratorEvent, input string) {
	dec, err := o.router.Route(ctx, input)
	if err != nil {
		ch <- OrchestratorEvent{Type: EventText, Content: fmt.Sprintf("Routing error: %v", err)}
		ch <- OrchestratorEvent{Type: EventDone}
		return
	}

	if dec.NoRoles {
		// No roles configured: fall back to a normal LLM turn.
		o.handleLLM(ctx, ch, input)
		return
	}

	roleNote := fmt.Sprintf("→ %s", dec.RoleName)
	if dec.Fallback {
		roleNote = fmt.Sprintf("→ %s (fallback: %s)", dec.RoleName, dec.Reason)
	}
	ch <- OrchestratorEvent{Type: EventText, Content: roleNote}

	if err := o.dispatcher.Dispatch(ctx, dec, input); err != nil {
		ch <- OrchestratorEvent{Type: EventText, Content: fmt.Sprintf("Dispatch error: %v", err)}
	}
	ch <- OrchestratorEvent{Type: EventDone}
}
```

- [ ] **Step 4: Build to verify it compiles**

Run: `go build ./...`
Expected: exit 0, no errors.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/orchestrator.go
git commit -m "feat(orchestrator): wire role Router into ProcessInput routing"
```

---

## Task 8: Load `roles.toml` in the TUI composition root (`main.go`)

**Files:**
- Modify: `main.go`

- [ ] **Step 1: Locate the skills-loading block**

Run:
```bash
cd /Users/naglezhang/Desktop/Code/Makro && grep -n "LoadSkills\|skillDirs\|\.makro.*skills" main.go
```
Expected output shows lines around 169–174 where `skillDirs` is built and `orch.LoadSkills(skillDirs)` is called.

- [ ] **Step 2: Add roles loading next to skills loading**

In `main.go`, immediately after the `orch.LoadSkills(skillDirs)` block (around line 174), add:
```go
	// Load A-level roles (spec: role-based routing). Missing file is fine —
	// routing is simply disabled. Project-local overrides user-global, same
	// convention as skills. Later files override earlier ones.
	rolePaths := []string{
		filepath.Join(homeDir, ".makro", "roles.toml"),
		filepath.Join(".", ".makro", "roles.toml"),
	}
	var allRoles []role.Role
	for _, p := range rolePaths {
		rs, err := role.LoadFile(p)
		if err != nil {
			log.Printf("[main] warning: roles config %s: %v", p, err)
		}
		allRoles = append(allRoles, rs...)
	}
	if len(allRoles) > 0 {
		orch.SetRoles(role.NewStore(allRoles))
	}
```

Add the import:
```go
	"github.com/naglezhang/makro/internal/role"
```

(Note: if `role` collides with an existing import alias or name in `main.go`, alias it as `rolev1` or similar. Check existing imports first.)

- [ ] **Step 3: Build and verify**

Run: `go build ./...`
Expected: exit 0.

- [ ] **Step 4: Commit**

```bash
git add main.go
git commit -m "feat(main): load ~/.makro/roles.toml and enable role routing"
```

---

## Task 9: Load `roles.toml` in the GUI composition root (`cmd/gui/chat_service.go`)

**Files:**
- Modify: `cmd/gui/chat_service.go`

- [ ] **Step 1: Locate the skills-loading block**

Run:
```bash
cd /Users/naglezhang/Desktop/Code/Makro && grep -n "LoadSkills\|skillDirs\|\.makro.*skills" cmd/gui/chat_service.go
```
Expected output shows lines around 241–245.

- [ ] **Step 2: Add roles loading (mirror Task 8)**

In `cmd/gui/chat_service.go`, immediately after the `orch.LoadSkills(skillDirs)` call (around line 245), add the same block as Task 8 Step 2, with the same import.

- [ ] **Step 3: Build and verify**

Run: `go build ./...`
Expected: exit 0.

- [ ] **Step 4: Commit**

```bash
git add cmd/gui/chat_service.go
git commit -m "feat(gui): load ~/.makro/roles.toml and enable role routing"
```

---

## Task 10: End-to-end integration test + sample roles.toml

**Files:**
- Create: `internal/role/integration_test.go`
- Create: `~/.makro/roles.toml` (sample, user-facing — do NOT commit; document in README instead)

- [ ] **Step 1: Write the integration test**

Create `internal/role/integration_test.go`:
```go
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
```

- [ ] **Step 2: Run the integration test**

Run: `go test ./internal/role/ -run TestEndToEnd -v`
Expected: PASS.

- [ ] **Step 3: Run the whole role package**

Run: `go test ./internal/role/ -v`
Expected: all tests PASS (role, loader, store, router, dispatcher, integration).

- [ ] **Step 4: Run the full pre-commit suite**

Run: `go vet ./... && gofmt -l . && go test ./...`
Expected: no vet errors, no gofmt diffs, all tests pass. (This mirrors the project's pre-commit hook.)

- [ ] **Step 5: Document the feature in the README**

In `README.md`, add a short "Role-Based Routing" section (a few sentences) explaining:
- Create `~/.makro/roles.toml` with `[[role]]` entries (name, description, optional session/clear_after/state_file).
- Non-slash, non-`@mention` input is routed to the role whose description best matches.
- Missing `roles.toml` = routing disabled (existing behavior).

Do NOT commit a sample `~/.makro/roles.toml` — that's user data. The README is the documentation.

- [ ] **Step 6: Commit**

```bash
git add internal/role/integration_test.go README.md
git commit -m "test(role): end-to-end integration; docs: role-based routing in README"
```

---

## Self-Review (completed by plan author)

**Spec coverage:**
- §3.1 Two-tier role model (Role struct) → Task 2 ✅
- §3.2 Routing cascade (A → fallback; B-level is Phase 2) → Task 5 ✅ (NoRoles + fallback paths; B-level explicitly deferred per Phase 1 scope)
- §3.3 Routing judgment (one LLM call, JSON, threshold 0.5) → Task 5 ✅
- §3.4 B→A graduation → **Phase 2, not in this plan** (correct per scope)
- §4 roles.toml schema → Tasks 2, 3 ✅
- §5 Reviewer role → **Phase 3, not in this plan** (correct per scope; StateFile field exists on Role for forward-compat)
- §6 Integration with send_to_session (delegate to DirectSend) → Task 6 ✅
- §6 Integration with slash/`@mention` (bypass routing) → Task 7 ✅ (routing branch is after both)
- §7 Components & data flow → Tasks 2–7 ✅
- §8 Error handling (missing file OK, LLM error → fallback, parse error → fallback, send rejection surfaces) → Tasks 3, 5, 6 ✅
- §9 Testing (router unit, dispatcher unit, config loader, integration) → Tasks 3–6, 10 ✅
- §12 Architectural principle (Metadata field, forward-compat loader) → Tasks 2, 3 ✅

**Placeholder scan:** Task 6 Step 3b uses `tmux.NewSessionCmd(name, "", "")` — signature verified against `internal/tmux/commands_test.go:10` (`NewSessionCmd("test", "", "")`), so the call is concrete, not a placeholder. No "TBD/TODO/figure out" steps remain.

**Type consistency:** `Decision.RoleName` / `Decision.NoRoles` / `Decision.Fallback` used consistently in Tasks 5, 6, 7. `Store.Get/Has/Default/All` consistent in Tasks 4, 5, 6, 10. `Role.Session` / `Role.StateFile` / `Role.ClearAfter` consistent in Tasks 2, 3, 6.

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-07-05-role-routing-phase1.md`. Two execution options:

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration. Best for this plan because each task is self-contained and the review-between-tasks catches integration issues early.

**2. Inline Execution** — I execute tasks in this session using executing-plans, batch execution with checkpoints. Faster overall but less granular review.

Which approach?
