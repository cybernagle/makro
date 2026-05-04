package tools

import (
	"os"
	"path/filepath"
	"strings"
)

// resolvePath expands ~ and resolves relative paths against cwd.
func resolvePath(path, cwd string) string {
	if path == "" {
		return cwd
	}
	if strings.HasPrefix(path, "~") {
		home, _ := os.UserHomeDir()
		if home != "" {
			path = strings.Replace(path, "~", home, 1)
		}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	return filepath.Clean(path)
}
