package tools

import (
	"context"
	"fmt"
)

func NewWatchSessionTool(gm Guardian) Tool {
	return Tool{
		Name:        "watch_session",
		Description: "Start a guardian that monitors a session for confirmation prompts. The guardian auto-responds: 'Yes' for safe operations, 'No' for risky ones. Multiple sessions can be watched simultaneously.",
		Parameters: []Param{
			{Name: "name", Type: "string", Description: "Session name to watch", Required: true},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			name, _ := args["name"].(string)
			if name == "" {
				return "", fmt.Errorf("name is required")
			}
			if err := gm.Watch(ctx, name); err != nil {
				return "", err
			}
			return fmt.Sprintf("Guardian started for %q. It will auto-approve safe operations and reject risky ones.", name), nil
		},
	}
}
