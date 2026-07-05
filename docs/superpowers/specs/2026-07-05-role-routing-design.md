# Role-Based Routing — Design Spec

- **Status:** Design (awaiting user review)
- **Date:** 2026-07-05
- **Branch:** `feat/role-routing`
- **Blocks on:** none (main is clean at `2d0a785`)

## 1. Problem

User runs 10+ tmux sessions, each a Claude Code instance managing one task/project. New ad-hoc tasks spawn new sessions reflexively, and the session pool grows without bound. The user can no longer tell *which* Claude should handle a given scrap of work. Context across sessions is unmanaged.

The pain is **at the session layer**, not the Orchestrator layer: it's the proliferation of indistinguishable coding-agent sessions that's the problem.

## 2. Goals & Non-Goals

**Goals**
- Give sessions stable, named **roles** so ad-hoc tasks route to the right one instead of spawning a new anonymous session.
- Keep the stable set (A-level) user-owned via config; let the Orchestrator create temporary roles (B-level) for the long tail.
- Provide a path for stable B-level roles to graduate into A-level config.
- Externalize per-project state for roles that need it (e.g., reviewer), so role sessions stay stateless and clean.

**Non-Goals (YAGNI)**
- No emergent role clustering / drift detection / auto-proposal of role structure. If ever wanted, reuse the Brain propose→inbox→confirm pattern rather than inventing a new system.
- No multi-tenant role isolation. Single user.
- No auto-routing of *every* command — slash commands and `@mention` direct sends keep bypassing routing exactly as today.

## 3. Architecture

### 3.1 Two-tier role model

A **Role** is a named, reusable binding between a class of work and the session that handles it.

```
Role = {
  name:         string   // unique, kebab/identifier
  description:  string   // one-line: what this role owns
  session:      string   // tmux session name; "" = create on demand
  level:        A | B    // A = config-owned, B = runtime-created
  clear_after:  manual | marker | guardian   // optional; default manual
  state_file:   string   // optional; relative path under ~/.makro/state/roles/
}
```

- **A-level**: declared in `~/.makro/roles.toml`, user-owned. Closed set, stable. Examples: `makro`, `juli`, `memory`, `research`, `reviewer`.
- **B-level**: created at runtime by the Orchestrator when no A-level matches and no existing B-level matches. Open set, temporary. Each B-level still has a name + description (no anonymous sessions). Stored in memory; not persisted unless it graduates.

### 3.2 Routing cascade

