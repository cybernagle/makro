package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/naglezhang/makro/internal/tmux"
)

func NewRespondConfirmationTool(tc TmuxClient) Tool {
	return Tool{
		Name:        "respond_confirmation",
		Description: "Send an approval or rejection to a session's pending confirmation prompt. Sends '1' (Yes) or '2' (No) followed by Enter.",
		Parameters: []Param{
			{Name: "session_name", Type: "string", Description: "Session name to respond to", Required: true},
			{Name: "approve", Type: "boolean", Description: "true to approve (Yes/1), false to reject (No/2)", Required: true},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			sessionName, _ := args["session_name"].(string)
			if sessionName == "" {
				return "", fmt.Errorf("session_name is required")
			}

			approve := false
			if v, ok := args["approve"].(bool); ok {
				approve = v
			}

			// Send key press via send-keys (not -l) so TUI selection menus
			// interpret it as a key event, not literal text.
			key := "2"
			label := "No"
			if approve {
				key = "1"
				label = "Yes"
			}

			if _, err := tc.Exec(tmux.SendKeysCmd(sessionName, key)); err != nil {
				return "", fmt.Errorf("send %s to %q: %w", label, sessionName, err)
			}

			data, _ := json.Marshal(map[string]any{
				"session": sessionName,
				"sent":    label,
			})
			return string(data), nil
		},
	}
}
