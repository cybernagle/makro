# Role Routing v2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the v1 Router+Dispatcher (which bypassed the main conversation) with roles injected into the system prompt, so the main conversation LLM dispatches via the existing `send_to_session` tool.

**Architecture:** Delete `internal/role/router.go`, `dispatcher.go`, their tests, and the orchestrator's routing branch. Add `renderRolesPrompt` to the `role` package and rewrite `SetRoles` to append the rendered roles section to `o.systemPrompt`. `ProcessInput` returns to its original three-branch form (slash / @mention / handleLLM).

**Tech Stack:** Go 1.26, testify.

**Spec:** `docs/superpowers/specs/2026-07-05-role-routing-v2-design.md`

**Branch:** `refactor/role-routing-v2` (already created; spec already committed at `33090ff`)

---

## File Structure

| File | Action | Responsibility |
|---|---|---|
| `internal/role/prompt.go` | **Create** | `RenderRolesPrompt(store *Store) string` — renders roles into the "Managed sessions" prompt section. |
| `internal/role/prompt_test.go` | **Create** | Tests for `RenderRolesPrompt`. |
| `internal/role/router.go` | **Delete** | v1 routing LLM call — gone. |
| `internal/role/router_test.go` | **Delete** | Tests for the deleted router. |
| `internal/role/dispatcher.go` | **Delete** | v1 dispatcher — gone. |
| `internal/role/dispatcher_test.go` | **Delete** | Tests for the deleted dispatcher. |
| `internal/role/integration_test.go` | **Delete** | End-to-end test wiring router→dispatcher — both gone. |
| `internal/agent/orchestrator.go` | **Modify** | Remove router/dispatcher fields, `handleRoute`, the routing branch in `ProcessInput`; rewrite `SetRoles(store)` to append the roles section to `o.systemPrompt`. |
| `internal/agent/tools/send_to_session.go` | **Modify** | Remove the `SafeSend` function (dead code after dispatcher deletion). |
| `main.go` | **Modify** | `SetRoles(store)` call signature drops the notifier arg. |
| `cmd/gui/chat_service.go` | **Modify** | Same signature change. |

`role.go`, `loader.go`, `store.go`, and their tests stay unchanged — they're the config layer v2 keeps.

---

## Task 1: `RenderRolesPrompt` (the one new function)

**Files:**
- Create: `internal/role/prompt.go`
- Create: `internal/role/prompt_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/role/prompt_test.go`:
```go
package role

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderRolesPromptEmpty(t *testing.T) {
	got := RenderRolesPrompt(NewStore(nil))
	assert.Equal(t, "", got, "empty store renders empty string")
}

func TestRenderRolesPromptContainsAllRoles(t *testing.T) {
	store := NewStore([]Role{
		{Name: "review", Description: "代码 review、PR review", Session: "review"},
		{Name: "dev", Description: "Makro 项目的开发", Session: "dev"},
		{Name: "research", Description: "通用调研", Session: ""}, // empty session
	})
	got := RenderRolesPrompt(store)

	// Every role's name, session (or name when empty), and description appears.
	assert.Contains(t, got, "review")
	assert.Contains(t, got, "代码 review、PR review")
	assert.Contains(t, got, "dev")
	assert.Contains(t, got, "Makro 项目的开发")
	// Empty session renders as the role name.
	assert.Contains(t, got, "research")

	// The sentinel marker is present (SetRoles uses it for idempotency).
	assert.True(t, strings.Contains(got, rolesPromptMarker), "rendered prompt must contain the sentinel marker %q", rolesPromptMarker)
}

func TestRenderRolesPromptFormat(t *testing.T) {
	store := NewStore([]Role{
		{Name: "review", Description: "review tasks", Session: "review"},
	})
	got := RenderRolesPrompt(store)

	// Spot-check the structural elements: a header line and a bullet per role.
	assert.Contains(t, got, "session:")
	assert.Contains(t, got, "- review")
}

func TestRolesPromptMarkerAccessor(t *testing.T) {
	m := RolesPromptMarker()
	assert.NotEmpty(t, m, "marker must not be empty")
	assert.Equal(t, rolesPromptMarker, m, "accessor returns the package const")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/role/ -run TestRenderRolesPrompt -v`
Expected: FAIL — `RenderRolesPrompt` and `rolesPromptMarker` undefined.

- [ ] **Step 3: Write the implementation**

