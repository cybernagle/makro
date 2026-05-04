package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureStopHookAddsHook(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")

	initial := map[string]any{"env": map[string]string{}}
	data, _ := json.MarshalIndent(initial, "", "  ")
	require.NoError(t, os.WriteFile(settingsPath, data, 0o644))

	err := EnsureStopHook(dir)
	require.NoError(t, err)

	result, err := os.ReadFile(settingsPath)
	require.NoError(t, err)

	var settings claudeSettings
	require.NoError(t, json.Unmarshal(result, &settings))

	found := false
	for _, group := range settings.Hooks["Stop"] {
		for _, h := range group.Hooks {
			if h.Command == `fingersaver notify "$(tmux display-message -p '#{session_name}')" done` {
				found = true
			}
		}
	}
	assert.True(t, found, "should contain fingersaver notify hook")
}

func TestEnsureStopHookIdempotent(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")

	initial := map[string]any{"env": map[string]string{}}
	data, _ := json.MarshalIndent(initial, "", "  ")
	require.NoError(t, os.WriteFile(settingsPath, data, 0o644))

	require.NoError(t, EnsureStopHook(dir))
	require.NoError(t, EnsureStopHook(dir))

	result, err := os.ReadFile(settingsPath)
	require.NoError(t, err)

	var settings claudeSettings
	require.NoError(t, json.Unmarshal(result, &settings))

	count := 0
	for _, group := range settings.Hooks["Stop"] {
		for _, h := range group.Hooks {
			if h.Command == `fingersaver notify "$(tmux display-message -p '#{session_name}')" done` {
				count++
			}
		}
	}
	assert.Equal(t, 1, count, "should not add duplicate hooks")
}

func TestEnsureStopHookPreservesExisting(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")

	initial := map[string]any{
		"hooks": map[string][]hookGroup{
			"Stop": {{
				Hooks: []hookEntry{{Type: "command", Command: "echo existing", Timeout: 5}},
			}},
		},
	}
	data, _ := json.MarshalIndent(initial, "", "  ")
	require.NoError(t, os.WriteFile(settingsPath, data, 0o644))

	err := EnsureStopHook(dir)
	require.NoError(t, err)

	result, err := os.ReadFile(settingsPath)
	require.NoError(t, err)

	var settings claudeSettings
	require.NoError(t, json.Unmarshal(result, &settings))

	assert.Len(t, settings.Hooks["Stop"], 2, "should have existing + new hook")
	assert.Equal(t, "echo existing", settings.Hooks["Stop"][0].Hooks[0].Command)
}

func TestEnsureStopHookNoFile(t *testing.T) {
	dir := t.TempDir()
	err := EnsureStopHook(dir)
	assert.NoError(t, err)
}

func TestEnsureStopHookMalformedSettings(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	require.NoError(t, os.WriteFile(settingsPath, []byte("{not valid json"), 0o644))

	err := EnsureStopHook(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse settings")
}
