package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/naglezhang/fingersaver/internal/tmux"
)

const defaultPageSize = 200

func NewReadSessionOutputTool(tc TmuxClient) Tool {
	return Tool{
		Name:        "read_session_output",
		Description: "Read output from a tmux session with paging support. Returns lines, total_lines, and has_more for pagination.",
		Parameters: []Param{
			{Name: "name", Type: "string", Description: "Session name", Required: true},
			{Name: "lines", Type: "number", Description: "Number of lines to read per page (default 200)"},
			{Name: "offset", Type: "number", Description: "Skip this many lines from the end (for paging backward, default 0)"},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			name, _ := args["name"].(string)
			if name == "" {
				return "", fmt.Errorf("name is required")
			}

			lines := defaultPageSize
			if v, ok := args["lines"].(float64); ok && v > 0 {
				lines = int(v)
			}
			offset := 0
			if v, ok := args["offset"].(float64); ok && v > 0 {
				offset = int(v)
			}

			// Capture full scrollback to count total lines.
			all, err := tc.Exec(tmux.CapturePaneAllCmd(name))
			if err != nil {
				return "", fmt.Errorf("capture pane %q: %w", name, err)
			}
			if all == "" {
				return "(empty)", nil
			}

			allLines := splitNonEmpty(all)
			totalLines := len(allLines)

			if totalLines == 0 {
				return "(empty)", nil
			}

			// Calculate slice: from the end, skip offset, take lines.
			end := totalLines - offset
			if end < 0 {
				end = 0
			}
			start := end - lines
			if start < 0 {
				start = 0
			}

			page := allLines[start:end]
			hasMore := start > 0

			result, _ := json.Marshal(map[string]any{
				"content":     strings.Join(page, "\n"),
				"total_lines": totalLines,
				"page_start":  start + 1, // 1-indexed for readability
				"page_end":    end,
				"has_more":    hasMore,
			})
			return string(result), nil
		},
	}
}

// splitNonEmpty splits by newline, removing trailing empty lines.
func splitNonEmpty(s string) []string {
	lines := strings.Split(s, "\n")
	// Trim trailing empty lines.
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
