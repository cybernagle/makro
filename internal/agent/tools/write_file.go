package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

func NewWriteFileTool(cwd string) Tool {
	return Tool{
		Name:        "write_file",
		Description: "Write content to a file. Creates parent directories automatically. Creates or overwrites the file.",
		Parameters: []Param{
			{Name: "path", Type: "string", Description: "Path to the file (relative or absolute)", Required: true},
			{Name: "content", Type: "string", Description: "Content to write to the file", Required: true},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			path, _ := args["path"].(string)
			content, _ := args["content"].(string)
			if path == "" {
				return "", fmt.Errorf("path is required")
			}

			absPath := resolvePath(path, cwd)

			info, err := os.Stat(absPath)
			if err == nil && info.IsDir() {
				return "", fmt.Errorf("path is a directory, not a file: %s", absPath)
			}

			dir := filepath.Dir(absPath)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return "", fmt.Errorf("create directory %s: %w", dir, err)
			}

			data := []byte(content)
			if err := os.WriteFile(absPath, data, 0o644); err != nil {
				return "", fmt.Errorf("write %s: %w", absPath, err)
			}

			return fmt.Sprintf("Wrote %d bytes to %s", len(data), absPath), nil
		},
	}
}
