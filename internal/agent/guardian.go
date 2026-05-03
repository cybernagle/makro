package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/naglezhang/fingersaver/internal/agent/tools"
	"github.com/naglezhang/fingersaver/internal/llm"
	"github.com/naglezhang/fingersaver/internal/util"
)

const defaultAssessorPrompt = `You are a session guardian. You monitor a coding agent in a terminal and decide how to respond to its confirmation prompts.

Most agent confirmations are routine (tool call approvals, "proceed?", file edits) and should be APPROVED. Only REJECT genuinely dangerous operations (deleting production data, force-pushing to main, dropping databases, sudo commands).

Respond with ONLY a JSON object on a single line. The "decision" field MUST be exactly one of: "approve", "reject", "idle", "unknown".
- Routine confirmation (tool calls, file edits, proceed prompts): {"decision":"approve","reason":"brief reason"}
- Dangerous operation (destructive, irreversible, production-impacting): {"decision":"reject","reason":"brief reason"}
- Agent is working, showing output, or idle (no prompt visible): {"decision":"idle","reason":"brief reason"}
- Cannot determine: {"decision":"unknown","reason":"brief reason"}`

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
	assessCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
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
	case "idle", "working", "running", "none", "n/a":
		return "idle"
	default:
		return "unknown"
	}
}
