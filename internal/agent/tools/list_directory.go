package tools

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
)

type dirEntry struct {
	name  string
	isDir bool
	size  int64
}

func NewListDirectoryTool(cwd string) Tool {
	return Tool{
		Name:        "list_directory",
		Description: "List files and directories at a path. Shows type (dir/file), size, and name. Defaults to current working directory.",
		Parameters: []Param{
			{Name: "path", Type: "string", Description: "Directory path (relative or absolute, defaults to cwd)"},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			path, _ := args["path"].(string)
			absPath := resolvePath(path, cwd)

			info, err := os.Stat(absPath)
			if err != nil {
				if os.IsNotExist(err) {
					return "", fmt.Errorf("directory not found: %s", absPath)
				}
				return "", fmt.Errorf("stat %s: %w", absPath, err)
			}
			if !info.IsDir() {
				return "", fmt.Errorf("path is a file, not a directory: %s", absPath)
			}

			entries, err := os.ReadDir(absPath)
			if err != nil {
				return "", fmt.Errorf("read directory %s: %w", absPath, err)
			}

			if len(entries) == 0 {
				return fmt.Sprintf("(empty directory: %s)", absPath), nil
			}

			var dirs, files []dirEntry
			for _, e := range entries {
				fi, err := e.Info()
				if err != nil {
					continue
				}
				entry := dirEntry{
					name:  e.Name(),
					isDir: e.IsDir(),
					size:  fi.Size(),
				}
				if e.IsDir() {
					dirs = append(dirs, entry)
				} else {
					files = append(files, entry)
				}
			}

			sort.Slice(dirs, func(i, j int) bool { return dirs[i].name < dirs[j].name })
			sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })

			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("Directory: %s\n", absPath))
			for _, d := range dirs {
				sb.WriteString(fmt.Sprintf("  [dir]  %s\n", d.name))
			}
			for _, f := range files {
				sb.WriteString(fmt.Sprintf("  [file] %6d %s\n", f.size, f.name))
			}
			sb.WriteString(fmt.Sprintf("\n%d directories, %d files", len(dirs), len(files)))

			return sb.String(), nil
		},
	}
}
