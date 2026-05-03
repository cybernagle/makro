package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/naglezhang/fingersaver/internal/agent/tools"
	"github.com/naglezhang/fingersaver/internal/llm"
	"github.com/naglezhang/fingersaver/internal/tmux"
	"github.com/naglezhang/fingersaver/internal/util"
)

const guardianInterval = 5 * time.Second

const guardianSystemPrompt = `You are a session guardian. You monitor a coding agent in a terminal and decide how to respond to its confirmation prompts.

Most agent confirmations are routine (tool call approvals, "proceed?", file edits) and should be APPROVED. Only REJECT genuinely dangerous operations (deleting production data, force-pushing to main, dropping databases, sudo commands).

Respond with ONLY a JSON object on a single line:
- Routine confirmation (tool calls, file edits, proceed prompts): {"decision":"approve","reason":"brief reason","needs_response":true}
- Dangerous operation (destructive, irreversible, production-impacting): {"decision":"reject","reason":"brief reason","needs_response":true}
- Agent is working, showing output, or idle (no prompt visible): {"decision":"idle","reason":"brief reason","needs_response":false}
- Cannot determine: {"decision":"unknown","reason":"brief reason","needs_response":false}`

type Judgment struct {
	Decision      string `json:"decision"`
	Reason        string `json:"reason"`
	NeedsResponse bool   `json:"needs_response"`
}

type GuardianState struct {
	SessionName     string
	Cancel          context.CancelFunc
	lastOutputHash  string
	lastRespondTime time.Time
}

type GuardianManager struct {
	mu        sync.Mutex
	guardians map[string]*GuardianState
	provider  llm.Provider
	tc        tools.TmuxClient
	model     string
	sendEvent func(session, content string)
}

func NewGuardianManager(provider llm.Provider, tc tools.TmuxClient, model string) *GuardianManager {
	return &GuardianManager{
		guardians: make(map[string]*GuardianState),
		provider:  provider,
		tc:        tc,
		model:     model,
	}
}

func (gm *GuardianManager) SetSendEvent(fn func(session, content string)) {
	gm.sendEvent = fn
}

func (gm *GuardianManager) Watch(ctx context.Context, sessionName string) error {
	gm.mu.Lock()
	defer gm.mu.Unlock()

	if _, exists := gm.guardians[sessionName]; exists {
		return fmt.Errorf("already watching %q", sessionName)
	}

	guardianCtx, cancel := context.WithCancel(ctx)
	gs := &GuardianState{
		SessionName: sessionName,
		Cancel:      cancel,
	}
	gm.guardians[sessionName] = gs

	go gm.guardianLoop(guardianCtx, gs)
	return nil
}

// AutoWatch starts watching a session if not already watched. No error if already active.
func (gm *GuardianManager) AutoWatch(ctx context.Context, sessionName string) {
	gm.mu.Lock()
	if _, exists := gm.guardians[sessionName]; exists {
		gm.mu.Unlock()
		return
	}
	gm.mu.Unlock()

	if err := gm.Watch(ctx, sessionName); err != nil {
		log.Printf("[guardian] auto-watch %q failed: %v", sessionName, err)
	}
}

func (gm *GuardianManager) Stop(sessionName string) error {
	gm.mu.Lock()
	defer gm.mu.Unlock()

	state, exists := gm.guardians[sessionName]
	if !exists {
		return fmt.Errorf("not watching %q", sessionName)
	}
	state.Cancel()
	delete(gm.guardians, sessionName)
	return nil
}

func (gm *GuardianManager) StopAll() {
	gm.mu.Lock()
	defer gm.mu.Unlock()

	for name, state := range gm.guardians {
		state.Cancel()
		delete(gm.guardians, name)
	}
}

func (gm *GuardianManager) ActiveGuardians() []string {
	gm.mu.Lock()
	defer gm.mu.Unlock()

	names := make([]string, 0, len(gm.guardians))
	for name := range gm.guardians {
		names = append(names, name)
	}
	return names
}

