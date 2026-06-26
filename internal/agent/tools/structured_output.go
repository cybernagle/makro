package tools

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/naglezhang/makro/internal/tmux"
	"github.com/naglezhang/makro/internal/util"
)

type StructuredOutput struct {
	RawOutput            string   `json:"raw_output"`
	Status               string   `json:"status"`
	LastUserMessage      string   `json:"last_user_message"`
	LastAssistantMessage string   `json:"last_assistant_message"`
	Errors               []string `json:"errors"`
	FilesModified        []string `json:"files_modified"`
	Timestamp            string   `json:"timestamp"`
}

var (
	runningRe      = regexp.MustCompile(`(?i)(running…|running command|⏺\s*(?:executing|running)\s)`)
	errorRe        = regexp.MustCompile(`(?i)(error[:：]|fatal[:：]|panic[:：]|FAIL!?\b)`)
	fileOpRe       = regexp.MustCompile(`⏺\s+(?:Read|Edit|Create|Write|Delete|Modified|Created|Writing to|Reading)\s+(?:file:?\s*)?(\S+)`)
	userMsgRe      = regexp.MustCompile(`^>\s+(.+)`)
	assistantMsgRe = regexp.MustCompile(`⏺\s+(.+)`)
	selectionRe    = regexp.MustCompile(`❯\s*\d+\.\s*(.+)`)
	// trustDialogRe matches Claude Code's startup trust/onboarding screen by
	// distinctive phrasing. Rendering varies by version (sometimes a numbered
	// ❯ selector caught by selectionRe above, sometimes a plain selector), so
	// match on words rather than cursor glyphs. Phrases are kept specific to
	// avoid false-positives on ordinary agent output.
	trustDialogRe = regexp.MustCompile(`(?i)(trust the files in this folder|yes, i trust the files|leave and come back|do you trust the files)`)
)

// ReadStructuredOutput captures and parses the full pane output from a session.
func ReadStructuredOutput(tc TmuxClient, sessionName string) (*StructuredOutput, error) {
	raw, err := tc.Exec(tmux.CapturePaneAllCmd(sessionName))
	if err != nil {
		return nil, fmt.Errorf("capture pane %q: %w", sessionName, err)
	}
	out := parseStructuredOutput(raw)
	return &out, nil
}

func parseStructuredOutput(raw string) StructuredOutput {
	lines := strings.Split(raw, "\n")
	tailStart := max(0, len(lines)-80)
	tail := lines[tailStart:]
	return StructuredOutput{
		RawOutput:            raw,
		Status:               detectStatus(tail),
		LastUserMessage:      extractLastUserMessage(lines),
		LastAssistantMessage: extractLastAssistantMessage(lines),
		Errors:               extractErrors(lines),
		FilesModified:        extractFiles(lines),
		Timestamp:            time.Now().Format(time.RFC3339),
	}
}

func detectStatus(lines []string) string {
	start := max(0, len(lines)-30)
	recent := lines[start:]

	// Check for selection prompt first — highest priority.
	for _, line := range recent {
		if selectionRe.MatchString(line) {
			return "waiting_input"
		}
	}

	// Check for activity indicators.
	hasRunning := false
	hasThinking := false
	for _, line := range recent {
		if runningRe.MatchString(line) {
			hasRunning = true
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "⏺") {
			hasThinking = true
		}
	}
	if hasRunning {
		return "executing_command"
	}
	if hasThinking {
		return "thinking"
	}
	for i := len(recent) - 1; i >= 0; i-- {
		if errorRe.MatchString(recent[i]) {
			return "error"
		}
	}
	return "working"
}

// agentReadyToSend reports whether the coding agent in a pane is at its input
// prompt (ready to receive a task) versus showing an interactive Yes/No
// selection (startup trust dialog, in-turn permission prompt, model picker, …).
//
// Why this exists alongside checkAgentAlive: checkAgentAlive only confirms the
// agent PROCESS exists. Right after `claude` launches — before its input box
// is ready, or while the trust dialog is still up — the process is alive but
// the pane is showing a selection. Sending a task then answers the selection
// instead of becoming a prompt (the "raw natural language into a Yes/No" bug),
// so the caller refuses when this returns false.
//
// Discriminator: any numbered "❯ N." selection (selectionRe) or trust-screen
// phrasing (trustDialogRe) in the recent pane tail means "asking", not "ready".
// We only need to detect the negative — the absence of both means ready. A bare
// "❯ " input cursor has no digit and no trust phrase, so it correctly reads as
// ready. If the pane can't be read (just created, nothing rendered yet), we do
// not block — agent-alive already passed — and let the send through.
func agentReadyToSend(tc TmuxClient, sessionName string) (bool, string) {
	raw, err := tc.Exec(tmux.CapturePaneRangeCmd(sessionName, 40, 0))
	if err != nil || strings.TrimSpace(raw) == "" {
		return true, ""
	}
	lines := strings.Split(raw, "\n")
	start := len(lines) - 30
	if start < 0 {
		start = 0
	}
	for _, line := range lines[start:] {
		if selectionRe.MatchString(line) || trustDialogRe.MatchString(line) {
			return false, "agent is showing a Yes/No selection (trust/permission); resolve it in that pane, then resend"
		}
	}
	return true, ""
}

func extractLastUserMessage(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		if m := userMsgRe.FindStringSubmatch(lines[i]); len(m) > 1 {
			return m[1]
		}
	}
	return ""
}

func extractLastAssistantMessage(lines []string) string {
	// Prefer non-tool-call messages.
	for i := len(lines) - 1; i >= 0; i-- {
		if m := assistantMsgRe.FindStringSubmatch(lines[i]); len(m) > 1 {
			text := m[1]
			if !strings.Contains(text, "file:") && !runningRe.MatchString(text) {
				return util.Truncate(text, 500)
			}
		}
	}
	// Fallback: any ⏺ line.
	for i := len(lines) - 1; i >= 0; i-- {
		if m := assistantMsgRe.FindStringSubmatch(lines[i]); len(m) > 1 {
			return util.Truncate(m[1], 500)
		}
	}
	return ""
}

func extractErrors(lines []string) []string {
	var errs []string
	seen := make(map[string]bool)
	for _, line := range lines {
		if errorRe.MatchString(line) {
			e := strings.TrimSpace(line)
			if e != "" && !seen[e] {
				seen[e] = true
				errs = append(errs, e)
			}
		}
	}
	return errs
}

func extractFiles(lines []string) []string {
	var files []string
	seen := make(map[string]bool)
	for _, line := range lines {
		if m := fileOpRe.FindStringSubmatch(line); len(m) > 1 {
			f := m[1]
			if !seen[f] {
				seen[f] = true
				files = append(files, f)
			}
		}
	}
	return files
}

func structuredToJSON(out *StructuredOutput) (string, error) {
	data, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("marshal structured output: %w", err)
	}
	return string(data), nil
}
