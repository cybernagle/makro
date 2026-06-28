package tools

import (
	"fmt"

	"github.com/naglezhang/makro/internal/tmux"
)

func sendText(tc TmuxClient, sessionName, text string) error {
	// Wrap multi-line / special content in a bracketed paste so the agent's
	// input box receives it verbatim; short plain strings go literal. Neither
	// path embeds the submit — both press Enter explicitly below.
	body := text
	if !isShortResponse(text) {
		body = fmt.Sprintf("\033[200~%s\033[201~", text)
	}
	// Type the body AND press Enter in a single tmux invocation (the two
	// commands joined by tmux's ";" separator). Two separate tc.Exec calls
	// race: if the Enter call is lost or lands during a pane redraw, the body
	// sits in the agent's input box unsubmitted and the turn never starts.
	// tc.Exec splits the command on whitespace, so the bare ";" becomes its own
	// argv token, which tmux parses as a command separator (the same trick
	// cmd/gui's sendToTmuxSession relies on). Both short and long paths now
	// submit via this explicit Enter; previously only the short path did and
	// the paste path relied on a trailing \r inside the paste payload, which is
	// less reliable across redraws.
	cmd := tmux.SendKeysLiteralCmd(sessionName, body) + " ; " + tmux.SendEnterCmd(sessionName)
	if _, err := tc.Exec(cmd); err != nil {
		return fmt.Errorf("send to %q: %w", sessionName, err)
	}
	return nil
}

func isShortResponse(text string) bool {
	return len(text) <= 10
}
