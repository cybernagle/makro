package tools

import (
	"fmt"

	"github.com/naglezhang/fingersaver/internal/tmux"
)

func sendText(tc TmuxClient, sessionName, text string) error {
	if isShortResponse(text) {
		if _, err := tc.Exec(tmux.SendKeysLiteralCmd(sessionName, text)); err != nil {
			return fmt.Errorf("send to %q: %w", sessionName, err)
		}
		if _, err := tc.Exec(tmux.SendEnterCmd(sessionName)); err != nil {
			return fmt.Errorf("send enter to %q: %w", sessionName, err)
		}
		return nil
	}
	payload := fmt.Sprintf("\033[200~%s\033[201~\r", text)
	if _, err := tc.Exec(tmux.SendKeysLiteralCmd(sessionName, payload)); err != nil {
		return fmt.Errorf("send to %q: %w", sessionName, err)
	}
	return nil
}

func isShortResponse(text string) bool {
	return len(text) <= 10
}
