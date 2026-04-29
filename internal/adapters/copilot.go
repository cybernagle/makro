package adapters

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/naglezhang/fingersaver/internal/tmux"
)

// CopilotAdapter manages GitHub Copilot CLI running inside a tmux session.
type CopilotAdapter struct {
	tc tmuxClient
}

func NewCopilotAdapter(tc tmuxClient) *CopilotAdapter {
	return &CopilotAdapter{tc: tc}
}

func (c *CopilotAdapter) Name() string { return "copilot" }

func (c *CopilotAdapter) Launch(ctx context.Context, sessionName, workingDir string) error {
	cmd := tmux.SendKeysCmd(sessionName, "github-copilot-cli")
	if _, err := c.tc.Exec(cmd); err != nil {
		return fmt.Errorf("launch copilot: %w", err)
	}
	if _, err := c.tc.Exec(tmux.SendEnterCmd(sessionName)); err != nil {
		return fmt.Errorf("confirm copilot launch: %w", err)
	}
	return nil
}

func (c *CopilotAdapter) SendMessage(ctx context.Context, sessionName, text string) error {
	cmd := tmux.SendKeysLiteralCmd(sessionName, text)
	if _, err := c.tc.Exec(cmd); err != nil {
		return fmt.Errorf("send to copilot: %w", err)
	}
	if _, err := c.tc.Exec(tmux.SendEnterCmd(sessionName)); err != nil {
		return fmt.Errorf("confirm message: %w", err)
	}
	return nil
}

// Copilot CLI prompt patterns.
var (
	copilotReadyPattern     = regexp.MustCompile(`(?m)(>\s*$|copilot>\s*$)`)
	copilotWorkingPattern   = regexp.MustCompile(`(?m)(Generating|Processing|Analyzing|Writing)`)
	copilotCompletedPattern = regexp.MustCompile(`(?m)(completed|finished|done|Suggestion:)`)
	copilotErrorPattern     = regexp.MustCompile(`(?m)(Error|error:|fatal:)`)
)

func (c *CopilotAdapter) IsRunning(output string) bool {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return false
	}
	return !copilotReadyPattern.MatchString(trimmed)
}

func (c *CopilotAdapter) ParseOutput(output string) AgentOutput {
	out := AgentOutput{LastLine: lastLine(output)}

	if copilotCompletedPattern.MatchString(output) {
		out.Completed = true
		return out
	}

	if copilotErrorPattern.MatchString(output) {
		matches := copilotErrorPattern.FindStringSubmatch(output)
		if len(matches) > 0 {
			out.Error = matches[0]
		}
		return out
	}

	if copilotWorkingPattern.MatchString(output) {
		out.Working = true
		return out
	}

	if copilotReadyPattern.MatchString(output) {
		out.Ready = true
		return out
	}

	return out
}

func (c *CopilotAdapter) StopConfig() StopConfig {
	return StopConfig{
		CompletionMarker: "Suggestion:",
		ErrorMarkers:     []string{"Error:", "fatal:"},
		StopCommand:      "exit",
	}
}