Create `internal/role/prompt.go`:
```go
package role

import (
	"fmt"
	"strings"
)

// rolesPromptMarker wraps the roles section so SetRoles can detect whether it
// has already been appended to the orchestrator's system prompt (idempotency).
// It must appear exactly once in any prompt that includes roles.
const rolesPromptMarker = "<!-- makro:roles -->"

// RenderRolesPrompt renders the store's roles into a "Managed sessions" section
// suitable for appending to the orchestrator's system prompt. Returns "" for
// an empty store (the caller treats empty as "append nothing").
//
// The section is wrapped in rolesPromptMarker so the orchestrator can detect a
// double-append. Format:
//
//     <!-- makro:roles -->
//     You manage the following tmux sessions, each with a role describing what
//     it handles. When the user's task matches a role, send it to that session
//     with send_to_session. If no role clearly fits, handle the task yourself.
//
//     - review (session: review): 代码 review、PR review
//     - dev (session: dev): Makro 项目的开发
//     - research (session: research): 通用调研
//     <!-- makro:roles -->
func RenderRolesPrompt(store *Store) string {
	if store == nil || store.Len() == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(rolesPromptMarker + "\n")
	sb.WriteString("You manage the following tmux sessions, each with a role describing what it handles. ")
	sb.WriteString("When the user's task matches a role, send it to that session with the send_to_session tool. ")
	sb.WriteString("If no role clearly fits, handle the task yourself in this conversation.\n\n")
	for _, r := range store.All() {
		session := r.Session
		if session == "" {
			session = r.Name
		}
		fmt.Fprintf(&sb, "- %s (session: %s): %s\n", r.Name, session, r.Description)
	}
	sb.WriteString(rolesPromptMarker)
	return sb.String()
}

// RolesPromptMarker returns the sentinel string that wraps a rendered roles
// section. The orchestrator's SetRoles uses it to detect whether the roles
// section has already been appended to a system prompt (idempotency).
func RolesPromptMarker() string {
	return rolesPromptMarker
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/role/ -run "TestRenderRolesPrompt|TestRolesPromptMarker" -v`
Expected: PASS (all 4 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/role/prompt.go internal/role/prompt_test.go
git commit -m "feat(role): RenderRolesPrompt + RolesPromptMarker for system-prompt injection"
```

---

## Task 2: Rewrite `SetRoles` and remove the routing branch in the orchestrator

**Files:**
- Modify: `internal/agent/orchestrator.go`

This task has three sub-edits in one file: rewrite `SetRoles`, delete `handleRoute`, delete the routing branch in `ProcessInput`, and remove the `router`/`dispatcher` struct fields.

- [ ] **Step 1: Remove the router/dispatcher struct fields**

In `internal/agent/orchestrator.go`, find the `Orchestrator struct` (around line 52). Delete these two fields:
```go
	router     *role.Router
	dispatcher *role.Dispatcher
```

- [ ] **Step 2: Rewrite `SetRoles`**

Replace the existing `SetRoles(store *role.Store, notifier tools.Notifier)` method (starts ~line 167) with:
```go
// SetRoles injects role knowledge into the orchestrator's system prompt. The
// main conversation LLM reads the rendered roles section and dispatches to the
// matching session via the existing send_to_session tool — there is no separate
// router or dispatcher. An empty/nil store is a no-op (base prompt unchanged).
//
// Idempotent: if the system prompt already contains the roles marker, this is
// a no-op. Call after SetSystemPrompt; both composition roots already do.
func (o *Orchestrator) SetRoles(store *role.Store) {
	if store == nil || store.Len() == 0 {
		return
	}
	section := role.RenderRolesPrompt(store)
	if section == "" || strings.Contains(o.systemPrompt, role.RolesPromptMarker()) {
		return
	}
	o.systemPrompt = o.systemPrompt + "\n\n" + section
}
```

Note this references `role.RolesPromptMarker()` (the accessor defined in Task 1) and `strings.Contains`. The `strings` import is already present in orchestrator.go (verify; if not, add it).

- [ ] **Step 3: Delete `handleRoute`**

Delete the entire `handleRoute` method (the block starting `func (o *Orchestrator) handleRoute(ctx context.Context, ch chan<- OrchestratorEvent, input string) {` through its closing brace).

- [ ] **Step 4: Delete the routing branch in `ProcessInput`**

In `ProcessInput`, find the block:
```go
	if o.router != nil {
		go func() {
			defer close(ch)
			defer cancel()
			o.handleRoute(ctx, ch, input)
		}()
		return ch, nil
	}
```
Delete it entirely. `ProcessInput` now flows: slash → @mention → `handleLLM` (the original three-branch form).

- [ ] **Step 5: Verify build compiles**

Run: `go build ./internal/agent/`
Expected: exit 0. Task 1 already defined `RolesPromptMarker()`, so the accessor resolves. The `role.Router`/`role.Dispatcher` types still exist until Task 3 deletes them, but no orchestrator code references them after Step 1+3+4.

If the build fails because `role.Router`/`role.Dispatcher` are still referenced anywhere in orchestrator.go, grep and remove the stray reference:
```bash
grep -n "role.Router\|role.Dispatcher\|o.router\|o.dispatcher\|handleRoute" internal/agent/orchestrator.go
```
Expected: no matches.

- [ ] **Step 6: Commit**

```bash
git add internal/agent/orchestrator.go
git commit -m "refactor(orchestrator): remove routing branch; SetRoles injects roles into prompt"
```

Note: this commit compiles cleanly because Task 1 already defined `RolesPromptMarker()`. The build will not break between commits.

---

## Task 3: Delete v1 router + dispatcher + integration test

**Files:**
- Delete: `internal/role/router.go`
- Delete: `internal/role/router_test.go`
- Delete: `internal/role/dispatcher.go`
- Delete: `internal/role/dispatcher_test.go`
- Delete: `internal/role/integration_test.go`

- [ ] **Step 1: Delete the files**

```bash
git rm internal/role/router.go internal/role/router_test.go \
       internal/role/dispatcher.go internal/role/dispatcher_test.go \
       internal/role/integration_test.go
```

- [ ] **Step 2: Verify the role package still compiles and tests pass**

Run: `go test ./internal/role/ -v`
Expected: all remaining tests PASS (role_test, loader_test, store_test, prompt_test). No references to `Router`, `Dispatcher`, `Decision`, `fakeProvider`, or `fakeTmux` remain.

- [ ] **Step 3: Verify no stray references repo-wide**

Run:
```bash
grep -rn "role.Router\|role.Dispatcher\|role.NewRouter\|role.NewDispatcher\|role.Decision" --include="*.go" .
```
Expected: no matches.

- [ ] **Step 4: Commit**

```bash
git commit -m "refactor(role): delete v1 router, dispatcher, and integration test

Removed by v2 design: the main conversation now dispatches via
send_to_session directly, reading roles from the system prompt."
```

---

## Task 4: Remove `SafeSend` (dead code)

**Files:**
- Modify: `internal/agent/tools/send_to_session.go`

- [ ] **Step 1: Locate and delete `SafeSend`**

Run:
```bash
grep -n "func SafeSend" internal/agent/tools/send_to_session.go
```

Delete the entire `SafeSend` function (the block starting `// SafeSend is the gated send path shared by every autonomous send route` through its closing brace, immediately before `DirectSend`).

- [ ] **Step 2: Verify no references remain**

Run:
```bash
grep -rn "tools.SafeSend\|SafeSend(" --include="*.go" .
```
Expected: no matches.

- [ ] **Step 3: Build + test**

Run: `go build ./... && go test ./internal/agent/tools/`
Expected: build exit 0, tests PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/agent/tools/send_to_session.go
git commit -m "refactor(tools): remove SafeSend (dead code after v1 dispatcher deletion)"
```

---

## Task 5: Update composition roots (`SetRoles` signature)

**Files:**
- Modify: `main.go`
- Modify: `cmd/gui/chat_service.go`

`SetRoles` lost its `notifier` parameter in Task 2. Both composition roots pass it; update them.

- [ ] **Step 1: Update `main.go`**

In `main.go`, find (around line 194):
```go
		orch.SetRoles(rolepkg.NewStore(allRoles), notifier)
```
Replace with:
```go
		orch.SetRoles(rolepkg.NewStore(allRoles))
```

- [ ] **Step 2: Update `cmd/gui/chat_service.go`**

In `cmd/gui/chat_service.go`, find (around line 264):
```go
		orch.SetRoles(role.NewStore(allRoles), notifier)
```
Replace with:
```go
		orch.SetRoles(role.NewStore(allRoles))
```

- [ ] **Step 3: Build + full test**

Run: `go build ./... && go test ./...`
Expected: build exit 0, all tests PASS.

- [ ] **Step 4: Commit**

```bash
git add main.go cmd/gui/chat_service.go
git commit -m "refactor: update SetRoles call sites for the new (store-only) signature"
```

---

## Task 6: Update README to reflect v2

**Files:**
- Modify: `README.md`

The "Role-based routing" section currently describes v1 (cascade, fallback to default session, "Makro asks the LLM which role best matches and routes"). v2 is simpler: roles are context for the main conversation.

- [ ] **Step 1: Rewrite the routing description**

In `README.md`, find the "### Role-based routing" subsection (around line 175). Replace its first paragraph (the one starting "Declare named **roles**...") with:

```markdown
Declare named **roles** that tell the orchestrator which tmux session handles what. Roles are injected into the orchestrator's system prompt as context — the main conversation reads them and uses the existing `send_to_session` tool to route matching tasks. Missing or empty `roles.toml` means the orchestrator just gets no role context (it behaves as before).
```

- [ ] **Step 2: Update the "Routing cascade" line**

Find the line starting "**Routing cascade:**". Replace with:

```markdown
**How routing works:** roles are context, not a separate router. The orchestrator's main conversation reads the role list from its system prompt and decides — per task — whether to `send_to_session` to a matching role's session or handle the task inline. Slash commands and `@mention` always bypass this (they work exactly as before). A task with no clear role match is handled in the main conversation.
```

- [ ] **Step 3: Remove the now-misleading sample-config comment**

In the sample `roles.toml` block, the comment `# tmux session that already has a coding agent running` on the `session = "makro"` line is still accurate (Phase 1 doesn't auto-create). Keep it. But if anywhere says "create on demand" or references auto-creation, leave it removed (v1's README fix already did this — verify it's gone).

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "docs: update role-routing README section for v2 (roles as prompt context)"
```

---

## Task 7: Full verification + manual smoke test

**Files:** none (verification only)

This is the gate v1's deployment skipped. Do not skip it.

- [ ] **Step 1: Clean full test run**

Run:
```bash
go clean -testcache
go test ./...
go vet ./...
gofmt -l internal/ cmd/ main.go
```
Expected: all tests PASS, vet clean, gofmt lists nothing.

- [ ] **Step 2: Confirm the orchestrator package no longer imports router/dispatcher**

Run:
```bash
grep -n "role\." internal/agent/orchestrator.go | head
```
Expected: references only to `role.Store`, `role.RenderRolesPrompt`, `role.RolesPromptMarker` — no `role.Router` / `role.Dispatcher` / `role.NewRouter` / `role.NewDispatcher`.

- [ ] **Step 3: Confirm ProcessInput is back to three branches**

Run:
```bash
sed -n '/func (o \*Orchestrator) ProcessInput/,/^}/p' internal/agent/orchestrator.go | grep -E "strings.HasPrefix|ExtractMention|handleLLM|o.router"
```
Expected: matches for slash (`HasPrefix`), @mention (`ExtractMention`), `handleLLM`. **No** match for `o.router`.

- [ ] **Step 4: Manual smoke (after deploy, recorded in the PR)**

This step runs after Task 8's deploy. With `~/.makro/roles.toml` present:
1. Send "帮我 review PR #12" → expect the orchestrator calls `send_to_session` to the `review` session (visible as a tool call in the chat).
2. Send a vague task like "嗯今天天气不错" → expect the orchestrator replies inline, no dispatch.
3. Temporarily rename `roles.toml` → confirm orchestrator still works (no crash, no missing-prompt error).

Record the outcomes in the PR description.

---

## Task 8: Push, open PR

**Files:** none

- [ ] **Step 1: Push the branch**

```bash
git push -u origin refactor/role-routing-v2
```

- [ ] **Step 2: Open the PR**

```bash
gh pr create --base main --head refactor/role-routing-v2 \
  --title "refactor: role routing v2 — roles as prompt context (replaces v1 router)" \
  --body '...'
```

PR body covers: what changed (delete router/dispatcher, inject roles into prompt), why v1 was wrong (bypassed main conversation, fail-closed), the v1→v2 delta, test plan (incl. manual smoke), and that `~/.makro/roles.toml` keeps working unchanged.

- [ ] **Step 3: Build & deploy the .app** (per makro-build skill)

Follow the makro-build skill: Go binary → Vite → Electron .app → deploy to /Applications. Then run Task 7 Step 4's manual smoke before declaring done.

---

## Self-Review (completed by plan author)

**Spec coverage:**
- §3.1 roles in system prompt → Task 1 (RenderRolesPrompt) + Task 2 (SetRoles appends it) ✅
- §3.2 delete router/dispatcher/tests → Task 3 ✅
- §3.2 delete SafeSend → Task 4 ✅
- §3.2 keep role.go/loader.go/store.go → untouched (no task modifies them) ✅
- §3.2 ProcessInput returns to three branches → Task 2 Step 4 ✅
- §5 SetRoles idempotency via marker → Task 1 (marker + accessor) + Task 2 (Contains check) ✅
- §6 fail-open (missing roles.toml = no-op) → Task 2 SetRoles nil/empty guard ✅
- §7 testing → Task 1 tests + Task 7 verification ✅
- §8 migration (net deletion) → Tasks 3, 4 delete; Tasks 1, 2 add minimal ✅

**Placeholder scan:** Task 7 Step 4 describes manual smoke with concrete inputs and expected outputs — not a placeholder. Task 8 Step 2 PR body is described by content, not deferred. No "TBD/TODO/figure out".

**Type consistency:** `RenderRolesPrompt(store *Store) string` in Task 1, called as `role.RenderRolesPrompt(store)` in Task 2. `RolesPromptMarker() string` in Task 1, called as `role.RolesPromptMarker()` in Task 2. `SetRoles(store *role.Store)` signature consistent in Tasks 2, 5. No `Router`/`Dispatcher` references after Task 3.
