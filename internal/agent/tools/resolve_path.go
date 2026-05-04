package tools

import (
	"fmt"
	"path/filepath"
	"strings"
)

// resolvePath resolves a path and ensures it stays within cwd.
// Rejects absolute paths and paths that escape the working directory.
func resolvePath(path, cwd string) (string, error) {
	if path == "" {
		return cwd, nil
	}

	// Reject absolute paths.
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("absolute paths not allowed: %s", path)
	}

	// Expand ~ is not allowed — it resolves outside cwd.
	if strings.HasPrefix(path, "~") {
		return "", fmt.Errorf("~ paths not allowed: %s", path)
	}

	// Resolve relative to cwd.
	absPath := filepath.Join(cwd, path)
	absPath = filepath.Clean(absPath)

	// Verify the resolved path is still within cwd.
	if !isWithinDir(absPath, cwd) {
		return "", fmt.Errorf("path escapes working directory: %s", path)
	}

	return absPath, nil
}

// isWithinDir checks that target is within dir (both must be clean absolute paths).
func isWithinDir(target, dir string) bool {
	rel, err := filepath.Rel(dir, target)
	if err != nil {
		return false
	}
	return !strings.HasPrefix(rel, "..") && rel != ".."
}
