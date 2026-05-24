package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/naglezhang/makro/internal/agent/tools"
	"github.com/naglezhang/makro/internal/llm"
	"github.com/naglezhang/makro/internal/util"
)

const defaultAssessorPrompt = `You are a session guardian. You monitor a coding agent in a terminal and decide how to respond to its confirmation prompts.

Determine if the agent is WAITING for user input. Return "approve"/"reject"/"unknown".

The agent is WAITING for input when:
- The output ends with a shell prompt (❯) followed by a selection menu (1. Yes / 2. No)
- The last question is asking for user approval or a decision (e.g. "Do you want to proceed?", "Allow this action?", "你希望我现在改...?")
- Any confirmation, permission, or choice prompt visible near the end of output
- The agent asks a direct question and the shell prompt (❯) is visible, meaning the agent is done and waiting

The agent is NOT waiting for input when:
- It is actively showing progress (e.g. "Phase 1", "Step 2/5", running commands)
- It is in the middle of generating output (no shell prompt visible)
- Tool calls are executing (showing output, not waiting for approval)

Respond with ONLY a JSON object on a single line:
- {"decision":"approve","reason":"brief reason"} — routine confirmation (tool calls, file edits, proceed prompts, questions asking for decisions)
- {"decision":"reject","reason":"brief reason"} — dangerous operation (deleting prod data, force-pushing, dropping databases, sudo)
- {"decision":"unknown","reason":"brief reason"} — cannot determine`

// SessionAssessor implements tools.Assessor using an LLM to evaluate
// pending confirmation prompts in coding agent sessions.
type SessionAssessor struct {
	provider llm.Provider
	model    string
	prompt   string
}

func NewSessionAssessor(provider llm.Provider, model, prompt string) *SessionAssessor {
	if prompt == "" {
		prompt = defaultAssessorPrompt
	}
	return &SessionAssessor{
		provider: provider,
		model:    model,
		prompt:   prompt,
	}
}

func (sa *SessionAssessor) Assess(ctx context.Context, sessionName, output string) (*tools.Assessment, error) {
	assessCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	msgs := []llm.Message{
		{Role: llm.RoleSystem, Content: sa.prompt},
		{Role: llm.RoleUser, Content: fmt.Sprintf("Session %q output:\n%s", sessionName, output)},
	}
	opts := llm.GenerateOptions{Model: sa.model, MaxTokens: 256}
	result, err := sa.provider.Complete(assessCtx, msgs, opts)
	if err != nil {
		return nil, fmt.Errorf("assess LLM call: %w", err)
	}

	raw := strings.TrimSpace(result.Content)
	if idx := strings.Index(raw, "{"); idx >= 0 {
		raw = raw[idx:]
		if end := strings.Index(raw, "}"); end >= 0 {
			raw = raw[:end+1]
		}
	}

	var j struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(raw), &j); err != nil {
		log.Printf("[assessor] parse error for %q: %v (raw: %s)", sessionName, err, util.Truncate(raw, 100))
		return &tools.Assessment{Decision: "unknown", Reason: "failed to parse LLM response"}, nil
	}

	decision := normalizeDecision(j.Decision)
	log.Printf("[assessor] %s: decision=%s reason=%s", sessionName, decision, j.Reason)
	return &tools.Assessment{Decision: decision, Reason: j.Reason}, nil
}

func normalizeDecision(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "approve", "yes", "safe", "ok", "allow", "accept":
		return "approve"
	case "reject", "no", "deny", "block", "risky", "dangerous", "unsafe":
		return "reject"
	default:
		return "unknown"
	}
}
