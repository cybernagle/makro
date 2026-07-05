package role

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/naglezhang/makro/internal/llm"
)

// ConfidenceThreshold is the minimum confidence to accept a routing decision.
// Below it, the router falls back to the store default (spec §3.3).
const ConfidenceThreshold = 0.5

// routingCallTimeout bounds a single routing LLM call. A slow or dead model
// falls back to the default role instead of hanging the turn.
const routingCallTimeout = 15 * time.Second

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

	// Bound the routing call: a slow/dead model falls back to default instead
	// of hanging the whole turn (ProcessInput's ctx is only cancelled by the
	// NEXT input, so without this a stuck route blocks indefinitely).
	routeCtx, cancel := context.WithTimeout(ctx, routingCallTimeout)
	defer cancel()
	result, err := r.provider.Complete(routeCtx, prompt, opts)
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

// truncate caps s to n runes (not bytes), appending "..." if shortened. Safe
// for UTF-8 / multi-byte content — byte-slicing would split runes and write
// invalid bytes to logs.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
