package tools

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/naglezhang/makro/internal/tmux"
	"github.com/naglezhang/makro/internal/util"
)

// ErrBareShell marks a send refused because the pane's foreground is a plain
// shell — the coding agent is gone (typical after a restart). It is the signal
// for the self-healing send paths to relaunch the agent programmatically
// instead of surfacing the failure to an LLM that has no tool to fix it.
var ErrBareShell = errors.New("bare shell, no coding agent running")

// SessionHealer programmatically restores the coding agent in a session whose
// agent died. Recovery is the program's job: when a send finds a bare shell,
// the system heals the session itself (from its snapshot of what ran there)
// rather than erroring out for the orchestrator or the user to fix by hand.
type SessionHealer interface {
	HealSession(session string) error
}

// WaitForAgent polls a session until a coding agent is ready to receive input
// (used after HealSession launched one — agents take a few seconds to boot).
func WaitForAgent(ctx context.Context, tc TmuxClient, session string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		status := checkAgentAlive(tc, session)
		ready := status.Alive || (!paneForegroundIsShell(tc, session) && paneEndsAtAgentPrompt(tc, session))
		if ready {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("agent in %q did not become ready within %s (last: %s)", session, timeout, status.Reason)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// validateSendTarget runs the pre-send gates shared by every autonomous send
// path (send_to_session, relay_message, restore_context): the agent must be
// alive and not showing a Yes/No dialog. It returns the detected agent name
// (e.g. "claude") so the caller can wait for a turn-start signal only for
// agents whose hooks can emit one.
//
// The pane-prompt fallback (paneEndsAtAgentPrompt) rescues the case where
// checkAgentAlive's process-tree scan false-negatives because the agent PID is
// invisible to ps (macOS SIP, nvm/asdf/launchd wrappers): if the pane shows the
// agent at its input prompt, trust the pane over the process scan. It must NOT
// rescue a bare shell, though: a freshly recovered post-reboot tmux session
// runs plain zsh, and starship-style prompts render the same "❯" the agent
// input uses — without the shell check below, task text gets typed into the
// shell and echoed/executed as garbage.
func validateSendTarget(tc TmuxClient, session string) (string, error) {
	status := checkAgentAlive(tc, session)
	if !status.Alive {
		if paneForegroundIsShell(tc, session) {
			return "", fmt.Errorf("cannot send to %q: %w — likely lost across a restart; the session will be auto-recovered if its agent is known, or start the agent in that pane first", session, ErrBareShell)
		}
		if !paneEndsAtAgentPrompt(tc, session) {
			return "", fmt.Errorf("cannot send to %q: no coding agent (claude/copilot/codex) is running (%s); start the agent in that pane first", session, status.Reason)
		}
	}
	if ready, reason := agentReadyToSend(tc, session); !ready {
		return status.Agent, fmt.Errorf("cannot send to %q: %s", session, reason)
	}
	return status.Agent, nil
}

func NewSendToSessionTool(tc TmuxClient, notifier Notifier, healer SessionHealer) Tool {
	return Tool{
		Name:        "send_to_session",
		Description: "Send a command or message to a tmux session. Only works when a known coding agent (claude, copilot, codex) is alive; a session whose agent died is auto-recovered from its snapshot before sending. Destructive shell commands (rm -rf, curl|sh, etc.) are blocked. Follow with wait_until_idle to handle any confirmation prompts.",
		Parameters: []Param{
			{Name: "name", Type: "string", Description: "Session name", Required: true},
			{Name: "message", Type: "string", Description: "Text to send to the session", Required: true},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			name, _ := args["name"].(string)
			message, _ := args["message"].(string)
			if name == "" || message == "" {
				return "", fmt.Errorf("name and message are required")
			}

			// Pre-send gates: agent alive (or at its input prompt) and not
			// showing a Yes/No dialog. Shared with relay_message and
			// restore_context via validateSendTarget so every autonomous send
			// path gets the same protection. A bare shell triggers program-
			// matic recovery (relaunch the agent, wait for it) — recovery is
			// the system's job, not something to hand back to the caller.
			if err := ensureSendable(ctx, tc, healer, name); err != nil {
				return "", err
			}

			// Blocklist check.
			if blocked, pattern := isBlockedCommand(message); blocked {
				return "", fmt.Errorf("command blocked by safety policy: matched pattern %q", pattern)
			}

			// Atomic send + first-send Enter-loss recovery (resends Enter if
			// claude didn't start a turn). Shared with DirectSend and the
			// kanban/@mention paths via SendConfirmed.
			if err := SendConfirmed(ctx, tc, notifier, name, message); err != nil {
				return "", err
			}

			return fmt.Sprintf("Sent to %q: %s", name, util.Truncate(message, 50)), nil
		},
	}
}

// submitConfirmGrace is how long send_to_session waits after sending for the
// agent to begin the turn (UserPromptSubmit hook → agent_start). If no reaction
// arrives in this window the prompt likely never submitted, and we return an
// error instead of letting the caller hang on wait_until_idle for the full
// timeout. Overridable in tests to keep the "never started" path fast.
var submitConfirmGrace = 10 * time.Second

// confirmAgentStarted reports whether the agent reacted to a just-sent prompt
// within grace — signalled by any hook notification newer than `before`
// (typically agent_start from Claude Code's UserPromptSubmit hook). It detects
// text that reached the input box but was never submitted, so the caller can
// fail fast instead of hanging on the idle wait. A nil notifier (e.g. manual
// @mention mode without hooks) short-circuits to true.
func confirmAgentStarted(ctx context.Context, notifier Notifier, session string, before uint64, grace time.Duration) bool {
	if notifier == nil {
		return true
	}
	ch, cancel := notifier.WaitAfter(session, before)
	defer cancel()
	// NewTimer (+ Stop) rather than time.After: time.After's timer is not
	// collected until it fires, so a burst of sends would leave one leaked
	// timer per send on the happy path for the whole grace window.
	t := time.NewTimer(grace)
	defer t.Stop()
	select {
	case <-ch:
		return true
	case <-t.C:
		return false
	case <-ctx.Done():
		return false
	}
}

// firstConfirmGrace is how long SendConfirmed waits for the agent to start a
// turn after the initial atomic send before assuming the Enter was lost and
// resending it. Kept short (vs submitConfirmGrace) so the retry happens fast.
// Overridable in tests.
var firstConfirmGrace = 3 * time.Second

// SendConfirmed sends message to session atomically and, for agents we can
// observe (claude — its UserPromptSubmit hook feeds agent_start), confirms the
// prompt was actually submitted. If the submit didn't register within
// firstConfirmGrace — the classic first-send Enter-loss, where the agent just
// rendered its welcome screen and absorbed the Enter while the text landed in
// the input box — it resends Enter once (the text is already typed) and
// re-confirms. Empirically the resend always lands once the agent has settled,
// turning "first send always fails, second works" into "first send succeeds".
//
// notifier may be nil (manual modes without hooks); then the send is trusted
// without confirmation. Non-claude agents (copilot/codex) have no submission
// hook, so their sends are trusted too. Returns an error only if the prompt
// still didn't submit after the retry, so callers fail fast instead of hanging.
func SendConfirmed(ctx context.Context, tc TmuxClient, notifier Notifier, session, message string) error {
	// Safety: enforce the same destructive-command blocklist as the
	// send_to_session tool. SendConfirmed is the choke point for every
	// autonomous send path (send_to_session, voice dispatch via ConfirmPlan,
	// kanban / @mention HTTP), and `message` is LLM-generated — so an rm -rf or
	// curl|sh buried in a plan's brief must be refused here too, not just in
	// the tool path.
	if blocked, pattern := isBlockedCommand(message); blocked {
		return fmt.Errorf("command blocked by safety policy: matched pattern %q", pattern)
	}

	before := uint64(0)
	if notifier != nil {
		before = notifier.Snapshot(session)
	}
	if err := sendText(tc, session, message); err != nil {
		return err
	}
	if notifier == nil {
		return nil
	}
	// Only Claude Code emits agent_start (UserPromptSubmit hook); copilot/codex
	// have no such hook, so we can't observe their submission — trust the send.
	if checkAgentAlive(tc, session).Agent != "claude" {
		return nil
	}
	if confirmAgentStarted(ctx, notifier, session, before, firstConfirmGrace) {
		return nil
	}
	// The turn didn't start within firstConfirmGrace: either Enter was lost
	// (text sitting in the input box unsubmitted) OR the UserPromptSubmit hook
	// fired but makro didn't receive it (path mismatch, socket error, timeout,
	// or any other edge case). Resend Enter unconditionally — we no longer
	// consult paneEndsAtAgentPrompt because that check is unreliable in
	// environments with status bars / wrappers below the ❯ prompt (it always
	// returns false, suppressing the resend). A stray Enter is harmless: if
	// the prompt already submitted and the agent is thinking, the resend lands
	// as an empty input that Claude ignores; if Enter was truly lost, this
	// recovers it. The timeout is the single source of truth.
	if _, err := tc.Exec(tmux.SendEnterCmd(session)); err != nil {
		return fmt.Errorf("resend enter to %q: %w", session, err)
	}
	if confirmAgentStarted(ctx, notifier, session, before, submitConfirmGrace) {
		return nil
	}
	return fmt.Errorf("sent to %q but claude did not start a turn within %s — the prompt may not have been submitted (Enter lost); check the pane and resend", session, firstConfirmGrace+submitConfirmGrace)
}

// blockedPatterns are hard-blocked — these commands are never allowed.
var blockedPatterns = []struct {
	re      *regexp.Regexp
	pattern string
}{
	// rm with both -r and -f in any form: combined (-rf, -fr), separate (-r -f), or long (--recursive --force).
	{regexp.MustCompile(`(?i)\brm\b.*(?:(?:-[a-zA-Z]*[rf][a-zA-Z]*[rf])|(?:-[a-zA-Z]*r[a-zA-Z]*\s+-[a-zA-Z]*f)|(?:-[a-zA-Z]*f[a-zA-Z]*\s+-[a-zA-Z]*r)|(--recursive\b.*--force\b)|(--force\b.*--recursive\b))`), "rm -rf"},
	{regexp.MustCompile(`(?i)sudo\s+rm\s+`), "sudo rm"},
	{regexp.MustCompile(`(?i)mkfs\b`), "mkfs"},
	{regexp.MustCompile(`(?i)dd\s+if=.*\s+of=/dev/`), "dd to block device"},
	{regexp.MustCompile(`(?i)chmod\s+-R\s+(777|000|a\+rwx)\s+(/|~)`), "chmod -R 777 /"},
	{regexp.MustCompile(`(?i)>\s*/dev/sd`), "write to block device"},
	{regexp.MustCompile(`(?i)(curl|wget)\b.*\|\s*(sh|bash)\b`), "curl/wget | sh"},
	{regexp.MustCompile(`\)\s*\{.*\|.*&`), "fork bomb"},
	{regexp.MustCompile(`(?i)shutdown\b`), "shutdown"},
	{regexp.MustCompile(`(?i)reboot\b`), "reboot"},
	{regexp.MustCompile(`(?i)\bhalt\b`), "halt"},
	{regexp.MustCompile(`(?i)\binit\s+[06]\b`), "init 0/6"},
}

func isBlockedCommand(message string) (bool, string) {
	parts := splitChainedCommands(message)

	// Check each part against blocklist.
	for _, part := range parts {
		for _, p := range blockedPatterns {
			if p.re.MatchString(part) {
				return true, p.pattern
			}
		}
	}

	// Detect distributed rm -rf: -r in one rm command, -f in another.
	var foundRecursive, foundForce bool
	for _, part := range parts {
		if rmRecursiveFlagRe.MatchString(part) {
			foundRecursive = true
		}
		if rmForceFlagRe.MatchString(part) {
			foundForce = true
		}
	}
	if foundRecursive && foundForce {
		return true, "rm -rf"
	}
	return false, ""
}

// rmRecursiveFlagRe matches an rm command containing -r or --recursive.
var rmRecursiveFlagRe = regexp.MustCompile(`(?i)\brm\b.*(?:-[a-zA-Z]*r|--recursive\b)`)

// rmForceFlagRe matches an rm command containing -f or --force.
var rmForceFlagRe = regexp.MustCompile(`(?i)\brm\b.*(?:-[a-zA-Z]*f|--force\b)`)

// splitChainedCommands splits a command string by && and ; delimiters.
func splitChainedCommands(cmd string) []string {
	var parts []string
	for _, segment := range strings.Split(cmd, "&&") {
		for _, sub := range strings.Split(segment, ";") {
			trimmed := strings.TrimSpace(sub)
			if trimmed != "" {
				parts = append(parts, trimmed)
			}
		}
	}
	return parts
}

// DirectSend bypasses all safety checks — used by @session manual mode.
func DirectSend(tc TmuxClient, sessionName, text string) error {
	// Verify session exists via has-session.
	if _, err := tc.Exec(tmux.HasSessionCmd(sessionName)); err != nil {
		return fmt.Errorf("session %q not found", sessionName)
	}
	return sendText(tc, sessionName, text)
}

// ensureSendable runs the pre-send gates and, on a bare-shell refusal with a
// healer wired in, recovers the session programmatically (relaunch the agent,
// wait for readiness, re-gate). Shared by every gate-then-send tool
// (send_to_session, relay_message, restore_context) and the direct dispatch
// paths (SafeSendWithHealer) so self-healing is uniform.
func ensureSendable(ctx context.Context, tc TmuxClient, healer SessionHealer, session string) error {
	if _, err := validateSendTarget(tc, session); err != nil {
		if !errors.Is(err, ErrBareShell) || healer == nil {
			return err
		}
		if herr := healer.HealSession(session); herr != nil {
			return fmt.Errorf("auto-recover session %q failed: %v (original: %v)", session, herr, err)
		}
		if werr := WaitForAgent(ctx, tc, session, 30*time.Second); werr != nil {
			return werr
		}
		if _, err := validateSendTarget(tc, session); err != nil {
			return err
		}
	}
	return nil
}

// SafeSendWithHealer is the gated send for program-initiated dispatches
// (voice-plan confirm, kanban): it runs the same pre-send gates as the
// send_to_session tool, and on a bare shell it heals the session first
// (relaunch the agent from the snapshot, wait for readiness) instead of
// failing — session recovery is the program's job, never the caller's.
func SafeSendWithHealer(ctx context.Context, tc TmuxClient, notifier Notifier, healer SessionHealer, session, message string) error {
	if err := ensureSendable(ctx, tc, healer, session); err != nil {
		return err
	}
	return SendConfirmed(ctx, tc, notifier, session, message)
}

// Ensure unused import is not needed.
var _ = strings.TrimSpace
