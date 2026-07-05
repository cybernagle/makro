# Role-Based Routing v2 — Design Spec (replaces v1)

- **Status:** Design (awaiting user review)
- **Date:** 2026-07-05
- **Branch:** `refactor/role-routing-v2` (new branch from latest main)
- **Replaces:** The Router/Dispatcher design in `2026-07-05-role-routing-design.md` (v1, merged as PR #12, then patched as `7a9ada6`)

## 0. Why this replaces v1

v1 (PR #12) added an **independent Router + Dispatcher** that bypassed the orchestrator's main conversation loop: a separate LLM call decided which role, then a dispatcher sent directly to a session. Two consequences made this wrong:

1. **It bypassed the base.** Makro's main conversation is already an LLM agent that dispatches via the `send_to_session` tool (with its blocklist, agent-alive gate, Enter-loss recovery). v1 routed around all of it.
2. **It was fail-closed by construction.** With `o.router != nil`, the main conversation path was entirely sidestepped. When the router's model was empty (a real bug), every message fell back into a session — breaking all base functionality.

The fix in `7a9ada6` (model propagation + fallback to `handleLLM`) patched the symptoms but not the root cause: **a separate routing LLM is redundant.** The main conversation LLM already decides dispatchment; it just needs to know which sessions exist and what each one is for. That knowledge is already in `roles.toml`.

**v2's principle: roles are input context to the main conversation, not a separate decision-maker.** No Router LLM call, no Dispatcher. The orchestrator's `ProcessInput` returns to its original three-branch form (slash / @mention / handleLLM). Roles are injected into the system prompt; the main LLM reads them and uses `send_to_session` as it always has.

## 1. Problem (unchanged from v1)

User runs 10+ tmux sessions, each a Claude Code instance. New ad-hoc tasks spawn new sessions reflexively; the user can't tell which Claude should handle a given scrap of work. The pain is at the session layer.

## 2. Goals & Non-Goals

**Goals**
- Give the orchestrator LLM stable, named knowledge of which session handles what (from `roles.toml`).
- Let the main conversation route tasks to the right session using its existing `send_to_session` tool — no new dispatch path.
- Fail open: a missing/empty `roles.toml`, a bad config, or any role-related error must never break the base conversation.

**Non-Goals**
- No independent routing LLM call (v1 had one; v2 removes it).
- No B-level runtime roles, no graduation, no reviewer auto-clear (still Phase 2/3).
- No change to how slash commands, `@mention`, or any existing tool works.

## 3. Architecture

### 3.1 The one change: roles in the system prompt

`roles.toml` is loaded at startup and rendered into a "Managed sessions" section appended to `DefaultSystemPrompt()`. The main conversation LLM sees, for example:

```
You manage the following tmux sessions, each with a role describing what it handles:
- review (session: review): 代码 review、PR review、检查代码质量、看 diff 找问题
- dev (session: dev): Makro 项目的日常开发、写代码、修 bug、重构
- juli-dev (session: juli-dev): juli 项目的开发、维护
- memory (session: memory): memory / memory-cli 项目相关
- business (session: business): 业务、产品、需求讨论
- personal (session: personal): 个人事务

When the user's task matches one of these roles, send it to that session with send_to_session.
If no role clearly fits, just handle the task yourself in this conversation.
```

The LLM does the rest. It already knows `send_to_session` from the existing tool list and system prompt rules.

### 3.2 What gets deleted

| v1 component | v2 action |
|---|---|
| `internal/role/router.go` (Router + LLM call) | **Delete.** No independent routing LLM. |
| `internal/role/dispatcher.go` (Dispatcher) | **Delete.** Main conversation dispatches via `send_to_session`. |
| `internal/role/router_test.go`, `dispatcher_test.go`, `integration_test.go` | **Delete** (their subjects are gone). |
| `tools.SafeSend` (added in v1's review fix) | **Delete.** v2 has no caller for it (the Dispatcher is gone; the `send_to_session` tool uses the unexported `validateSendTarget`+`SendConfirmed` directly). Leaving it is dead code. (If `handleMention` ever needs the gated path, re-add it then — YAGNI for now.) |
| Orchestrator `router`/`dispatcher` fields, `handleRoute` method, the `if o.router != nil` branch in `ProcessInput` | **Delete.** `ProcessInput` returns to slash / @mention / handleLLM. |
| `SetRoles(store, notifier)` | **Replace** with `SetRoles(store)` that renders roles into the system prompt (append to whatever `SetSystemPrompt` set). |

### 3.3 What stays

| Component | Why it stays |
|---|---|
| `internal/role/role.go` (Role struct, Validate, defaults) | Still the config schema. |
| `internal/role/loader.go` (TOML parsing, forward-compat) | Still how `roles.toml` is read. |
| `internal/role/store.go` (in-memory lookup) | Used to render the prompt; also lets future phases query by name. |
| `~/.makro/roles.toml` schema | Unchanged. `clear_after` / `state_file` fields remain (unused in v2, reserved for Phase 3). |
| `send_to_session`, all tools, `@mention`, slash commands | Untouched — they're the dispatch path now, as they always should have been. |

## 4. Data flow (v2)

```
Startup:
  main.go / chat_service.go
    → role.LoadFile("~/.makro/roles.toml") + role.LoadFile("./.makro/roles.toml")
    → role.NewStore(merged)
    → orch.SetSystemPrompt(DefaultSystemPrompt())   // existing
    → orch.SetRoles(store)                           // NEW: appends roles to systemPrompt

Per user input:
  ProcessInput(input)
    → slash command?  → skill/command path   (unchanged)
    → @mention?       → handleMention         (unchanged)
    → else            → handleLLM             (unchanged)
         ↓
         provider.Stream/Complete with systemPrompt that includes the roles section
         ↓
         LLM decides: "this is a review task" → calls send_to_session(name="review", message=...)
                                       OR:    "I'll handle it" → replies directly
```

No second LLM call. No branch that can bypass `handleLLM`. The roles are just words in the prompt the main conversation already reads.

## 5. `SetRoles` semantics

```go
func (o *Orchestrator) SetRoles(store *role.Store) {
    if store == nil || store.Len() == 0 {
        return // nothing to add; systemPrompt stays as DefaultSystemPrompt()
    }
    section := renderRolesPrompt(store)
    o.systemPrompt = o.systemPrompt + "\n\n" + section
}
```

- **Idempotency:** `SetRoles` appends a marked section (e.g. wrapped in a sentinel comment or a recognizable header line). If `o.systemPrompt` already contains that marker, `SetRoles` is a no-op. This makes it safe to call multiple times (defensive against future wiring changes) without double-appending.
- **Order in composition roots:** `SetSystemPrompt(DefaultSystemPrompt())` runs first, then `SetRoles(store)`. Both `main.go` and `chat_service.go` already call them in this order; verify and keep.
- **renderRolesPrompt(store)** lives in the `role` package (so the orchestrator doesn't format role text). Returns the "Managed sessions" block shown in §3.1.

## 6. Error handling (fail-open everywhere)

- **`roles.toml` missing** → `LoadFile` returns `nil, nil` (existing behavior); `SetRoles(nil)` is a no-op; base conversation works unchanged.
- **`roles.toml` malformed** → log a warning, skip that file, proceed with whatever loaded; base conversation works.
- **No roles loaded at all** → `SetRoles` no-op; systemPrompt is just `DefaultSystemPrompt()`.
- **Role references a session that doesn't exist** → not an error at load time. If the LLM tries `send_to_session` to it, the tool's existing agent-alive gate returns a clear error to the LLM, which can then fall back to handling the task itself. (This is the existing tool behavior — no new code.)

The invariant: **any failure in the roles subsystem degrades to the base conversation, never to a broken state.** There is no `o.router != nil` branch that could bypass `handleLLM`.

## 7. Testing

- **`role.LoadFile` / `Store`** — existing tests stay (loader, store, validation). They're still correct.
- **`renderRolesPrompt`** (new) — unit test: given a Store, assert the rendered string contains each role's name, session, and description in the expected format. Assert empty store → empty string.
- **`SetRoles`** — unit test: assert systemPrompt grows by the roles section; assert calling with nil/empty store leaves systemPrompt unchanged; assert the base prompt is still present.
- **No router/dispatcher/integration tests** — those files are deleted.
- **Regression:** the existing `TestSetModelPropagatesToRouter` (added in `7a9ada6`) becomes meaningless and is deleted along with `router.go`.
- **End-to-end smoke (manual, post-deploy):** with `~/.makro/roles.toml` configured, send "帮我 review PR #12" in the running app and confirm the orchestrator calls `send_to_session` to the review session (visible in the chat). Send a vague task and confirm the orchestrator handles it inline (no dispatch). This is the verification v1's deployment skipped.

## 8. Migration from v1

The `feat/role-routing` branch and PR #12 are already merged to main. v2 is a **net deletion + a small addition**:

- Delete: `router.go`, `dispatcher.go`, their tests, `integration_test.go`, orchestrator's `handleRoute` + routing branch + router/dispatcher fields.
- Add: `renderRolesPrompt` in `role` package (~20 lines), rewrite `SetRoles` (~10 lines).
- Keep: `role.go`, `loader.go`, `store.go`, `loader_test.go`, `store_test.go`, `role_test.go`, `SafeSend`.

Net code change is negative (more deleted than added). The `~/.makro/roles.toml` the user already wrote keeps working unchanged.

## 9. Phasing

This is the entirety of "role routing v2." There is no Phase 1/2 split within it — it's one coherent change. Subsequent phases (unchanged from v1's roadmap):

- **Phase 2:** B-level runtime roles + graduation (still uses Brain's propose→inbox).
- **Phase 3:** Reviewer role — `clear_after` implementation (marker/guardian), optional review-result persistence.
- **Phase 4:** Session auto-creation with agent launch.

## 10. Why this is strictly better than v1

| | v1 (Router+Dispatcher) | v2 (roles in prompt) |
|---|---|---|
| Extra LLM call per task | Yes (routing judgment) | None |
| Bypasses main conversation | Yes (when `router != nil`) | No |
| Failure mode | Fail-closed (bug → everything to default session) | Fail-open (degrades to base conversation) |
| Lines of code | ~600 (router+dispatcher+tests) | ~30 (renderRolesPrompt + SetRoles rewrite) |
| Reuses existing dispatch path | No (separate Dispatcher) | Yes (`send_to_session` tool) |
| Touches `ProcessInput` branching | Yes (new branch) | No (original three branches) |

The v1 design solved a problem that didn't exist (the main conversation already dispatches) by building a parallel system that couldn't fail safely. v2 gives the main conversation the knowledge it was missing and lets it do its job.
