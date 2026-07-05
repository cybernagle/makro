package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ArtifactService serves per-session artifacts from a CENTRAL store at
// ~/.makro/artifacts/<session>/. The coding agent (Claude Code is the
// executor) writes deliverables there directly, following the
// `makro-artifacts` skill that Makro installs into ~/.claude/skills/. Makro
// itself does NOT scan session cwds or copy files — it just lists/serves the
// central dir, so discovery is deterministic and doesn't depend on where an
// agent happened to write (which is what hid the `business` session's
// ~/artifact/ output in the scan-based model).
type ArtifactService struct{}

// ArtifactEntry is one artifact in the central store. Path is the basename
// (the central store is flat per session).
type ArtifactEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Type  string `json:"type"` // "html" or "video"
	Mtime int64  `json:"mtime"`
	Size  int64  `json:"size"`
}

func artifactExtType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".html", ".htm":
		return "html"
	case ".mp4", ".webm", ".mov":
		return "video"
	}
	return ""
}

// centralArtifactsDir is the per-session store, e.g. ~/.makro/artifacts/business.
func centralArtifactsDir(session string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".makro", "artifacts", session)
}

// ListArtifacts lists the central store for a session. Returns an empty slice
// (not nil) when the dir is absent/empty. Newest first.
func (s *ArtifactService) ListArtifacts(session string) ([]ArtifactEntry, error) {
	entries := []ArtifactEntry{}
	files, err := os.ReadDir(centralArtifactsDir(session))
	if err != nil {
		return entries, nil
	}
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		t := artifactExtType(f.Name())
		if t == "" {
			continue
		}
		info, err := f.Info()
		if err != nil {
			continue
		}
		entries = append(entries, ArtifactEntry{
			Name: f.Name(), Path: f.Name(), Type: t,
			Mtime: info.ModTime().Unix(), Size: info.Size(),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Mtime > entries[j].Mtime })
	return entries, nil
}

// ResolveArtifact resolves relPath within the central store (flat: basenames)
// and returns its absolute path + type. Traversal-safe: rejects "..",
// absolute paths, symlink escapes.
func (s *ArtifactService) ResolveArtifact(session, relPath string) (absPath, artType string, err error) {
	if relPath == "" {
		return "", "", fmt.Errorf("path is required")
	}
	dir := centralArtifactsDir(session)
	// EvalSymlinks on BOTH the dir and the target so a cwd under a symlinked
	// path (e.g. macOS /var → /private/var) doesn't make isWithinDir false-
	// negative on the resolved target.
	dirResolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", "", fmt.Errorf("artifact not found")
	}
	cleaned := filepath.Clean(filepath.Join(dir, strings.TrimLeft(relPath, "/")))
	resolved, evalErr := filepath.EvalSymlinks(cleaned)
	if evalErr != nil {
		return "", "", fmt.Errorf("artifact not found")
	}
	if !isWithinDir(resolved, dirResolved) {
		return "", "", fmt.Errorf("path escapes artifact store")
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

// isWithinDir checks that target is within dir (both clean absolute paths).
func isWithinDir(target, dir string) bool {
	target = filepath.Clean(target)
	dir = filepath.Clean(dir)
	rel, err := filepath.Rel(dir, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !strings.HasPrefix(rel, "../")
}
