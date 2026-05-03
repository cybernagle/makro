package skills

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadFromDir(t *testing.T) {
	dir := t.TempDir()

	// Create a valid skill
	reviewDir := filepath.Join(dir, "review")
	require.NoError(t, os.MkdirAll(reviewDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(reviewDir, "SKILL.md"), []byte(`---
name: review
description: Review a PR
---
Review PR #{{.Args}}.
`), 0o644))

	// Create a skill with no name (should be skipped)
	badDir := filepath.Join(dir, "bad")
	require.NoError(t, os.MkdirAll(badDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(badDir, "SKILL.md"), []byte("no frontmatter"), 0o644))

	// Create a directory without SKILL.md (should be skipped)
	emptyDir := filepath.Join(dir, "empty")
	require.NoError(t, os.MkdirAll(emptyDir, 0o755))

	result, err := LoadFromDir(dir)
	require.NoError(t, err)

	assert.Len(t, result, 1)
	assert.Contains(t, result, "review")
	assert.Equal(t, "Review a PR", result["review"].Description)
}

func TestLoadFromDirNonexistent(t *testing.T) {
	result, err := LoadFromDir("/nonexistent/path")
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestLoadAll(t *testing.T) {
	dir1 := t.TempDir()
	reviewDir := filepath.Join(dir1, "review")
	require.NoError(t, os.MkdirAll(reviewDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(reviewDir, "SKILL.md"), []byte(`---
name: review
description: User-level review
---
User review prompt.
`), 0o644))

	dir2 := t.TempDir()
	reviewDir2 := filepath.Join(dir2, "review")
	require.NoError(t, os.MkdirAll(reviewDir2, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(reviewDir2, "SKILL.md"), []byte(`---
name: review
description: Project-level review
---
Project review prompt.
`), 0o644))

	deployDir := filepath.Join(dir2, "deploy")
	require.NoError(t, os.MkdirAll(deployDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(deployDir, "SKILL.md"), []byte(`---
name: deploy
description: Deploy
---
Deploy prompt.
`), 0o644))

	result, err := LoadAll([]string{dir1, dir2})
	require.NoError(t, err)

	assert.Len(t, result, 2)
	// dir2 overrides dir1 for "review"
	assert.Equal(t, "Project-level review", result["review"].Description)
	assert.Contains(t, result, "deploy")
}
