package tools

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/naglezhang/fingersaver/internal/tmux"
	"github.com/naglezhang/fingersaver/internal/util"
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

			// Check agent is alive and is a known agent.
			status := checkAgentAlive(tc, name)
			if !status.Alive {
				return "", fmt.Errorf("session %q: %s", name, status.Reason)
			}

			// Blocklist check.
			if blocked, pattern := isBlockedCommand(message); blocked {
				return "", fmt.Errorf("command blocked by safety policy: matched pattern %q", pattern)
			}

			if err := sendText(tc, name, message); err != nil {
				return "", err
			}

			return fmt.Sprintf("Sent to %q (%s): %s", name, status.Agent, util.Truncate(message, 50)), nil
		},
	}
}

// blockedPatterns are hard-blocked — these commands are never allowed.
var blockedPatterns = []struct {
	re      *regexp.Regexp
	pattern string
}{
	{regexp.MustCompile(`(?i)\brm\s+-[a-zA-Z]*[rf][a-zA-Z]*[rf][a-zA-Z]*\s`), "rm -rf"},
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
	for _, p := range blockedPatterns {
		if p.re.MatchString(message) {
			return true, p.pattern
		}
	}
	return false, ""
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
