package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/naglezhang/fingersaver/internal/tmux"
)

const defaultPageSize = 500

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

			emptyResult := func() (string, error) {
				result, _ := json.Marshal(map[string]any{
					"content":     "",
					"total_lines": 0,
					"page_start":  0,
					"page_end":    0,
					"has_more":    false,
				})
				return string(result), nil
			}

			var allLines []string
			var totalLines int

			if offset == 0 {
				// Common case: read only the tail from tmux, no full scrollback needed.
				captureCount := lines + 1 // +1 to detect has_more
				raw, err := tc.Exec(tmux.CapturePaneRangeCmd(name, captureCount, 0))
				if err != nil {
					return "", fmt.Errorf("capture pane %q: %w", name, err)
				}
				if raw == "" {
					return emptyResult()
				}
				allLines = splitNonEmpty(raw)
				// If we got more lines than requested, there's more content above.
				hasMore := len(allLines) > lines
				if hasMore {
					allLines = allLines[len(allLines)-lines:]
				}
				// totalLines is approximate — only accurate if we didn't truncate.
				// For the no-offset case the LLM only needs the content and has_more.
				totalLines = len(allLines)
				if hasMore {
					// Get accurate total only when it matters for pagination display.
					full, err := tc.Exec(tmux.CapturePaneAllCmd(name))
					if err == nil && full != "" {
						totalLines = len(splitNonEmpty(full))
					}
				}

				page := allLines
				result, _ := json.Marshal(map[string]any{
					"content":     strings.Join(page, "\n"),
					"total_lines": totalLines,
					"page_start":  totalLines - len(page) + 1,
					"page_end":    totalLines,
					"has_more":    hasMore,
				})
				return string(result), nil
			}

			// Paging backward: need full scrollback for accurate slicing.
			all, err := tc.Exec(tmux.CapturePaneAllCmd(name))
			if err != nil {
				return "", fmt.Errorf("capture pane %q: %w", name, err)
			}
			if all == "" {
				return emptyResult()
			}
			allLines = splitNonEmpty(all)
			totalLines = len(allLines)
			if totalLines == 0 {
				return emptyResult()
			}

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
				"page_start":  start + 1,
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
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
