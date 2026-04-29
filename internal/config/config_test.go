package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	assert.Equal(t, "anthropic", cfg.LLMProvider)
	assert.NotEmpty(t, cfg.DataDir)
	assert.NotEmpty(t, cfg.TmuxSocketPath)
	assert.NotEmpty(t, cfg.ChatHistoryPath)
	assert.Contains(t, cfg.DataDir, ".fingersaver")
}

func TestLoadCreatesDataDir(t *testing.T) {
	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, ".fingersaver")

	t.Setenv("FINGERSAVER_DATA_DIR", dataDir)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, dataDir, cfg.DataDir)

	info, err := os.Stat(dataDir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestLoadFromConfigFile(t *testing.T) {
	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, ".fingersaver")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))

	configData := `{"llm_provider": "openai", "llm_model": "gpt-4o"}`
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "config.json"), []byte(configData), 0o644))

	t.Setenv("FINGERSAVER_DATA_DIR", dataDir)
	t.Setenv("FINGERSAVER_LLM_PROVIDER", "openai")
	t.Setenv("OPENAI_API_KEY", "test-key")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "openai", cfg.LLMProvider)
	assert.Equal(t, "gpt-4o", cfg.LLMModel)
}

func TestEnvOverrides(t *testing.T) {
	tmpDir := t.TempDir()

	t.Setenv("FINGERSAVER_DATA_DIR", tmpDir)
	t.Setenv("FINGERSAVER_LLM_PROVIDER", "openai")
	t.Setenv("FINGERSAVER_LLM_MODEL", "gpt-4o-mini")
	t.Setenv("OPENAI_API_KEY", "sk-test-1234")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "openai", cfg.LLMProvider)
	assert.Equal(t, "gpt-4o-mini", cfg.LLMModel)
	assert.Equal(t, "sk-test-1234", cfg.LLMAPIKey)
	assert.Equal(t, filepath.Join(tmpDir, "tmux.sock"), cfg.TmuxSocketPath)
	assert.Equal(t, filepath.Join(tmpDir, "chat.md"), cfg.ChatHistoryPath)
}

func TestClaudeDirFallback(t *testing.T) {
	tmpDir := t.TempDir()
	claudeDir := filepath.Join(tmpDir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o755))

	settingsData := `{"model": "claude-sonnet-4-6"}`
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(settingsData), 0o644))

	dataDir := filepath.Join(tmpDir, ".fingersaver")
	configData := `{"claude_dir": "` + claudeDir + `"}`
	require.NoError(t, os.MkdirAll(dataDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "config.json"), []byte(configData), 0o644))

	t.Setenv("FINGERSAVER_DATA_DIR", dataDir)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-4-6", cfg.LLMModel)
}

func TestInvalidProvider(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("FINGERSAVER_DATA_DIR", tmpDir)
	t.Setenv("FINGERSAVER_LLM_PROVIDER", "invalid")

	_, err := Load()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported llm_provider")
}

func TestKeyHint(t *testing.T) {
	assert.Equal(t, "(not set)", keyHint(""))
	assert.Equal(t, "****", keyHint("short"))
	assert.Equal(t, "sk-t********5678", keyHint("sk-test-12345678"))
}
