package tools

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func NewWaitUntilIdleTool(tc TmuxClient) Tool {
	return Tool{
		Name:        "wait_until_idle",
		Description: "Poll a session until the agent returns to idle state (❯ prompt with no pending confirmation), or timeout",
		Parameters: []Param{
			{Name: "session_name", Type: "string", Description: "Session name to poll", Required: true},
			{Name: "timeout_seconds", Type: "number", Description: "Max wait time in seconds (default 300)"},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			sessionName, _ := args["session_name"].(string)
			if sessionName == "" {
				return "", fmt.Errorf("session_name is required")
			}

			timeoutSec := 300
			if v, ok := args["timeout_seconds"].(float64); ok && v > 0 {
				timeoutSec = int(v)
			}

			result, waited := pollUntilIdle(ctx, tc, sessionName, timeoutSec)
			return fmt.Sprintf(`{"status":"%s","waited_seconds":%.1f}`, result["status"], waited.Seconds()), nil
		},
	}
}

func pollUntilIdle(ctx context.Context, tc TmuxClient, sessionName string, timeoutSec int) (map[string]string, time.Duration) {
	deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)
	start := time.Now()

	for {
		if ctx.Err() != nil {
			return map[string]string{"status": "error"}, time.Since(start)
		}

		out, err := readStructured(tc, sessionName)
		if err != nil {
			return map[string]string{"status": "error"}, time.Since(start)
		}

		if isIdle(out) {
			return map[string]string{"status": "idle"}, time.Since(start)
		}

		if time.Now().After(deadline) {
			return map[string]string{"status": "timeout"}, time.Since(start)
		}

		wait := 3 * time.Second
		if remaining := time.Until(deadline); remaining < wait {
			wait = remaining
		}
		if wait <= 0 {
			return map[string]string{"status": "timeout"}, time.Since(start)
		}

		select {
		case <-ctx.Done():
			return map[string]string{"status": "error"}, time.Since(start)
		case <-time.After(wait):
		}
	}
}

// isIdle returns true if the agent is at an idle prompt — no pending
// confirmation, no active command, and a bare ❯ prompt is present.
func isIdle(out *StructuredOutput) bool {
	if out.PendingConfirmation != nil {
		return false
	}
	if out.Status == "completed" {
		return true
	}
	// Check for bare ❯ prompt (not a selection like ❯ 1. option).
	if hasBarePrompt(out.RawOutput) && out.Status != "executing_command" && out.Status != "waiting_input" {
		return true
	}
	return false
}

// hasBarePrompt checks if the raw output ends with a bare ❯ prompt line.
func hasBarePrompt(raw string) bool {
	lines := strings.Split(raw, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "❯") && !selectionRe.MatchString(line) {
			return true
		}
		return false
	}
	return false
}
