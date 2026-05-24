package config

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	TmuxModeAuto      = "auto"
	TmuxModeDedicated = "dedicated"
	TmuxModeShared    = "shared"
)

type Config struct {
	LLMProvider        string `json:"llm_provider"`
	LLMModel           string `json:"llm_model"`
	LLMAPIKey          string `json:"-"`
	LLMBaseURL         string `json:"llm_base_url,omitempty"`
	TmuxMode           string `json:"tmux_mode"`
	TmuxSocketPath     string `json:"tmux_socket_path"`
	DataDir            string `json:"data_dir"`
	ChatHistoryPath    string `json:"chat_history_path"`
	ClaudeDir          string `json:"claude_dir"`
	GuardianPrompt     string `json:"guardian_prompt,omitempty"`
	MaxContextMessages int    `json:"max_context_messages"`
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "/tmp"
}

func DefaultConfig() *Config {
	home := homeDir()
	dataDir := filepath.Join(home, ".makro")
	return &Config{
		LLMProvider:        "",
		LLMModel:           "",
		TmuxMode:           TmuxModeAuto,
		TmuxSocketPath:     filepath.Join(dataDir, "tmux.sock"),
		DataDir:            dataDir,
		ChatHistoryPath:    filepath.Join(dataDir, "chat.md"),
		ClaudeDir:          filepath.Join(home, ".claude"),
		MaxContextMessages: 40,
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

	// BigModel uses OpenAI-compatible protocol.
	if strings.Contains(cfg.LLMBaseURL, "bigmodel") {
		cfg.LLMProvider = "openai"
		rewritten := strings.Replace(cfg.LLMBaseURL, "/api/anthropic", "/api/coding/paas/v4", 1)
		if rewritten != cfg.LLMBaseURL {
			log.Printf("[config] BigModel detected, switching to OpenAI protocol and rewriting base URL: %s", rewritten)
			cfg.LLMBaseURL = rewritten
		} else {
			log.Printf("[config] BigModel detected, switching to OpenAI protocol")
		}
	}

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir %s: %w", cfg.DataDir, err)
	}

	return cfg, nil
}

func (c *Config) applyEnvOverrides() {
	if v := os.Getenv("MAKRO_LLM_PROVIDER"); v != "" {
		c.LLMProvider = v
	}
	if v := os.Getenv("MAKRO_LLM_MODEL"); v != "" {
		c.LLMModel = v
	}
	if v := os.Getenv("MAKRO_TMUX_MODE"); v != "" {
		c.TmuxMode = v
	}
	if v := os.Getenv("MAKRO_DATA_DIR"); v != "" {
		c.DataDir = v
		c.TmuxSocketPath = filepath.Join(v, "tmux.sock")
		c.ChatHistoryPath = filepath.Join(v, "chat.md")
	}
	if v := os.Getenv("MAKRO_TMUX_SOCKET"); v != "" {
		c.TmuxSocketPath = v
	}
	if v := os.Getenv("MAKRO_CHAT_HISTORY"); v != "" {
		c.ChatHistoryPath = v
	}

	switch c.LLMProvider {
	case "anthropic":
		c.LLMAPIKey = os.Getenv("ANTHROPIC_API_KEY")
	case "openai":
		c.LLMAPIKey = os.Getenv("OPENAI_API_KEY")
	default:
		// Provider not set yet — try both and auto-detect.
		if v := os.Getenv("ANTHROPIC_API_KEY"); v != "" {
			c.LLMAPIKey = v
			c.LLMProvider = "anthropic"
		} else if v := os.Getenv("OPENAI_API_KEY"); v != "" {
			c.LLMAPIKey = v
			c.LLMProvider = "openai"
		}
	}
	if v := os.Getenv("MAKRO_LLM_API_KEY"); v != "" {
		c.LLMAPIKey = v
	}

	if v := os.Getenv("ANTHROPIC_BASE_URL"); v != "" {
		c.LLMBaseURL = v
	}
	if v := os.Getenv("OPENAI_BASE_URL"); v != "" {
		c.LLMBaseURL = v
	}
	if v := os.Getenv("MAKRO_LLM_BASE_URL"); v != "" {
		c.LLMBaseURL = v
	}
	if v := os.Getenv("MAKRO_MAX_CONTEXT_MESSAGES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			c.MaxContextMessages = n
		} else if err != nil {
			log.Printf("[config] warning: invalid MAKRO_MAX_CONTEXT_MESSAGES: %q, ignored", v)
		}
	}
}

