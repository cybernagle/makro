package tools

import (
	"context"
	"fmt"
)

func NewUnwatchSessionTool(gm Guardian) Tool {
	return Tool{
		Name:        "unwatch_session",
		Description: "Stop watching a session. The guardian will no longer monitor or auto-respond.",
		Parameters: []Param{
			{Name: "name", Type: "string", Description: "Session name to stop watching", Required: true},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			name, _ := args["name"].(string)
			if name == "" {
				return "", fmt.Errorf("name is required")
			}
			if err := gm.Stop(name); err != nil {
				return "", err
			}
			return fmt.Sprintf("Guardian stopped for %q.", name), nil
		},
	}
}
