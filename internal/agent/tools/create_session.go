package tools

import (
	"context"
	"fmt"

	"github.com/naglezhang/makro/internal/tmux"
)

func NewCreateSessionTool(tc TmuxClient) Tool {
	return Tool{
		Name:        "create_session",
		Description: "Create a new tmux session. Returns the session name on success.",
		Parameters: []Param{
			{Name: "name", Type: "string", Description: "Session name", Required: true},
			{Name: "working_dir", Type: "string", Description: "Working directory for the session"},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			name, _ := args["name"].(string)
			if name == "" {
				return "", fmt.Errorf("name is required")
			}
			workingDir, _ := args["working_dir"].(string)
			_, err := tc.Exec(tmux.NewSessionCmd(name, workingDir, ""))
			if err != nil {
				return "", fmt.Errorf("create session %q: %w", name, err)
			}
			return fmt.Sprintf("Session %q created.", name), nil
		},
	}
}