func (c *Config) loadClaudeDefaults() {
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
		Model string            `json:"model"`
		Env   map[string]string `json:"env"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return
	}

	// Model from settings.json (if not already set).
	if c.LLMModel == "" && settings.Model != "" {
		c.LLMModel = settings.Model
	}

	// Auto-detect provider from env keys.
	if c.LLMAPIKey == "" {
		if v := settings.Env["ANTHROPIC_AUTH_TOKEN"]; v != "" {
			c.LLMAPIKey = v
			if c.LLMProvider == "" {
				c.LLMProvider = "anthropic"
			}
		}
	}
	if c.LLMAPIKey == "" {
		if v := settings.Env["ANTHROPIC_API_KEY"]; v != "" {
			c.LLMAPIKey = v
			if c.LLMProvider == "" {
				c.LLMProvider = "anthropic"
			}
		}
	}
	if c.LLMAPIKey == "" {
		if v := settings.Env["OPENAI_API_KEY"]; v != "" {
			c.LLMAPIKey = v
			if c.LLMProvider == "" {
				c.LLMProvider = "openai"
			}
		}
	}

	// Base URL for custom API endpoints.
	if c.LLMBaseURL == "" {
		if v := settings.Env["ANTHROPIC_BASE_URL"]; v != "" {
			c.LLMBaseURL = v
		}
	}
	if c.LLMBaseURL == "" {
		if v := settings.Env["OPENAI_BASE_URL"]; v != "" {
			c.LLMBaseURL = v
		}
	}

	// Model aliases from claude env.
	if c.LLMModel == "" {
		// Try sonnet model alias first, then default.
		for _, key := range []string{
			"ANTHROPIC_DEFAULT_SONNET_MODEL",
			"ANTHROPIC_DEFAULT_OPUS_MODEL",
			"ANTHROPIC_DEFAULT_HAIKU_MODEL",
		} {
			if v := settings.Env[key]; v != "" {
				c.LLMModel = v
				break
			}
		}
	}
}

func (c *Config) validate() error {
	if c.LLMProvider == "" {
		c.LLMProvider = "anthropic"
	}
	if c.LLMProvider != "anthropic" && c.LLMProvider != "openai" {
		return fmt.Errorf("unsupported llm_provider: %s (must be anthropic or openai)", c.LLMProvider)
	}
	switch c.TmuxMode {
	case TmuxModeAuto, TmuxModeDedicated, TmuxModeShared:
	default:
		return fmt.Errorf("unsupported tmux_mode: %s (must be auto, dedicated, or shared)", c.TmuxMode)
	}
	return nil
}

// ValidateAPIKey checks that an API key is configured. Call this before
// starting the LLM provider, not during config loading, so that --config
// can display the key status even when no key is set.
func (c *Config) ValidateAPIKey() error {
	if c.LLMAPIKey == "" {
		return fmt.Errorf("no API key found: set ANTHROPIC_API_KEY/OPENAI_API_KEY or configure %s", filepath.Join(c.ClaudeDir, "settings.json"))
	}
	return nil
}

func (c *Config) Save() error {
	configPath := filepath.Join(c.DataDir, "config.json")
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, data, 0o644)
}

func (c *Config) Summary() string {
	var sb strings.Builder
	sb.WriteString("Makro Configuration:\n")
	sb.WriteString(fmt.Sprintf("  Provider:    %s\n", c.LLMProvider))
	sb.WriteString(fmt.Sprintf("  Model:       %s\n", c.LLMModel))
	sb.WriteString(fmt.Sprintf("  API Key:     %s\n", keyHint(c.LLMAPIKey)))
	if c.LLMBaseURL != "" {
		sb.WriteString(fmt.Sprintf("  Base URL:    %s\n", c.LLMBaseURL))
	}
	sb.WriteString(fmt.Sprintf("  Tmux Socket: %s\n", c.TmuxSocketPath))
	sb.WriteString(fmt.Sprintf("  Tmux Mode:   %s\n", c.TmuxMode))
	sb.WriteString(fmt.Sprintf("  Max Context: %d messages\n", c.MaxContextMessages))
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