When a task arrives at the Orchestrator (and it's not a slash command or `@mention`):

1. **Match A-level** → if a role scores above threshold, route there.
2. **Match existing B-level** → if one scores above threshold, route there.
3. **Create B-level** → Orchestrator mints a new role (name + description derived from the task), optionally creates a new session, routes there. B-level creation is **autonomous** (no user prompt) — low risk, just naming a bucket.
4. **Below threshold / ambiguous** → fall back: prefer a role literally named `default` if declared in `roles.toml`; otherwise the first A-level role; otherwise ask the user via a confirmation gate.

### 3.3 Routing judgment mechanism

Routing is **one LLM call** with structured output:

- **Input:** `{ task_description, roles: [{name, description, level}, ...] }`
- **Output:** `{ role: <name|null>, confidence: 0..1, reason: <short> }`
- **Threshold:** confidence ≥ 0.5 to accept; else step 4.
- **Tool reuse:** the call goes through the existing `provider.Complete` path, identical to how the Orchestrator already invokes the LLM. Output is parsed as JSON; on parse failure, fall back to default + log.

This deliberately mirrors the voice-call philosophy (`chat_service.go:501`): the LLM proposes a destination, deterministic code dispatches. The LLM never sends keystrokes itself; routing is a routing decision, then `send_to_session` (with its full safety net) does the actual send.

### 3.4 B → A graduation

- Trigger: a B-level role is used ≥ N times (default N=5) **or** the user explicitly promotes it.
- Mechanism: the Orchestrator proposes graduation via the existing Brain inbox pattern (propose → user confirms). On confirmation, the role is written into `~/.makro/roles.toml` as A-level and the B-level runtime entry is retired.
- Graduation is **propose + confirm**, not autonomous — it modifies the user's permanent config. This matches voice-call and Brain's "LLM proposes, human confirms" pattern.
- Demotion/merge of underused A-level roles is left to the user editing `roles.toml` manually (no auto-demotion in v1).

## 4. Role config (`~/.makro/roles.toml`)

TOML, user-owned, mirrors the existing `~/.makro/skills/` directory convention.

```toml
# A-level roles. Add/edit freely; changes are picked up on next Orchestrator turn.

[[role]]
name        = "makro"
description = "Makro 项目本身的开发、编排、修复"
session     = "makro"

[[role]]
name        = "juli"
description = "juli 项目的开发与维护"
session     = "juli"

[[role]]
name        = "reviewer"
description = "跨项目代码 review 与 review-fix loop。每轮结束 clear,状态外置到文件。"
session     = "reviewer"
clear_after = "manual"                                  # v1: manual. Future: marker | guardian
state_file  = "reviewer/{project}.md"                   # relative to ~/.makro/state/roles/

[[role]]
name        = "research"
description = "通用调研、技术选型、阅读外部资料"
session     = ""                                        # empty = create on demand

[[role]]
name        = "default"
description = "兜底:无法路由时的默认角色"
session     = ""
```

Schema rules:
- `name`: required, unique, identifier-safe.
- `description`: required, used verbatim as the routing LLM's matching target — write it carefully.
- `session`: optional; empty string = Orchestrator creates a new session when this role is first routed to.
- `level`: omitted in config (config roles are always A-level).
- `clear_after`: optional enum; default `manual`. Only meaningful for roles that run review-style loops.
- `state_file`: optional; `{project}` is substituted with the matched project/role name at read/write time.

## 5. Reviewer role — stateless session, externalized state

The reviewer is the first role that needs externalized state, because it may review 10+ projects and `/compact` would leave 10 contexts polluting one session.

### 5.1 Storage: centralized, not repo-level

```
~/.makro/state/roles/<role>/<project>.md
```

Example tree:
```
~/.makro/state/roles/
  reviewer/
    makro.md
    juli.md
    memory.md
    research.md
```

**Why centralized over repo-level** (e.g. `<repo>/.makro-review.md`):
- Doesn't pollute external repos (`memory`, `juli` are not Makro's codebase).
- One namespace, indexed `role × project` — exactly the two axes of the routing model.

**Why net-new, not reusing `save_context`/`restore_context`:**
Makro already persists session snapshots via `save_context`/`restore_context` to `~/.makro/contexts/<session>/latest.json`. That mechanism is **not** reused here, deliberately:
- **Wrong key** — `contexts/` is keyed by *tmux session name*. One reviewer session handles 10 projects, so all 10 projects' state would collapse into `contexts/reviewer/`, losing the project axis (or forcing string-packing like `contexts/reviewer__makro/`).
- **Wrong format** — `contexts/` stores an auto-parsed pane snapshot in JSON (`StructuredOutput`: rawOutput, status, errors, filesModified). Reviewer state is human-authored Markdown conclusions — a different content type.
- **Wrong lifecycle** — `save_context` is a point-in-time manual snapshot for crash recovery / handoff; reviewer state is cumulative per-project knowledge, rewritten at the end of each review loop.
- Forcing `contexts/` to serve both would mean a new keying scheme + a second format + a different write trigger inside an existing subsystem — more coupling, not less. `save_context` stays fit-for-purpose (single-session snapshot); the new `state/roles/` tree owns role×project state.
- Same root as `~/.makro/skills/` and `~/.makro/roles.toml`.
- Not in git — this state is local, it should not land in the reviewed repo.

### 5.2 File naming

`<project>` is the **A-level role name** the task was routed for, not the repo directory name. This keeps the filename aligned with the routing decision — no second translation step.

### 5.3 Reviewer workflow

