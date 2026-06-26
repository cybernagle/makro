package tools

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/naglezhang/makro/internal/tmux"
	"github.com/naglezhang/makro/internal/util"
)

func NewSendToSessionTool(tc TmuxClient, notifier Notifier) Tool {
	return Tool{
		Name:        "send_to_session",
		Description: "Send a command or message to a tmux session. Only works when a known coding agent (claude, copilot, codex) is alive. Destructive shell commands (rm -rf, curl|sh, etc.) are blocked. Follow with wait_until_idle to handle any confirmation prompts.",
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

			// Require a known coding agent to actually be running in the pane.
			// Without this gate the tool's own contract ("only works when a
			// known coding agent is alive") is a lie: sending into a
			// freshly-created session whose pane is still a bare shell dumps
			// raw natural-language text into the shell. checkAgentAlive also
			// covers the session-not-found case (the pane lookup errors).
			status := checkAgentAlive(tc, name)
			if !status.Alive {
				return "", fmt.Errorf("cannot send to %q: no coding agent (claude/copilot/codex) is running (%s); start the agent in that pane first", name, status.Reason)
			}

			// Don't send while the agent is showing an interactive Yes/No
			// selection (startup trust dialog or in-turn permission). The task
			// text would answer that selection instead of becoming a prompt.
			if ready, reason := agentReadyToSend(tc, name); !ready {
				return "", fmt.Errorf("cannot send to %q: %s", name, reason)
			}

			// Blocklist check.
			if blocked, pattern := isBlockedCommand(message); blocked {
				return "", fmt.Errorf("command blocked by safety policy: matched pattern %q", pattern)
			}

			// Snapshot notification state BEFORE sending so the confirmation
			// below only reacts to this send.
			before := uint64(0)
			if notifier != nil {
				before = notifier.Snapshot(name)
			}

			if err := sendText(tc, name, message); err != nil {
				return "", err
			}

			// Confirm the agent actually picked up the prompt (UserPromptSubmit
			// hook → agent_start notification). Catches the failure mode where
			// text reaches the input box but is never submitted: without this
			// the orchestrator would wait_until_idle for the full timeout on a
			// turn that never starts. Surface an error instead of hanging.
			if !confirmAgentStarted(ctx, notifier, name, before, submitConfirmGrace) {
				return "", fmt.Errorf("cannot send to %q: text was delivered but the agent did not start a turn within %s — the prompt may not have been submitted; check the pane and resend", name, submitConfirmGrace)
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
	select {
	case <-ch:
		return true
	case <-time.After(grace):
		return false
	case <-ctx.Done():
		return false
	}
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

// Ensure unused import is not needed.
var _ = strings.TrimSpace
