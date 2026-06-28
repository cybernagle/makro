package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// ArtifactService discovers and serves artifact files (HTML, video) generated
// by AI agents in their session working directories.
//
// Artifacts are scanned from a fixed set of conventional subdirectories under
// each session's cwd (dist, output, artifacts, public, build) to avoid
// surfacing noise like node_modules. Only .html and .mp4/.webm are collected.
type ArtifactService struct{}

// ArtifactEntry is one discovered artifact, returned to the client.
type ArtifactEntry struct {
	Name  string `json:"name"`  // basename
	Path  string `json:"path"`  // path relative to the session cwd
	Type  string `json:"type"`  // "html" or "video"
	Mtime int64  `json:"mtime"` // unix seconds
	Size  int64  `json:"size"`  // bytes
}

// artifactScanDirs are the conventional output directories scanned under a
// session cwd. Kept narrow to avoid node_modules / .git noise.
var artifactScanDirs = []string{"dist", "output", "artifacts", "public", "build"}

// artifactExtType maps a file extension to its artifact type, or "" if the
// extension is not a recognized artifact.
func artifactExtType(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".html", ".htm":
		return "html"
	case ".mp4", ".webm", ".mov":
		return "video"
	}
	return ""
}

// sessionCwd resolves a tmux session name to its active pane's working directory
// via tmux display-message. Works for any agent (Claude Code, Copilot, ZCode)
// since it reads tmux state, not agent-specific records. Returns "" if tmux
// can't resolve the session (not running, no such session).
func sessionCwd(sessionName string) string {
	out, err := exec.Command(getTmuxBin(), tmuxArgs(
		"display-message", "-t", sessionName, "-p", "#{pane_current_path}",
	)...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ListArtifacts scans the conventional subdirectories of a session's cwd and
// returns all recognized artifacts, newest first. Returns an empty slice (not
// nil) if the cwd can't be resolved or no artifacts exist.
func (s *ArtifactService) ListArtifacts(sessionName string) ([]ArtifactEntry, error) {
	cwd := sessionCwd(sessionName)
	if cwd == "" {
		return []ArtifactEntry{}, fmt.Errorf("could not resolve working directory for session %q", sessionName)
	}

	var entries []ArtifactEntry
	for _, sub := range artifactScanDirs {
		dir := filepath.Join(cwd, sub)
		// Skip non-existent scan dirs quietly — most projects won't have all of them.
		fi, err := os.Stat(dir)
		if err != nil || !fi.IsDir() {
			continue
		}
		_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			artType := artifactExtType(d.Name())
			if artType == "" {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			rel, err := filepath.Rel(cwd, path)
			if err != nil {
				return nil
			}
			entries = append(entries, ArtifactEntry{
				Name:  d.Name(),
				Path:  filepath.ToSlash(rel),
				Type:  artType,
				Mtime: info.ModTime().Unix(),
				Size:  info.Size(),
			})
			return nil
		})
	}

	// Newest first.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Mtime > entries[j].Mtime
	})
	return entries, nil
}

// ResolveArtifact validates `relPath` against the session cwd and returns the
// absolute filesystem path of the artifact, plus its type. It rejects any path
// that escapes the cwd (.., absolute paths, symlink escapes). This is the only
// path that reaches the filesystem for serving, so traversal safety lives here.
func (s *ArtifactService) ResolveArtifact(sessionName, relPath string) (absPath, artType string, err error) {
	cwd := sessionCwd(sessionName)
	if cwd == "" {
		return "", "", fmt.Errorf("could not resolve working directory for session %q", sessionName)
	}
	if relPath == "" {
		return "", "", fmt.Errorf("path is required")
	}

	// Join against cwd and clean. filepath.Join already resolves ".." lexically,
	// but a leading "/" in relPath would make Join treat it as absolute — strip
	// any leading separator to keep it relative.
	relPath = strings.TrimLeft(relPath, "/")
	joined := filepath.Join(cwd, relPath)
	cleaned := filepath.Clean(joined)

	// Resolve symlinks so a link can't escape cwd.
	resolved, evalErr := filepath.EvalSymlinks(cleaned)
	if evalErr != nil {
		return "", "", fmt.Errorf("artifact not found")
	}

	if !isWithinCwd(resolved, cwd) {
		return "", "", fmt.Errorf("path escapes session working directory")
	}

	fi, err := os.Stat(resolved)
	if err != nil || fi.IsDir() {
		return "", "", fmt.Errorf("artifact not found")
	}

	t := artifactExtType(resolved)
	if t == "" {
		return "", "", fmt.Errorf("unsupported artifact type")
	}
	return resolved, t, nil
}

// isWithinDir checks that target is within dir (both must be clean absolute
// paths). Mirrors internal/agent/tools/resolve_path.go's isWithinDir: a relative
// path from dir→target starting with ".." means target escaped dir.
func isWithinCwd(target, dir string) bool {
	target = filepath.Clean(target)
	dir = filepath.Clean(dir)
	rel, err := filepath.Rel(dir, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !strings.HasPrefix(rel, "../")
}