```
route(task) → role=reviewer, project=makro
  → read  ~/.makro/state/roles/reviewer/makro.md   (if exists)
  → send  task + prior-review context to reviewer session
  → ... reviewer works ...
  → on review-loop end signal (v1: manual):
       write ~/.makro/state/roles/reviewer/makro.md   (this round's conclusions)
       send /clear to reviewer session
```

The reviewer session is thus a **stateless function + per-project state files**: clean every round, but cumulative knowledge lives on disk.

### 5.4 Review-loop end signal — three options, v1 choice

The hard part is "when does one review-fix loop end?" `agent_stop` fires per-turn, not per-loop. Three options:

1. **Marker string** — reviewer emits a sentinel (à la voice-call's ` ```plan `), Makro listens and triggers clear. Most precise, but constrains Claude Code's output format (fragile).
2. **LLM assessment** — Makro's Guardian/Orchestrator reads session output, judges convergence (reuses `SessionAssessor`, `guardian.go:38`). Flexible, but adds an LLM call and non-determinism.
3. **Manual / Guardian-prompted** — user or Guardian explicitly triggers `/clear`. Simplest, most controllable, not automatic.

**v1: option 3** (`clear_after = "manual"`). Ship the plumbing for all three (the enum is in config), implement manual first, leave marker/guardian for later.

### 5.5 clear vs compact

`/clear` (full wipe), not `/compact`. Compact would retain 10 projects' contexts in one session — the exact pollution we're avoiding. The state file replaces what compact would have kept, scoped per-project.

## 6. Integration with existing systems

| Existing | How routing interacts |
|---|---|
| `send_to_session` tool | Unchanged. Routing decides *which* session; `send_to_session` (with blocklist, Enter-loss recovery, agent-alive gate) does the actual send. Routing never sends keystrokes directly. |
| `@mention` direct send | Bypasses routing entirely (`DirectSend`, `orchestrator.go:362`). Untouched. |
| Slash commands / skills | Bypass routing. Untouched. |
| `list_sessions` / `create_session` / `switch_session` | Used by routing under the hood: route to existing role session vs. create new one when `session = ""`. |
| Guardian (`SessionAssessor`) | Future home for `clear_after = "guardian"` auto-judgment. v1 not used by routing. |
| Brain (propose→inbox) | Reused verbatim for B→A graduation proposals. |
| Skills loader (`parser.go`/`loader.go`) | Pattern to mirror for `roles.toml` loading (watch/reload on file change is a nice-to-have, not v1). |

## 7. Components & data flow

```
User input (non-slash, non-@mention)
   │
   ▼
┌─────────────────────────────────────┐
│ Router                              │
│  - load roles (A from toml + B in-mem) │
│  - call provider.Complete(routing prompt) │
│  - parse {role, confidence, reason} │
│  - apply cascade (A → B → create-B) │
└─────────────────────────────────────┘
   │  role + project
   ▼
┌─────────────────────────────────────┐
│ RoleDispatcher                      │
│  - resolve session (existing/new)   │
│  - if role.state_file: read it      │
│  - prepend state context to task    │
│  - send_to_session (with safety net)│
│  - on loop-end signal (v1 manual):  │
│      write state_file               │
│      send /clear                    │
└─────────────────────────────────────┘
   │
   ▼
  tmux session (Claude Code)
```

New packages/files (proposed; final names TBD in implementation plan):
- `internal/role/` — `Role` struct, `Store` (load A-level from toml + hold B-level in memory), routing-LLM prompt, graduation tracker.
- `internal/role/router.go` — the routing cascade + LLM judgment call.
- `internal/role/dispatcher.go` — session resolution, state-file read/write, `/clear` trigger.
- Wiring in `orchestrator.go` (`ProcessInput`) — route non-slash/non-mention input through `Router` before falling into `handleLLM` or `send_to_session`.
- Config loading wired in both composition roots (`main.go:169` area, `cmd/gui/chat_service.go:241` area), next to existing `~/.makro/skills/` loading.

## 8. Error handling

- **`roles.toml` missing/malformed** → log warning, operate as if empty (every task falls through to B-level creation or default). Don't crash.
- **Routing LLM call fails** → fall back to default role (or first A-level), log.
- **Routing JSON parse fails** → same fallback, log.
- **Role's session doesn't exist** → create it (mirror `create_session`).
- **`send_to_session` rejects** (blocklist, agent not alive) → surface to user as today; routing doesn't override safety.
- **State file read/write fails** → log, continue without that context (don't block the review).

## 9. Testing

- **Router unit tests**: given `{task, roles}`, assert the cascade picks the right role; assert threshold fallback; assert B-level creation.
- **Router LLM**: the LLM call is mocked behind the existing `Provider` interface — no real API calls in tests.
- **Dispatcher unit tests**: state-file read/write/substitution; `{project}` templating; session resolution (existing vs new).
- **Config loader tests**: parse the sample `roles.toml`; missing file; malformed TOML; duplicate names rejected.
- **Integration**: route → dispatch → assert the right `send_to_session` args were produced (don't actually run tmux in tests).
- Graduation counter + proposal emission (B used N times → proposal appears).

## 10. Open questions (resolve during implementation planning)

1. **Routing prompt design** — exact wording, few-shot examples. Defer to implementation; start with a minimal zero-shot prompt.
2. **B-level naming** — when Orchestrator mints a B-level, how is its `name` generated? Likely: ask the routing LLM to also suggest a slug. Implementation detail.
3. **State-file format** — free-form markdown, or a structured frontmatter+body (e.g., `status:`, `last_reviewed:`, `open_issues:`)? Start free-form; structure can evolve.
4. **Reload semantics** — pick up `roles.toml` edits per-turn (cheap) or via file watcher? Per-turn is simpler and sufficient for v1.
5. **Where the routing LLM call is billed** — same provider/model as Orchestrator, or a cheaper/faster model? v1: same model for simplicity.

## 11. Phasing

**Phase 1 (this spec → first implementation plan):**
- `roles.toml` loading (A-level only).
- Router: LLM judgment + cascade (A → default fallback). B-level creation deferred.
- Dispatcher: session resolution + `send_to_session` integration. No state files yet.
- Manual: every task routed; user can observe behavior.

**Phase 2:**
- B-level runtime creation + in-memory store.
- B→A graduation (propose via Brain inbox).

**Phase 3:**
- Reviewer role: `state_file`, read/write, `/clear` on manual signal.
- `clear_after = "marker"` and `"guardian"` implementations.

## 12. Architectural principle: Role is an asset, Agent is infrastructure

Long-term direction (recorded 2026-07-05, validated with user). This is a **trajectory, not Phase 1 scope** — it constrains *how* data structures are written, not *what* is built now.

- **Role is the compounding asset**: it encodes domain knowledge, accumulates telemetry (call-count, score), and grows coupled to its owner. The role library is the moat.
- **Agent is replaceable infrastructure**: the LLM, the orchestrator loop, the tool registry are all commodity over time.
- **Single role evolves toward `agent + code + prompt`**: not just a persona/prompt, but carrying its own tools/behaviors. Phase 1's config-only role is the starting point of that evolution.
- **Role has a full lifecycle**: call-count → score → deprecation. The B→A graduation in §3.4 is the seed of this; later phases generalize it to telemetry-driven promotion *and* retirement.

### Implication for Phase 1 (the only one that touches Phase 1)

The `Role` data model must be **extensible without migration**:

- `Role` struct carries a `Metadata map[string]any` field. Phase 1 reads/writes nothing in it; future telemetry fields live there first, then get promoted to first-class struct fields once stable.
- The `roles.toml` loader is **forward-compatible**: unknown keys are tolerated (not errors), so adding config fields later never breaks existing user configs. (This is typically a one-flag default in Go TOML libraries and costs nothing.)
- The graduation counter (Phase 2) is shaped as `map[string]int` (role-name → count) from day one, not a hardcoded `if usedFiveTimes` — so it can grow into full lifecycle telemetry without rework.

Phase 1 implements **zero** telemetry/scoring/deprecation logic. The principle only fixes the *shape* of the structs so the evolution path stays open at near-zero cost.
