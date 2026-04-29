package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChatHistoryRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chat.md")

	ch, err := NewChatHistory(path)
	require.NoError(t, err)
	defer ch.Close()

	// Write messages.
	require.NoError(t, ch.Append("user", "hello there"))
	require.NoError(t, ch.Append("assistant", "hi! how can I help?"))
	require.NoError(t, ch.Append("system", "[list_sessions] session1\nsession2"))

	// Load them back.
	msgs, err := ch.Load()
	require.NoError(t, err)

	require.Len(t, msgs, 3)
	assert.Equal(t, "user", msgs[0].Role)
	assert.Equal(t, "hello there", msgs[0].Content)
	assert.Equal(t, "assistant", msgs[1].Role)
	assert.Equal(t, "hi! how can I help?", msgs[1].Content)
	assert.Equal(t, "system", msgs[2].Role)
	assert.Contains(t, msgs[2].Content, "session1")
}

func TestChatHistoryEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chat.md")

	ch, err := NewChatHistory(path)
	require.NoError(t, err)
	defer ch.Close()

	msgs, err := ch.Load()
	require.NoError(t, err)
	assert.Empty(t, msgs)
}

func TestChatHistoryCreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "chat.md")

	ch, err := NewChatHistory(path)
	// Should fail since parent dir doesn't exist.
	require.Error(t, err)
	require.Nil(t, ch)
}

func TestChatHistoryExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chat.md")

	// Pre-populate.
	content := "### [2026-01-01 00:00:00] User\n\nexisting message\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	ch, err := NewChatHistory(path)
	require.NoError(t, err)
	defer ch.Close()

	msgs, err := ch.Load()
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, "user", msgs[0].Role)
	assert.Equal(t, "existing message", msgs[0].Content)

	// Append and reload.
	require.NoError(t, ch.Append("assistant", "new response"))
	msgs, err = ch.Load()
	require.NoError(t, err)
	assert.Len(t, msgs, 2)
	assert.Equal(t, "new response", msgs[1].Content)
}

func TestParseRoleFromHeader(t *testing.T) {
	assert.Equal(t, "user", parseRoleFromHeader("### [2026-01-01 00:00:00] User"))
	assert.Equal(t, "assistant", parseRoleFromHeader("### [2026-01-01 00:00:00] Assistant"))
	assert.Equal(t, "system", parseRoleFromHeader("### [2026-01-01 00:00:00] System"))
	assert.Equal(t, "system", parseRoleFromHeader("bad header"))
}
