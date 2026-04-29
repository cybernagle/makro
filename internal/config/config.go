package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type Config struct {
	LLMProvider     string `json:"llm_provider"`
	LLMModel        string `json:"llm_model"`
	LLMAPIKey       string `json:"-"`
	TmuxSocketPath  string `json:"tmux_socket_path"`
	DataDir         string `json:"data_dir"`
	ChatHistoryPath string `json:"chat_history_path"`
	ClaudeDir       string `json:"claude_dir"`
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "/tmp"
}

func DefaultConfig() *Config {
	home := homeDir()
	dataDir := filepath.Join(home, ".fingersaver")
	return &Config{
		LLMProvider:     "anthropic",
		LLMModel:        "",
		TmuxSocketPath:  filepath.Join(dataDir, "tmux.sock"),
		DataDir:         dataDir,
		ChatHistoryPath: filepath.Join(dataDir, "chat.md"),
		ClaudeDir:       filepath.Join(home, ".claude"),
	}
}

func Load() (*Config, error) {
	cfg := DefaultConfig()

	// Apply env overrides first so DataDir is resolved before reading config file.
	cfg.applyEnvOverrides()

	configPath := filepath.Join(cfg.DataDir, "config.json")
	if data, err := os.ReadFile(configPath); err == nil {
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", configPath, err)
		}
		// Re-apply env overrides so they always win over file values.
		cfg.applyEnvOverrides()
	}

	cfg.loadClaudeDefaults()

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir %s: %w", cfg.DataDir, err)
	}

	return cfg, nil
}

func (c *Config) applyEnvOverrides() {
	if v := os.Getenv("FINGERSAVER_LLM_PROVIDER"); v != "" {
		c.LLMProvider = v
	}
	if v := os.Getenv("FINGERSAVER_LLM_MODEL"); v != "" {
		c.LLMModel = v
	}
	if v := os.Getenv("FINGERSAVER_DATA_DIR"); v != "" {
		c.DataDir = v
		c.TmuxSocketPath = filepath.Join(v, "tmux.sock")
		c.ChatHistoryPath = filepath.Join(v, "chat.md")
	}
	if v := os.Getenv("FINGERSAVER_TMUX_SOCKET"); v != "" {
		c.TmuxSocketPath = v
	}
	if v := os.Getenv("FINGERSAVER_CHAT_HISTORY"); v != "" {
		c.ChatHistoryPath = v
	}

	switch c.LLMProvider {
	case "anthropic":
		c.LLMAPIKey = os.Getenv("ANTHROPIC_API_KEY")
	case "openai":
		c.LLMAPIKey = os.Getenv("OPENAI_API_KEY")
	}
	if v := os.Getenv("FINGERSAVER_LLM_API_KEY"); v != "" {
		c.LLMAPIKey = v
	}
}

func (c *Config) loadClaudeDefaults() {
	if c.LLMModel != "" {
		return
	}
	claudeDir := c.ClaudeDir
	if claudeDir == "" {
		return
	}

	settingsPath := filepath.Join(claudeDir, "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return
	}

	var settings struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return
	}
	if settings.Model != "" {
		c.LLMModel = settings.Model
	}
}

func (c *Config) validate() error {
	if c.LLMProvider != "anthropic" && c.LLMProvider != "openai" {
		return fmt.Errorf("unsupported llm_provider: %s (must be anthropic or openai)", c.LLMProvider)
	}
	return nil
}

func (c *Config) Summary() string {
	var sb strings.Builder
	sb.WriteString("FingerSaver Configuration:\n")
	sb.WriteString(fmt.Sprintf("  Provider:    %s\n", c.LLMProvider))
	sb.WriteString(fmt.Sprintf("  Model:       %s\n", c.LLMModel))
	sb.WriteString(fmt.Sprintf("  API Key:     %s\n", keyHint(c.LLMAPIKey)))
	sb.WriteString(fmt.Sprintf("  Tmux Socket: %s\n", c.TmuxSocketPath))
	sb.WriteString(fmt.Sprintf("  Data Dir:    %s\n", c.DataDir))
	sb.WriteString(fmt.Sprintf("  Chat File:   %s\n", c.ChatHistoryPath))
	sb.WriteString(fmt.Sprintf("  Claude Dir:  %s\n", c.ClaudeDir))
	sb.WriteString(fmt.Sprintf("  OS:          %s/%s\n", runtime.GOOS, runtime.GOARCH))
	return sb.String()
}

func keyHint(key string) string {
	if key == "" {
		return "(not set)"
	}
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + strings.Repeat("*", len(key)-8) + key[len(key)-4:]
}
