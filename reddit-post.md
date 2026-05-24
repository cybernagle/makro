## Makro — orchestrate Claude Code, Copilot, and Codex from one terminal

GitHub: https://github.com/cybernagle/makro
Install: `brew install cybernagle/tap/makro`

I got tired of switching between terminals to manage Claude Code and Copilot sessions. Different tasks — devops, bug fixes, code review — all running in parallel. So I built Makro, a terminal UI that controls multiple AI coding agents from one split-pane interface.

**How it works:**

Each agent lives in its own tmux session. I wrap tmux operations as LLM function calling tools — create_session, send_to_session, read_session_output. The orchestrator doesn't care what agent is inside. Works with Claude Code, Copilot, Codex, or anything that accepts text input.

**Multi-agent workflows:**

- Chain: Codex writes code → Copilot reviews → Claude Code runs tests
- Parallel: Same prompt to multiple agents, compare results
- Relay: Read output from one session, summarize, forward to another

**Safety layer:**

Every command goes through LLM risk assessment before reaching an agent. Approve or block. This is critical for automation — you don't want to blindly forward dangerous commands.

**Skills system:**

Tools grouped with behavior constraints. "Review" skill is read-only. "Deploy" skill can send but only to specific sessions. Keeps agents focused.

**Current bottleneck:** capture-pane polling burns tokens. Exploring hooks as an event-driven alternative.

**Try it:**

```
Monitor session "dev" — when it finishes the task, tell session "review" to start code review. Run up to 10 review rounds until the results converge.
```

One sentence, and the orchestrator handles the rest: wait for dev to finish, relay output to review, loop until quality converges.

Built with Go, Bubbletea v2, tmux. MIT licensed.
