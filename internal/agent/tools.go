package agent

import (
	"context"
	"fmt"

	"github.com/naglezhang/fingersaver/internal/tmux"
)

type Tool struct {
	Name        string
	Description string
	Parameters  []Param
	Execute     func(ctx context.Context, args map[string]any) (string, error)
}

type Param struct {
	Name        string
	Type        string // "string", "number", "boolean"
	Description string
	Required    bool
}

// TmuxClient is the subset of tmux.TmuxClient that tools need.
type TmuxClient interface {
	Exec(cmd string) (string, error)
	State() *tmux.StateMirror
}

func NewListSessionsTool(tc TmuxClient) Tool {
	return Tool{
		Name:        "list_sessions",
		Description: "List all tmux sessions managed by FingerSaver",
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			output, err := tc.Exec(tmux.ListSessionsCmd())
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
			return fmt.Sprintf("Switched to session %q.", name), nil
		},
	}
}

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

func NewSendToSessionTool(tc TmuxClient) Tool {
	return Tool{
		Name:        "send_to_session",
		Description: "Send a command or message to a tmux session via send-keys",
		Parameters: []Param{
			{Name: "name", Type: "string", Description: "Session name", Required: true},
			{Name: "message", Type: "string", Description: "Text to send to the session", Required: true},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			name, _ := args["name"].(string)
			message, _ := args["message"].(string)
			if name == "" || message == "" {
				return "", fmt.Errorf("name and message are required")
			}
			_, err := tc.Exec(tmux.SendKeysLiteralCmd(name, message))
			if err != nil {
				return "", fmt.Errorf("send to %q: %w", name, err)
			}
			_, err = tc.Exec(tmux.SendEnterCmd(name))
			if err != nil {
				return "", fmt.Errorf("send enter to %q: %w", name, err)
			}
			return fmt.Sprintf("Sent to %q: %s", name, truncate(message, 50)), nil
		},
	}
}

func NewReadSessionOutputTool(tc TmuxClient) Tool {
	return Tool{
		Name:        "read_session_output",
		Description: "Read the current visible output from a tmux session's active pane",
		Parameters: []Param{
			{Name: "name", Type: "string", Description: "Session name", Required: true},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			name, _ := args["name"].(string)
			if name == "" {
				return "", fmt.Errorf("name is required")
			}
			output, err := tc.Exec(tmux.CapturePaneCmd(name))
			if err != nil {
				return "", fmt.Errorf("capture pane %q: %w", name, err)
			}
			if output == "" {
				return "(empty)", nil
			}
			return output, nil
		},
	}
}

func AllTools(tc TmuxClient) []Tool {
	return []Tool{
		NewListSessionsTool(tc),
		NewCreateSessionTool(tc),
		NewSwitchSessionTool(tc),
		NewKillSessionTool(tc),
		NewSendToSessionTool(tc),
		NewReadSessionOutputTool(tc),
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
