package tools

import (
	"context"
	"fmt"

	"github.com/naglezhang/fingersaver/internal/tmux"
)

func NewKillSessionTool(tc TmuxClient) Tool {
	return Tool{
		Name:        "kill_session",
		Description: "Kill and remove a tmux session",
		Parameters: []Param{
			{Name: "name", Type: "string", Description: "Session name to kill", Required: true},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			name, _ := args["name"].(string)
			if name == "" {
				return "", fmt.Errorf("name is required")
			}
			_, err := tc.Exec(tmux.KillSessionCmd(name))
			if err != nil {
				return "", fmt.Errorf("kill session %q: %w", name, err)
			}
			return fmt.Sprintf("Session %q killed.", name), nil
		},
	}
}
