package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func stateTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	return dir
}

func TestSetAndGetState(t *testing.T) {
	stateTestDir(t)

	setTool := NewSetStateTool()
	getTool := NewGetStateTool()

	result, err := setTool.Execute(context.Background(), map[string]any{
		"key":   "phase",
		"value": "review",
	})
	require.NoError(t, err)
	assert.Contains(t, result, `"key":"phase"`)
	assert.Contains(t, result, `"value":"review"`)

	result, err = getTool.Execute(context.Background(), map[string]any{
		"key": "phase",
	})
	require.NoError(t, err)
	assert.Contains(t, result, `"key":"phase"`)
	assert.Contains(t, result, `"value":"review"`)
}

func TestGetStateMissingKey(t *testing.T) {
	stateTestDir(t)

	getTool := NewGetStateTool()
	result, err := getTool.Execute(context.Background(), map[string]any{
		"key": "nonexistent",
	})
	require.NoError(t, err)
	assert.Contains(t, result, `"key":"nonexistent"`)
	assert.Contains(t, result, `"value":""`)
}

func TestSetStateMissingKey(t *testing.T) {
	stateTestDir(t)
	tool := NewSetStateTool()
	_, err := tool.Execute(context.Background(), map[string]any{
		"value": "x",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "key is required")
}

func TestGetStateMissingKeyArg(t *testing.T) {
	stateTestDir(t)
	tool := NewGetStateTool()
	_, err := tool.Execute(context.Background(), map[string]any{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "key is required")
}

func TestSetStateMerges(t *testing.T) {
	dir := stateTestDir(t)

	setTool := NewSetStateTool()
	setTool.Execute(context.Background(), map[string]any{"key": "a", "value": "1"})
	setTool.Execute(context.Background(), map[string]any{"key": "b", "value": "2"})

	data, err := os.ReadFile(filepath.Join(dir, ".makro", "state.json"))
	require.NoError(t, err)

	var m map[string]string
	require.NoError(t, json.Unmarshal(data, &m))
	assert.Equal(t, "1", m["a"])
	assert.Equal(t, "2", m["b"])
}

func TestSetStateOverwrite(t *testing.T) {
	stateTestDir(t)

	setTool := NewSetStateTool()
	setTool.Execute(context.Background(), map[string]any{"key": "phase", "value": "review"})
	setTool.Execute(context.Background(), map[string]any{"key": "phase", "value": "fix"})

	getTool := NewGetStateTool()
	result, err := getTool.Execute(context.Background(), map[string]any{"key": "phase"})
	require.NoError(t, err)
	assert.Contains(t, result, `"value":"fix"`)
}
