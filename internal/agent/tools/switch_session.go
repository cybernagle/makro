package tools

import (
	"context"
	"fmt"
)

func NewSwitchSessionTool(tc TmuxClient) Tool {
	return Tool{
		Name:        "switch_session",
		Description: "Switch the active tmux session viewer to a different session",
		Parameters: []Param{
			{Name: "name", Type: "string", Description: "Session name to switch to", Required: true},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			name, _ := args["name"].(string)
			if name == "" {
				return "", fmt.Errorf("name is required")
			}
			s := tc.State().FindSession(name)
			if s == nil {
				return "", fmt.Errorf("session %q not found", name)
			}
			return name, nil
		},
	}
}
