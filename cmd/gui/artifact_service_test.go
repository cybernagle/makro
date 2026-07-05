package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withTempHome points HOME at a temp dir so centralArtifactsDir resolves there
// instead of the real ~/.makro. os.UserHomeDir honors $HOME on darwin/linux.
func withTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	return dir
}

// TestListArtifactsEmpty: an absent central dir yields an empty slice (not nil)
// so clients render "no artifacts" cleanly.
func TestListArtifactsEmpty(t *testing.T) {
	withTempHome(t)
	svc := &ArtifactService{}
	entries, err := svc.ListArtifacts("business")
	require.NoError(t, err)
	assert.Equal(t, []ArtifactEntry{}, entries)
}

// TestListArtifactsFromCentral: only recognized types (.html/.mp4/…) are listed;
// other files are filtered out.
func TestListArtifactsFromCentral(t *testing.T) {
	withTempHome(t)
	dir := centralArtifactsDir("business")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.html"), []byte("<h1>a</h1>"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.txt"), []byte("noise"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "c.mp4"), []byte("video"), 0o644))

	svc := &ArtifactService{}
	entries, err := svc.ListArtifacts("business")
	require.NoError(t, err)
	require.Len(t, entries, 2, ".txt must be filtered out")
	names := []string{entries[0].Name, entries[1].Name}
	assert.Contains(t, names, "a.html")
	assert.Contains(t, names, "c.mp4")
}

// TestResolveArtifactCentralServe: a central artifact resolves to an absolute
// path with the right type.
func TestResolveArtifactCentralServe(t *testing.T) {
	withTempHome(t)
	dir := centralArtifactsDir("dev")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "report.html"), []byte("<h1>ok</h1>"), 0o644))

	svc := &ArtifactService{}
	abs, artType, err := svc.ResolveArtifact("dev", "report.html")
	require.NoError(t, err)
	assert.Equal(t, "html", artType)
	assert.True(t, filepath.IsAbs(abs))
}

// TestResolveArtifactRejectsTraversal: a ".." path that would escape the
// central store is refused.
func TestResolveArtifactRejectsTraversal(t *testing.T) {
	withTempHome(t)
	dir := centralArtifactsDir("dev")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "report.html"), []byte("x"), 0o644))

	svc := &ArtifactService{}
	_, _, err := svc.ResolveArtifact("dev", "../../../etc/passwd")
	require.Error(t, err)
}

// TestEnsureCCSkillsIdempotent: writes the skill once, leaves an existing file
// untouched on a second call (user edits win).
func TestEnsureCCSkillsIdempotent(t *testing.T) {
	home := withTempHome(t)
	target := filepath.Join(home, ".claude", "skills", "makro-artifacts", "SKILL.md")

	ensureCCSkills()
	data, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Contains(t, string(data), "makro-artifacts")
	assert.Contains(t, string(data), "~/.makro/artifacts/")

	// User edits the skill; a second ensure must NOT overwrite.
	require.NoError(t, os.WriteFile(target, []byte("USER EDIT"), 0o644))
	ensureCCSkills()
	data, err = os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "USER EDIT", string(data), "existing skill must not be clobbered")
}
