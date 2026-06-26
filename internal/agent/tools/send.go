package tools

import (
	"fmt"

	"github.com/naglezhang/makro/internal/tmux"
)

func sendText(tc TmuxClient, sessionName, text string) error {
	if isShortResponse(text) {
		// Type the text AND press Enter in a single tmux invocation (the two
		// commands joined by tmux's ";" separator). Two separate tc.Exec calls
		// race: if the Enter call is lost or lands during a pane redraw, the
		// text sits in the agent's input box unsubmitted and the turn never
		// starts — the "reaches the box but never sends" hang. tc.Exec splits
		// the command on whitespace, so the bare ";" becomes its own argv
		// token, which tmux parses as a command separator (the same trick
		// cmd/gui's sendToTmuxSession already relies on).
		cmd := tmux.SendKeysLiteralCmd(sessionName, text) + " ; " + tmux.SendEnterCmd(sessionName)
		if _, err := tc.Exec(cmd); err != nil {
			return fmt.Errorf("send to %q: %w", sessionName, err)
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
