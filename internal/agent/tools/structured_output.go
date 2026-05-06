package tools

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/naglezhang/fingersaver/internal/tmux"
	"github.com/naglezhang/fingersaver/internal/util"
)

type StructuredOutput struct {
	RawOutput            string               `json:"raw_output"`
	Status               string               `json:"status"`
	LastUserMessage      string               `json:"last_user_message"`
	LastAssistantMessage string               `json:"last_assistant_message"`
	PendingConfirmation  *PendingConfirmation `json:"pending_confirmation"`
	Errors               []string             `json:"errors"`
	FilesModified        []string             `json:"files_modified"`
	Timestamp            string               `json:"timestamp"`
}

type PendingConfirmation struct {
	Type   string `json:"type"`
	Prompt string `json:"prompt"`
}

var (
	confirmationRe = regexp.MustCompile(`(?i)(do you want|would you like|allow this|proceed\?|confirm\?|should i|approve)`)
	selectionRe    = regexp.MustCompile(`❯\s*\d+\.\s*(.+)`)
	runningRe      = regexp.MustCompile(`(?i)(running…|running command|⏺\s*(?:executing|running)\s)`)
	errorRe        = regexp.MustCompile(`(?i)(error[:：]|fatal[:：]|panic[:：]|FAIL!?\b)`)
	fileOpRe       = regexp.MustCompile(`⏺\s+(?:Read|Edit|Create|Write|Delete|Modified|Created|Writing to|Reading)\s+(?:file:?\s*)?(\S+)`)
	userMsgRe      = regexp.MustCompile(`^>\s+(.+)`)
	assistantMsgRe = regexp.MustCompile(`⏺\s+(.+)`)
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
	// For status detection and confirmation, only look at the tail to avoid
	// stale state from earlier in the scrollback.
	tailStart := max(0, len(lines)-80)
	tail := lines[tailStart:]
	return StructuredOutput{
		RawOutput:            raw,
		Status:               detectStatus(tail),
		LastUserMessage:      extractLastUserMessage(lines),
		LastAssistantMessage: extractLastAssistantMessage(lines),
		PendingConfirmation:  detectConfirmation(tail),
		Errors:               extractErrors(lines),
		FilesModified:        extractFiles(lines),
		Timestamp:            time.Now().Format(time.RFC3339),
	}
}

func detectStatus(lines []string) string {
	start := max(0, len(lines)-30)
	recent := lines[start:]

	for _, line := range recent {
		if selectionRe.MatchString(line) {
			return "waiting_input"
		}
	}
	for _, line := range recent {
		if confirmationRe.MatchString(line) {
			return "waiting_input"
		}
	}
	for i := len(recent) - 1; i >= 0; i-- {
		if runningRe.MatchString(recent[i]) {
			return "executing_command"
		}
	}
	for i := len(recent) - 1; i >= 0; i-- {
		if errorRe.MatchString(recent[i]) {
			return "error"
		}
	}
	for i := len(recent) - 1; i >= 0; i-- {
		if strings.HasPrefix(strings.TrimSpace(recent[i]), "⏺") {
			return "thinking"
		}
	}
	return "completed"
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

func detectConfirmation(lines []string) *PendingConfirmation {
	start := max(0, len(lines)-30)
	recent := lines[start:]

	selectionIdx := -1
	for i := len(recent) - 1; i >= 0; i-- {
		if selectionRe.MatchString(recent[i]) {
			selectionIdx = i
			break
		}
	}
	if selectionIdx >= 0 {
		var promptLines []string
		for i := selectionIdx - 1; i >= max(0, selectionIdx-5); i-- {
			line := strings.TrimSpace(recent[i])
			if line == "" {
				break
			}
			promptLines = append([]string{line}, promptLines...)
		}
		prompt := "Confirm"
		if len(promptLines) > 0 {
			prompt = strings.Join(promptLines, " ")
		}
		return &PendingConfirmation{Type: "yes_no", Prompt: prompt}
	}

	for i := len(recent) - 1; i >= 0; i-- {
		if confirmationRe.MatchString(recent[i]) {
			return &PendingConfirmation{Type: "yes_no", Prompt: strings.TrimSpace(recent[i])}
		}
	}
	return nil
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
