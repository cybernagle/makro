package tools

import (
	"context"
	"strings"
)

func NewListWatchersTool(gm Guardian) Tool {
	return Tool{
		Name:        "list_watchers",
		Description: "List all sessions currently being watched by guardians.",
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			active := gm.ActiveGuardians()
			if len(active) == 0 {
				return "No active guardians.", nil
			}
			return "Watching: " + strings.Join(active, ", "), nil
		},
	}
}
