package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
)

func NewWaitUntilIdleTool(tc TmuxClient, notifier Notifier) Tool {
	return Tool{
		Name:        "wait_until_idle",
		Description: "Wait for a session agent to finish, then return its output. Combines waiting and reading — no need to call read_session_output afterwards.",
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

			// Always capture final output so the LLM doesn't need read_session_output.
			out, err := ReadStructuredOutput(tc, sessionName)
			data := map[string]any{
				"status":         result["status"],
				"waited_seconds": fmt.Sprintf("%.1f", waited.Seconds()),
			}
			if err == nil {
				lines := strings.Split(out.RawOutput, "\n")
				tailStart := len(lines) - 20
				if tailStart < 0 {
					tailStart = 0
				}
				data["output_tail"] = strings.Join(lines[tailStart:], "\n")
				data["status_detail"] = out.Status
				if len(out.Errors) > 0 {
					data["errors"] = out.Errors
				}
				if len(out.FilesModified) > 0 {
					data["files_modified"] = out.FilesModified
				}
			}
			if v, ok := result["pending_prompt"]; ok {
				data["pending_prompt"] = v
			}
			if v, ok := result["pending_type"]; ok {
				data["pending_type"] = v
			}
			if v, ok := result["reason"]; ok {
				data["reason"] = v
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
	var cancelNotify func()
	var lastSeen uint64

	if notifier != nil {
		lastSeen = notifier.Snapshot(sessionName)
		notifyCh, cancelNotify = notifier.WaitAfter(sessionName, lastSeen)
		defer func() {
			if cancelNotify != nil {
				cancelNotify()
			}
		}()
	}

	for {
		if ctx.Err() != nil {
			return map[string]string{"status": "error"}, time.Since(start)
		}

		if time.Now().After(deadline) {
			return map[string]string{"status": "timeout"}, time.Since(start)
		}

		// Agent process died (crash, killed) — this is abnormal.
		alive := checkAgentAlive(tc, sessionName)
		if !alive.Alive {
			log.Printf("[wait_until_idle] agent not alive: %s", alive.Reason)
			return map[string]string{"status": "error", "reason": "agent process not running: " + alive.Reason}, time.Since(start)
		}

		// Check for pending confirmation prompt.
		out, err := ReadStructuredOutput(tc, sessionName)
		if err == nil && out.PendingConfirmation != nil {
			return map[string]string{
				"status":         "blocked",
				"pending_prompt": out.PendingConfirmation.Prompt,
				"pending_type":   out.PendingConfirmation.Type,
			}, time.Since(start)
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
			// Stop hook fired — trust it.
			log.Printf("[wait_until_idle] stop hook received for %s", sessionName)
			time.Sleep(500 * time.Millisecond)

			// Check for confirmation before declaring idle.
			out, err = ReadStructuredOutput(tc, sessionName)
			if err == nil && out.PendingConfirmation != nil {
				return map[string]string{
					"status":         "blocked",
					"pending_prompt": out.PendingConfirmation.Prompt,
					"pending_type":   out.PendingConfirmation.Type,
				}, time.Since(start)
			}

			return map[string]string{"status": "idle"}, time.Since(start)
		}
	}
}
