package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func NewWaitUntilIdleTool(tc TmuxClient, notifier Notifier) Tool {
	return Tool{
		Name:        "wait_until_idle",
		Description: "Poll a session until the agent returns to idle state, or timeout. Returns 'blocked' if a confirmation prompt is pending and needs a response (use assess_confirmation + respond_confirmation to handle).",
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

			result, waited := pollUntilIdle(ctx, tc, sessionName, timeoutSec, notifier)

			data := map[string]any{
				"status":         result["status"],
				"waited_seconds": fmt.Sprintf("%.1f", waited.Seconds()),
			}
			if v, ok := result["pending_prompt"]; ok {
				data["pending_prompt"] = v
			}
			if v, ok := result["pending_type"]; ok {
				data["pending_type"] = v
			}
			jsonBytes, _ := json.Marshal(data)
			return string(jsonBytes), nil
		},
	}
}

func pollUntilIdle(ctx context.Context, tc TmuxClient, sessionName string, timeoutSec int, notifier Notifier) (map[string]string, time.Duration) {
	deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)
	start := time.Now()
	var notifyCh <-chan struct{}

	if notifier != nil {
		// Drop stale notifications from earlier tasks before waiting for the
		// current one; otherwise a previous stop event can wake this poll
		// immediately even though the agent is still working.
		notifier.Clear(sessionName)
		notifyCh = notifier.WaitCh(sessionName)
		defer notifier.Clear(sessionName)
	}

	for {
		if ctx.Err() != nil {
			return map[string]string{"status": "error"}, time.Since(start)
		}

		out, err := readStructured(tc, sessionName)
		if err != nil {
			return map[string]string{"status": "error"}, time.Since(start)
		}

		if out.PendingConfirmation != nil {
			return map[string]string{
				"status":         "blocked",
				"pending_prompt": out.PendingConfirmation.Prompt,
				"pending_type":   out.PendingConfirmation.Type,
			}, time.Since(start)
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
			// Continue polling.
		case <-notifyCh:
			// Agent reported stop via hook. Brief settle then verify.
			time.Sleep(100 * time.Millisecond)
			out, err = readStructured(tc, sessionName)
			if err == nil && isIdle(out) {
				return map[string]string{"status": "idle"}, time.Since(start)
			}
			// The notification did not correspond to the idle state we need.
			// Re-arm the waiter for the next stop event.
			if notifier != nil {
				notifier.Clear(sessionName)
				notifyCh = notifier.WaitCh(sessionName)
			}
		}
	}
}

func isIdle(out *StructuredOutput) bool {
	if out.PendingConfirmation != nil {
		return false
	}
	if out.Status == "completed" {
		return true
	}
	if hasBarePrompt(out.RawOutput) && out.Status != "executing_command" && out.Status != "waiting_input" {
		return true
	}
	return false
}

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
