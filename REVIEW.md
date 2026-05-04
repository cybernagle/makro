# PR #6: Add agent stop hook notification for instant idle detection

## Round 1 — 5 comments
| # | File | Issue | Status | Commit |
|---|------|-------|--------|--------|
| 1 | internal/agent/hook_config.go:43 | Preserve unknown Claude settings fields instead of rewriting settings.json through a partial struct. | ✅ fixed | |
| 2 | internal/agent/hook_config.go:63 | Use a real executable path for the stop hook instead of relying on bare fingersaver being on PATH. | ✅ fixed | |
| 3 | internal/agent/hook_config.go:73 | Preserve the existing settings.json file mode instead of forcing 0644. | ✅ fixed | |
| 4 | internal/agent/tools/agent_alive.go:170 | Limit wrapped-agent detection to executable chains so command arguments do not create false positives. | ✅ fixed | |
| 5 | internal/agent/tools/wait_until_idle.go:59 | Remove session-wide waiter resets so concurrent waits on the same session do not interfere with each other. | ✅ fixed | |
