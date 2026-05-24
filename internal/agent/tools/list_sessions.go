package tools

import (
	"context"
	"fmt"
)

func NewListSessionsTool(tc TmuxClient) Tool {
	return Tool{
		Name:        "list_sessions",
		Description: "List all tmux sessions managed by Makro",
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			output, err := tc.Exec("list-sessions")
			if err != nil {
				return "", fmt.Errorf("list sessions: %w", err)
			}
			if output == "" {
				return "No sessions found.", nil
			}
			return output, nil
		},
	}
}
