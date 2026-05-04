package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolvePath resolves a path and ensures it stays within cwd.
// Rejects absolute paths, ~ expansion, path traversal, and symlink escapes.
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

	// Lexical traversal check — catches ../.. without filesystem access.
	if !isWithinDir(absPath, cwd) {
		return "", fmt.Errorf("path escapes working directory: %s", path)
	}

	// Symlink escape check — only when cwd exists on disk.
	if realCwd, err := filepath.EvalSymlinks(cwd); err == nil {
		realPath := evalSymlinksSafe(absPath)
		if !isWithinDir(realPath, realCwd) {
			return "", fmt.Errorf("path escapes working directory: %s", path)
		}
	}

	return absPath, nil
}

// evalSymlinksSafe resolves symlinks for the deepest existing ancestor.
// If the full path exists, resolves it. Otherwise walks up to find an
// existing prefix, resolves that, and re-appends the remainder.
func evalSymlinksSafe(path string) string {
	// Try full path first.
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	// Walk up to find an existing ancestor.
	dir := filepath.Dir(path)
	for dir != "" && dir != "/" {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			suffix := strings.TrimPrefix(path, dir)
			return filepath.Clean(resolved + suffix)
		}
		dir = filepath.Dir(dir)
	}
	// Fallback: nothing resolved, return lexical path.
	return path
}

// isWithinDir checks that target is within dir (both must be clean absolute paths).
func isWithinDir(target, dir string) bool {
	// Ensure both are cleaned.
	target = filepath.Clean(target)
	dir = filepath.Clean(dir)
	rel, err := filepath.Rel(dir, target)
	if err != nil {
		return false
	}
	return !strings.HasPrefix(rel, "..") && rel != ".."
}

// ensure resolve_path.go compiles — os import for future use.
var _ = os.PathSeparator
