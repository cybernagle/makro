package role

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "roles.toml")
	require.NoError(t, os.WriteFile(path, []byte(`
[[role]]
name = "makro"
description = "Makro dev"
session = "makro"

[[role]]
name = "reviewer"
description = "Cross-project review"
session = "reviewer"
clear_after = "manual"
state_file = "reviewer/{project}.md"

[[role]]
name = "research"
description = "General research"
`), 0o644))

	roles, err := LoadFile(path)
	require.NoError(t, err)
	require.Len(t, roles, 3)

	assert.Equal(t, "makro", roles[0].Name)
	assert.Equal(t, ClearAfterManual, roles[0].ClearAfter, "default applied")

	assert.Equal(t, "reviewer", roles[1].Name)
	assert.Equal(t, "reviewer/{project}.md", roles[1].StateFile)

	assert.Equal(t, "research", roles[2].Name)
	assert.Equal(t, "", roles[2].Session, "empty session = create on demand")
}

func TestLoadFileMissing(t *testing.T) {
	roles, err := LoadFile("/nonexistent/roles.toml")
	require.NoError(t, err, "missing file = empty list, not error")
	assert.Empty(t, roles)
}

func TestLoadFileDuplicateName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "roles.toml")
	require.NoError(t, os.WriteFile(path, []byte(`
[[role]]
name = "dup"
description = "first"
[[role]]
name = "dup"
description = "second"
`), 0o644))

	_, err := LoadFile(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate role name")
}

func TestLoadFileUnknownKeyTolerated(t *testing.T) {
	// Forward-compat: a future roles.toml with a key Phase 1 doesn't know
	// (e.g. call_count) must NOT error.
	dir := t.TempDir()
	path := filepath.Join(dir, "roles.toml")
	require.NoError(t, os.WriteFile(path, []byte(`
[[role]]
name = "makro"
description = "x"
future_field = "anything"
metadata_score = 7
`), 0o644))

	roles, err := LoadFile(path)
	require.NoError(t, err, "unknown keys must be tolerated")
	require.Len(t, roles, 1)
	assert.Equal(t, "makro", roles[0].Name)
}

func TestLoadFileInvalidRole(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "roles.toml")
	require.NoError(t, os.WriteFile(path, []byte(`
[[role]]
description = "no name"
`), 0o644))

	_, err := LoadFile(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "role name is required")
}