func (gm *GuardianManager) guardianLoop(ctx context.Context, gs *GuardianState) {
	defer gm.removeGuardian(gs.SessionName)

	// Give the agent a few seconds to start working before we start checking.
	startupGrace := 3
	idleCount := 0

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(guardianInterval):
		}

		// Check session still exists.
		if gm.tc.State() != nil && gm.tc.State().FindSession(gs.SessionName) == nil {
			log.Printf("[guardian] session %q gone, stopping", gs.SessionName)
			return
		}

		if startupGrace > 0 {
			startupGrace--
			continue
		}

		output, err := gm.readAndFilter(gs.SessionName)
		if err != nil || output == "" {
			continue
		}

		// Auto-stop: if session is idle (completed + no pending confirmation) for
		// 3 consecutive checks (~15s), the task is done — stop watching.
		if gm.isOutputIdle(output) {
			idleCount++
			if idleCount >= 3 {
				log.Printf("[guardian] %q idle for %d checks, auto-stopping", gs.SessionName, idleCount)
				gm.notifyTUI(gs.SessionName, &Judgment{Decision: "safe", Reason: "task completed, guardian auto-stopped"})
				return
			}
		} else {
			idleCount = 0
		}

		// Skip if output hasn't changed since last response.
		hash := outputHash(output)
		if hash == gs.lastOutputHash && time.Since(gs.lastRespondTime) < 30*time.Second {
			continue
		}

		judgment := gm.assess(ctx, gs.SessionName, output)
		if judgment == nil {
			continue
		}

		if judgment.NeedsResponse {
			// Cooldown: don't respond more than once every 10s.
			if time.Since(gs.lastRespondTime) < 10*time.Second {
				continue
			}
			approve := judgment.Decision == "approve"
			gm.sendResponse(gs.SessionName, approve)
			gs.lastOutputHash = hash
			gs.lastRespondTime = time.Now()
			log.Printf("[guardian] %s: decision=%s approve=%v reason=%s", gs.SessionName, judgment.Decision, approve, judgment.Reason)
			idleCount = 0

			gm.notifyTUI(gs.SessionName, judgment)
		}
	}
}

// isOutputIdle checks if the session output indicates the agent has finished.
// Idle = completed status with bare ❯ prompt and no pending confirmation.
func (gm *GuardianManager) isOutputIdle(output string) bool {
	return strings.Contains(output, "❯") &&
		!strings.Contains(output, "❯ 1.") &&
		!strings.Contains(output, "❯ 2.") &&
		!confirmPatternRe.MatchString(output)
}

var confirmPatternRe = regexp.MustCompile(`(?i)(do you want|would you like|allow this|proceed\?|confirm\?|should i|approve)`)

func (gm *GuardianManager) readAndFilter(sessionName string) (string, error) {
	raw, err := gm.tc.Exec(tmux.CapturePaneCmd(sessionName))
	if err != nil {
		return "", fmt.Errorf("capture: %w", err)
	}
	if raw == "" {
		return "", nil
	}

	// Take the last 2000 chars — most recent output is what matters.
	return util.ReadProgressive(raw, 2000), nil
}

func (gm *GuardianManager) assess(ctx context.Context, sessionName, output string) *Judgment {
	assessCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	msgs := []llm.Message{
		{Role: llm.RoleSystem, Content: guardianSystemPrompt},
		{Role: llm.RoleUser, Content: fmt.Sprintf("Session %q output:\n%s", sessionName, output)},
	}
	opts := llm.GenerateOptions{Model: gm.model, MaxTokens: 256}
	result, err := gm.provider.Complete(assessCtx, msgs, opts)
	if err != nil {
		log.Printf("[guardian] assess error for %q: %v", sessionName, err)
		return nil
	}

	var j Judgment
	raw := strings.TrimSpace(result.Content)
	// Extract JSON from possible markdown fences.
	if idx := strings.Index(raw, "{"); idx >= 0 {
		raw = raw[idx:]
		if end := strings.Index(raw, "}"); end >= 0 {
			raw = raw[:end+1]
		}
	}
	if err := json.Unmarshal([]byte(raw), &j); err != nil {
		log.Printf("[guardian] assess parse error for %q: %v (raw: %s)", sessionName, err, util.Truncate(raw, 100))
		return nil
	}
	return &j
}

func (gm *GuardianManager) sendResponse(sessionName string, approve bool) {
	text := "No"
	if approve {
		text = "Yes"
	}
	if _, err := gm.tc.Exec(tmux.SendKeysLiteralCmd(sessionName, text)); err != nil {
		log.Printf("[guardian] send %s to %q error: %v", text, sessionName, err)
		return
	}
	if _, err := gm.tc.Exec(tmux.SendEnterCmd(sessionName)); err != nil {
		log.Printf("[guardian] send enter to %q error: %v", sessionName, err)
	}
}

func (gm *GuardianManager) notifyTUI(sessionName string, j *Judgment) {
	if gm.sendEvent == nil {
		return
	}
	action := "observing"
	if j.NeedsResponse {
		if j.Decision == "safe" {
			action = "approved (Yes)"
		} else {
			action = "rejected (No)"
		}
	}
	gm.sendEvent(sessionName, fmt.Sprintf("[guardian] %s: %s — %s", sessionName, action, j.Reason))
}

func (gm *GuardianManager) removeGuardian(sessionName string) {
	gm.mu.Lock()
	defer gm.mu.Unlock()
	delete(gm.guardians, sessionName)
}

// outputHash returns a fast fingerprint for deduplication.
func outputHash(s string) string {
	h := uint32(0x811c9dc5)
	for _, r := range s {
		h ^= uint32(r)
		h *= 0x01000193
	}
	return fmt.Sprintf("%08x:%d", h, utf8.RuneCountInString(s))
}
