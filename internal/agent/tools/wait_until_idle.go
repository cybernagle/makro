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

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return map[string]string{"status": "timeout"}, time.Since(start)
		}

		// Check process alive every 30s as a fallback for crashed agents.
		checkInterval := 30 * time.Second
		if remaining < checkInterval {
			checkInterval = remaining
		}

		select {
		case <-ctx.Done():
			return map[string]string{"status": "error"}, time.Since(start)
		case <-time.After(checkInterval):
			if alive := checkAgentAlive(tc, sessionName); !alive.Alive {
				log.Printf("[wait_until_idle] agent process dead for %s: %s", sessionName, alive.Reason)
				return map[string]string{"status": "agent_dead", "reason": alive.Reason}, time.Since(start)
			}
		case <-notifyCh:
			log.Printf("[wait_until_idle] hook notification received for %s", sessionName)
			time.Sleep(500 * time.Millisecond)

			// Determine action from hook type.
			if notifier != nil && notifier.LastStatus(sessionName) == "permission" {
				return map[string]string{"status": "blocked"}, time.Since(start)
			}
			return map[string]string{"status": "idle"}, time.Since(start)
		}
	}
}
