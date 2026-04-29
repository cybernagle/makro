package adapters

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/naglezhang/fingersaver/internal/tmux"
)

// ClaudeAdapter manages Claude Code CLI running inside a tmux session.
type ClaudeAdapter struct {
	tc tmuxClient
}

func NewClaudeAdapter(tc tmuxClient) *ClaudeAdapter {
	return &ClaudeAdapter{tc: tc}
}

func (c *ClaudeAdapter) Name() string { return "claude" }

func (c *ClaudeAdapter) Launch(ctx context.Context, sessionName, workingDir string) error {
	cmd := tmux.SendKeysCmd(sessionName, "claude --dangerously-skip-permissions")
	if _, err := c.tc.Exec(cmd); err != nil {
		return fmt.Errorf("launch claude: %w", err)
	}
	// Send Enter to confirm.
	if _, err := c.tc.Exec(tmux.SendEnterCmd(sessionName)); err != nil {
		return fmt.Errorf("confirm claude launch: %w", err)
	}
	return nil
}

func (c *ClaudeAdapter) SendMessage(ctx context.Context, sessionName, text string) error {
	// Send the message as literal text, then Enter.
	cmd := tmux.SendKeysLiteralCmd(sessionName, text)
	if _, err := c.tc.Exec(cmd); err != nil {
		return fmt.Errorf("send to claude: %w", err)
	}
	if _, err := c.tc.Exec(tmux.SendEnterCmd(sessionName)); err != nil {
		return fmt.Errorf("confirm message: %w", err)
	}
	return nil
}

// Claude Code prompt patterns.
var (
	claudeReadyPattern     = regexp.MustCompile(`(?m)>\s*$`)
	claudeWorkingPattern   = regexp.MustCompile(`(?m)(Thinking|Reading|Writing|Editing|Running|Searching|Analyzing)`)
	claudeCompletedPattern = regexp.MustCompile(`(?m)(completed|finished|done|Task complete)`)
	claudeErrorPattern     = regexp.MustCompile(`(?m)(Error|error:|fatal:)`)
)

func (c *ClaudeAdapter) IsRunning(output string) bool {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return false
	}
	// Running if there's output and no ready prompt at the end.
	return !claudeReadyPattern.MatchString(trimmed)
}

func (c *ClaudeAdapter) ParseOutput(output string) AgentOutput {
	out := AgentOutput{LastLine: lastLine(output)}

	if claudeCompletedPattern.MatchString(output) {
		out.Completed = true
		return out
	}

	if claudeErrorPattern.MatchString(output) {
		matches := claudeErrorPattern.FindStringSubmatch(output)
		if len(matches) > 0 {
			out.Error = matches[0]
		}
		return out
	}

	if claudeWorkingPattern.MatchString(output) {
		out.Working = true
		return out
	}

	if claudeReadyPattern.MatchString(output) {
		out.Ready = true
		return out
	}

	return out
}

func (c *ClaudeAdapter) StopConfig() StopConfig {
	return StopConfig{
		CompletionMarker: "Task complete",
		ErrorMarkers:     []string{"Error:", "fatal:"},
		StopCommand:      "/stop",
	}
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(lines[len(lines)-1])
}
