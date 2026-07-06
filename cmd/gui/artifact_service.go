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
// (the central store is flat per session). Session tags which session owns it
// (always set, so the "all sessions" view can group by session).
type ArtifactEntry struct {
	Session string `json:"session"`
	Name    string `json:"name"`
	Path    string `json:"path"`
	Type    string `json:"type"` // "html" or "video"
	Mtime   int64  `json:"mtime"`
	Size    int64  `json:"size"`
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
	return filepath.Join(centralArtifactsRoot(), session)
}

// centralArtifactsRoot is the parent of all per-session stores:
// ~/.makro/artifacts/. Each subdir is one session's store.
func centralArtifactsRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".makro", "artifacts")
}

// validArtifactSession rejects session names that could escape the central
// store via path traversal. A real tmux session name can't contain "." or ":"
// anyway, and certainly not "/" or "\" — so this only refuses names that
// centralArtifactsDir would resolve outside ~/.makro/artifacts/<session>/.
// Without it, /api/artifacts?session=../../etc could list (and /api/artifact
// could serve) arbitrary html/video files outside the store.
func validArtifactSession(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.ContainsAny(name, `/\`) {
		return false
	}
	if strings.Contains(name, "..") {
		return false
	}
	return true
}

// ListArtifacts lists artifacts. If session is "", lists across ALL sessions
// (each entry tagged with its Session); otherwise lists just that session.
// Returns an empty slice (not nil) when the dir is absent/empty. Newest first.
func (s *ArtifactService) ListArtifacts(session string) ([]ArtifactEntry, error) {
	if session == "" {
		return s.listAllArtifacts()
	}
	if !validArtifactSession(session) {
		return nil, fmt.Errorf("invalid session name")
	}
	return s.listSessionArtifacts(session), nil
}

// listSessionArtifacts lists one session's central dir, tagging each entry
// with the session name. Empty slice if the dir is absent.
func (s *ArtifactService) listSessionArtifacts(session string) []ArtifactEntry {
	entries := []ArtifactEntry{}
	files, err := os.ReadDir(centralArtifactsDir(session))
	if err != nil {
		return entries
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
			Session: session, Name: f.Name(), Path: f.Name(), Type: t,
			Mtime: info.ModTime().Unix(), Size: info.Size(),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Mtime > entries[j].Mtime })
	return entries
}

// listAllArtifacts lists every session's central dir (each subdir of the root
// is one session), tagging each entry with its session. Newest first across
// all sessions.
func (s *ArtifactService) listAllArtifacts() ([]ArtifactEntry, error) {
	root := centralArtifactsRoot()
	sessions, err := os.ReadDir(root)
	if err != nil {
		return []ArtifactEntry{}, nil // no central store yet
	}
	entries := []ArtifactEntry{}
	for _, sess := range sessions {
		if !sess.IsDir() {
			continue
		}
		name := sess.Name()
		// Skip traversal-shaped / hidden dir names — they aren't real sessions.
		if !validArtifactSession(name) || strings.HasPrefix(name, ".") {
			continue
		}
		entries = append(entries, s.listSessionArtifacts(name)...)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Mtime > entries[j].Mtime })
	return entries, nil
}

// ResolveArtifact resolves relPath within the central store (flat: basenames)
// and returns its absolute path + type. Traversal-safe: rejects "..",
// absolute paths, symlink escapes.
func (s *ArtifactService) ResolveArtifact(session, relPath string) (absPath, artType string, err error) {
	if !validArtifactSession(session) {
		return "", "", fmt.Errorf("invalid session name")
	}
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
