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
	execPath := filepath.Join(dir, "bin", "makro")

	initial := map[string]any{"env": map[string]string{}}
	data, _ := json.MarshalIndent(initial, "", "  ")
	require.NoError(t, os.WriteFile(settingsPath, data, 0o644))

	err := EnsureStopHook(dir, execPath)
	require.NoError(t, err)

	result, err := os.ReadFile(settingsPath)
	require.NoError(t, err)

	var settings claudeSettings
	require.NoError(t, json.Unmarshal(result, &settings))

	found := false
	for _, group := range settings.Hooks["Stop"] {
		for _, h := range group.Hooks {
			if h.Command == buildStopHookCommand(execPath) {
				found = true
			}
		}
	}
	assert.True(t, found, "should contain makro notify hook")
}

func TestEnsureStopHookIdempotent(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	execPath := filepath.Join(dir, "Finger Saver", "makro")

	initial := map[string]any{"env": map[string]string{}}
	data, _ := json.MarshalIndent(initial, "", "  ")
	require.NoError(t, os.WriteFile(settingsPath, data, 0o644))

	require.NoError(t, EnsureStopHook(dir, execPath))
	require.NoError(t, EnsureStopHook(dir, execPath))

	result, err := os.ReadFile(settingsPath)
	require.NoError(t, err)

	var settings claudeSettings
	require.NoError(t, json.Unmarshal(result, &settings))

	count := 0
	for _, group := range settings.Hooks["Stop"] {
		for _, h := range group.Hooks {
			if h.Command == buildStopHookCommand(execPath) {
				count++
			}
		}
	}
	assert.Equal(t, 1, count, "should not add duplicate hooks")
}

func TestEnsureStopHookPreservesExisting(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	execPath := filepath.Join(dir, "bin", "makro")

	initial := map[string]any{
		"model": "claude-3-7-sonnet",
		"hooks": map[string][]hookGroup{
			"Stop": {{
				Hooks: []hookEntry{{Type: "command", Command: "echo existing", Timeout: 5}},
			}},
		},
	}
	data, _ := json.MarshalIndent(initial, "", "  ")
	require.NoError(t, os.WriteFile(settingsPath, data, 0o644))

	err := EnsureStopHook(dir, execPath)
	require.NoError(t, err)

	result, err := os.ReadFile(settingsPath)
	require.NoError(t, err)

	var settings map[string]any
	require.NoError(t, json.Unmarshal(result, &settings))

	hooks := settings["hooks"].(map[string]any)
	stopHooks := hooks["Stop"].([]any)
	assert.Len(t, stopHooks, 2, "should have existing + new hook")
	firstGroup := stopHooks[0].(map[string]any)
	firstHooks := firstGroup["hooks"].([]any)
	assert.Equal(t, "echo existing", firstHooks[0].(map[string]any)["command"])
	assert.Equal(t, "claude-3-7-sonnet", settings["model"])
}

func TestEnsureStopHookNoFile(t *testing.T) {
	dir := t.TempDir()
	err := EnsureStopHook(dir, filepath.Join(dir, "bin", "makro"))
	assert.NoError(t, err)
}

func TestEnsureStopHookMalformedSettings(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	require.NoError(t, os.WriteFile(settingsPath, []byte("{not valid json"), 0o644))

	err := EnsureStopHook(dir, filepath.Join(dir, "bin", "makro"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse settings")
}

func TestEnsureStopHookPreservesFileMode(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	initial := map[string]any{"env": map[string]string{}}
	data, _ := json.MarshalIndent(initial, "", "  ")
	require.NoError(t, os.WriteFile(settingsPath, data, 0o600))

	require.NoError(t, EnsureStopHook(dir, filepath.Join(dir, "bin", "makro")))

	info, err := os.Stat(settingsPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestEnsurePermissionHookAddsHook(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	execPath := filepath.Join(dir, "bin", "makro")

	initial := map[string]any{"env": map[string]string{}}
	data, _ := json.MarshalIndent(initial, "", "  ")
	require.NoError(t, os.WriteFile(settingsPath, data, 0o644))

	err := EnsurePermissionHook(dir, execPath)
	require.NoError(t, err)

	result, err := os.ReadFile(settingsPath)
	require.NoError(t, err)

	var settings claudeSettings
	require.NoError(t, json.Unmarshal(result, &settings))

	found := false
	for _, group := range settings.Hooks["PermissionRequest"] {
		for _, h := range group.Hooks {
			if h.Command == buildPermissionHookCommand(execPath) {
				found = true
			}
		}
	}
	assert.True(t, found, "should contain makro permission hook")
}

func TestEnsurePermissionHookIdempotent(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	execPath := filepath.Join(dir, "bin", "makro")

	initial := map[string]any{"env": map[string]string{}}
	data, _ := json.MarshalIndent(initial, "", "  ")
	require.NoError(t, os.WriteFile(settingsPath, data, 0o644))

	require.NoError(t, EnsurePermissionHook(dir, execPath))
	require.NoError(t, EnsurePermissionHook(dir, execPath))

	result, err := os.ReadFile(settingsPath)
	require.NoError(t, err)

	var settings claudeSettings
	require.NoError(t, json.Unmarshal(result, &settings))

	count := 0
	for _, group := range settings.Hooks["PermissionRequest"] {
		for _, h := range group.Hooks {
			if h.Command == buildPermissionHookCommand(execPath) {
				count++
			}
		}
	}
	assert.Equal(t, 1, count, "should not add duplicate permission hooks")
}
