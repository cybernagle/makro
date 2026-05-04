package tools

import (
	"context"
	"fmt"
	"os"
	"strings"
)

const (
	defaultReadLimit    = 2000
	defaultReadMaxBytes = 50 * 1024 // 50KB
)

func NewReadFileTool(cwd string) Tool {
	return Tool{
		Name:        "read_file",
		Description: "Read file contents. Returns line-numbered text with optional offset/limit pagination.",
		Parameters: []Param{
			{Name: "path", Type: "string", Description: "Path to the file (relative or absolute)", Required: true},
			{Name: "offset", Type: "number", Description: "Line number to start reading from (1-indexed)"},
			{Name: "limit", Type: "number", Description: "Maximum number of lines to read (default 2000)"},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			path, _ := args["path"].(string)
			if path == "" {
				return "", fmt.Errorf("path is required")
			}

			absPath, err := resolvePath(path, cwd)
			if err != nil {
				return "", err
			}

			// Validate offset early, before any early returns.
			offset := 0
			if v, ok := args["offset"].(float64); ok && v > 0 {
				offset = int(v) - 1 // convert 1-indexed to 0-indexed
			}
			if offset < 0 {
				offset = 0
			}

			info, err := os.Stat(absPath)
			if err != nil {
				if os.IsNotExist(err) {
					return "", fmt.Errorf("file not found: %s", absPath)
				}
				return "", fmt.Errorf("stat %s: %w", absPath, err)
			}
			if info.IsDir() {
				return "", fmt.Errorf("path is a directory, not a file: %s", absPath)
			}
			if info.Size() == 0 {
				if offset > 0 {
					return "", fmt.Errorf("offset %d exceeds total lines 0", offset+1)
				}
				return "(empty file)", nil
			}

			data, err := os.ReadFile(absPath)
			if err != nil {
				return "", fmt.Errorf("read %s: %w", absPath, err)
			}

			limit := defaultReadLimit
			if v, ok := args["limit"].(float64); ok && v > 0 {
				limit = int(v)
			}

			lines := strings.Split(string(data), "\n")
			// Remove trailing empty line from split if file ends with newline.
			if len(lines) > 0 && lines[len(lines)-1] == "" {
				lines = lines[:len(lines)-1]
			}

			totalLines := len(lines)

			if offset >= totalLines {
				return "", fmt.Errorf("offset %d exceeds total lines %d", offset+1, totalLines)
			}

			end := offset + limit
			if end > totalLines {
				end = totalLines
			}

			page := lines[offset:end]

			// Check byte limit.
			var sb strings.Builder
			truncated := false
			totalBytes := 0
			for i, line := range page {
				numbered := fmt.Sprintf("%6d\t%s\n", offset+i+1, line)
				totalBytes += len(numbered)
				if totalBytes > defaultReadMaxBytes {
					if sb.Len() > 0 {
						sb.WriteString(fmt.Sprintf("\n<result truncated at %d bytes, use offset=%d to continue>", defaultReadMaxBytes, offset+i+1))
					}
					truncated = true
					break
				}
				sb.WriteString(numbered)
			}

			if !truncated && end < totalLines {
				sb.WriteString(fmt.Sprintf("\n<result truncated at %d lines, use offset=%d to continue>", limit, end+1))
			}

			result := sb.String()
			if result == "" {
				return "(empty range)", nil
			}
			return result, nil
		},
	}
}
