package tools

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/naglezhang/makro/internal/tmux"
	"github.com/naglezhang/makro/internal/util"
)

func NewSendToSessionTool(tc TmuxClient) Tool {
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

			// Check session exists.
			if !IsSessionAlive(tc, name) {
				return "", fmt.Errorf("session %q not found", name)
			}

			// Blocklist check.
			if blocked, pattern := isBlockedCommand(message); blocked {
				return "", fmt.Errorf("command blocked by safety policy: matched pattern %q", pattern)
			}

			if err := sendText(tc, name, message); err != nil {
				return "", err
			}

			return fmt.Sprintf("Sent to %q: %s", name, util.Truncate(message, 50)), nil
		},
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

// IsSessionAlive checks if a session exists. Exported for orchestrator use.
func IsSessionAlive(tc TmuxClient, sessionName string) bool {
	_, err := tc.Exec(tmux.HasSessionCmd(sessionName))
	return err == nil
}

// Ensure unused import is not needed.
var _ = strings.TrimSpace
