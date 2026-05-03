package tools

import (
	"fmt"
	"strings"

	"github.com/naglezhang/fingersaver/internal/tmux"
)

// sendText sends text to a tmux session, handling multi-line content correctly
// to avoid triggering bracket paste mode. For multi-line text, it sends each
// line individually to prevent the terminal from detecting a rapid character
// sequence as a paste event.
func sendText(tc TmuxClient, sessionName, text string) error {
	if !strings.Contains(text, "\n") {
		return sendSingleLine(tc, sessionName, text)
	}
	return sendMultiLine(tc, sessionName, text)
}

func sendSingleLine(tc TmuxClient, sessionName, text string) error {
	if text != "" {
		if _, err := tc.Exec(tmux.SendKeysLiteralCmd(sessionName, text)); err != nil {
			return fmt.Errorf("send to %q: %w", sessionName, err)
		}
	}
	if _, err := tc.Exec(tmux.SendEnterCmd(sessionName)); err != nil {
		return fmt.Errorf("send enter to %q: %w", sessionName, err)
	}
	return nil
}

func sendMultiLine(tc TmuxClient, sessionName, text string) error {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if line != "" {
			if _, err := tc.Exec(tmux.SendKeysLiteralCmd(sessionName, line)); err != nil {
				return fmt.Errorf("send line %d to %q: %w", i+1, sessionName, err)
			}
		}
		// Between lines: send C-j (newline) to insert a line break without
		// submitting, then a final Enter after the last line to submit.
		if i < len(lines)-1 {
			if _, err := tc.Exec(tmux.SendCJCmd(sessionName)); err != nil {
				return fmt.Errorf("send newline to %q: %w", sessionName, err)
			}
		}
	}

	if _, err := tc.Exec(tmux.SendEnterCmd(sessionName)); err != nil {
		return fmt.Errorf("send enter to %q: %w", sessionName, err)
	}
	return nil
}
