package tools

import (
	"fmt"
	"strings"

	"github.com/naglezhang/fingersaver/internal/tmux"
)

// sendText sends text to a tmux session using bracket paste mode.
// All text and the submit keystroke are sent as a single payload to avoid
// timing races with TUI apps like Copilot that may drop separate events.
func sendText(tc TmuxClient, sessionName, text string) error {
	if !strings.Contains(text, "\n") {
		return sendSingleLine(tc, sessionName, text)
	}
	return sendMultiLine(tc, sessionName, text)
}

func sendSingleLine(tc TmuxClient, sessionName, text string) error {
	payload := fmt.Sprintf("\033[200~%s\033[201~\r", text)
	if _, err := tc.Exec(tmux.SendKeysLiteralCmd(sessionName, payload)); err != nil {
		return fmt.Errorf("send to %q: %w", sessionName, err)
	}
	return nil
}

func sendMultiLine(tc TmuxClient, sessionName, text string) error {
	lines := strings.Split(text, "\n")
	var buf strings.Builder
	buf.WriteString("\033[200~")
	for i, line := range lines {
		buf.WriteString(line)
		if i < len(lines)-1 {
			buf.WriteString("\n")
		}
	}
	buf.WriteString("\033[201~\r")

	if _, err := tc.Exec(tmux.SendKeysLiteralCmd(sessionName, buf.String())); err != nil {
		return fmt.Errorf("send multi-line to %q: %w", sessionName, err)
	}
	return nil
}
