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

	// APNs (iOS push) — all optional; when key_path is unset, push is disabled.
	APNsKeyPath  string `json:"apns_key_path,omitempty"`
	APNsKeyID    string `json:"apns_key_id,omitempty"`
	APNsTeamID   string `json:"apns_team_id,omitempty"`
	APNsBundleID string `json:"apns_bundle_id,omitempty"`
	APNsSandbox  bool   `json:"apns_sandbox,omitempty"`

	// Bark (iOS push via Bark app — bypasses APNs signing). bark_key from Bark app.
	BarkKey string `json:"bark_key,omitempty"`
	BarkURL string `json:"bark_url,omitempty"`

	// Usage tracking. HighCostModels = model prefixes counted as high-tier
	// (prefix match); UsageQuota5h = window prompt quota (0 = unknown/hidden).
	HighCostModels []string `json:"high_cost_models,omitempty"`
	UsageQuota5h   int64    `json:"usage_quota_5h,omitempty"`

	// Brain (proactive second-brain). Brain.Enabled toggles capture + the
	// future brain daemon. The capture pipeline runs whenever Enabled is true,
	// even if the TUI/GUI is the only makro instance — capture is the shared
	// foundation both the reactive orchestrator and the proactive brain build on.
	Brain BrainConfig `json:"brain,omitempty"`
}

// BrainConfig configures the proactive brain. All knobs are advisory defaults;
// the self-tuning layer (P3) may adjust caps/threshold at runtime and persist
// the adjusted values back here.
type BrainConfig struct {
	Enabled bool `json:"enabled"`

	// Memory REST endpoint + auth. Endpoint must be the :8765 REST API
	// (RECONCILE §1), NOT :8090 (that's stop-hook.sh's transport). APIKey is the
	// Bearer token; the placeholder "mk-car-agent-abc123" MUST be replaced at
	// deploy time once memory-cli's :8765 auth lands (M3).
	MemoryEndpoint string `json:"memory_endpoint,omitempty"`
	MemoryAPIKey   string `json:"memory_api_key,omitempty"`
	MemoryCLIPath  string `json:"memory_cli_path,omitempty"` // ~/bin/memory fallback

	// Proactive push limits (§7). Caps gate how many proposals reach the inbox.
	DailyProposalCap    int     `json:"daily_proposal_cap,omitempty"`
	WeeklyProposalCap   int     `json:"weekly_proposal_cap,omitempty"`
	ConfidenceThreshold float64 `json:"confidence_threshold,omitempty"`
	CronTime            string  `json:"cron_time,omitempty"` // "HH:MM" daily wake

	// Capture control. When false the capture pipeline is a no-op (useful to
	// A/B whether capture slows chat down).
	CaptureEnabled bool `json:"capture_enabled,omitempty"`
}

// DefaultBrainConfig returns sensible defaults. Enabled defaults to true so a
// fresh install captures from day one (the brain itself is inert until P1).
func DefaultBrainConfig() BrainConfig {
	return BrainConfig{
		Enabled:             true,
		CaptureEnabled:      true,
		MemoryEndpoint:      "http://127.0.0.1:8765",
		MemoryAPIKey:        "mk-car-agent-abc123", // placeholder — replace at deploy
		MemoryCLIPath:       "~/bin/memory",
		DailyProposalCap:    2,
		WeeklyProposalCap:   8,
		ConfidenceThreshold: 0.6,
		CronTime:            "08:00",
	}
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
		Brain:              DefaultBrainConfig(),
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

	cfg.normalizeBrain()

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

	// APNs (iOS push) overrides.
	if v := os.Getenv("MAKRO_APNS_KEY_PATH"); v != "" {
		c.APNsKeyPath = v
	}
	if v := os.Getenv("MAKRO_APNS_KEY_ID"); v != "" {
		c.APNsKeyID = v
	}
	if v := os.Getenv("MAKRO_APNS_TEAM_ID"); v != "" {
		c.APNsTeamID = v
	}
	if v := os.Getenv("MAKRO_APNS_BUNDLE_ID"); v != "" {
		c.APNsBundleID = v
	}
	if v := os.Getenv("MAKRO_APNS_SANDBOX"); v != "" {
		c.APNsSandbox = v == "true" || v == "1"
	}
	if v := os.Getenv("MAKRO_BARK_KEY"); v != "" {
		c.BarkKey = v
	}
	if v := os.Getenv("MAKRO_BARK_URL"); v != "" {
		c.BarkURL = v
	}

	// Usage tracking overrides.
	if v := os.Getenv("MAKRO_HIGH_COST_MODELS"); v != "" {
		c.HighCostModels = strings.Split(v, ",")
	}
	if v := os.Getenv("MAKRO_QUOTA_5H"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			c.UsageQuota5h = n
		}
	}

	// Brain overrides.
	if v := os.Getenv("MAKRO_BRAIN_ENABLED"); v != "" {
		c.Brain.Enabled = v == "true" || v == "1"
	}
	if v := os.Getenv("MAKRO_BRAIN_CAPTURE_ENABLED"); v != "" {
		c.Brain.CaptureEnabled = v == "true" || v == "1"
	}
	if v := os.Getenv("MAKRO_BRAIN_MEMORY_ENDPOINT"); v != "" {
		c.Brain.MemoryEndpoint = v
	}
	if v := os.Getenv("MAKRO_BRAIN_MEMORY_API_KEY"); v != "" {
		c.Brain.MemoryAPIKey = v
	}
	if v := os.Getenv("MAKRO_BRAIN_MEMORY_CLI"); v != "" {
		c.Brain.MemoryCLIPath = v
	}
	if v := os.Getenv("MAKRO_BRAIN_DAILY_CAP"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Brain.DailyProposalCap = n
		}
	}
	if v := os.Getenv("MAKRO_BRAIN_WEEKLY_CAP"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Brain.WeeklyProposalCap = n
		}
	}
	if v := os.Getenv("MAKRO_BRAIN_CONFIDENCE"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			c.Brain.ConfidenceThreshold = f
		}
	}
	if v := os.Getenv("MAKRO_BRAIN_CRON"); v != "" {
		c.Brain.CronTime = v
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

// normalizeBrain fills zero-value Brain fields from defaults. A partial "brain"
// object in config.json would otherwise wipe the defaults we set in
// DefaultConfig (json.Unmarshal populates only present keys, but nested struct
// fields not in the JSON stay zero). Merge so an omitted knob keeps its default.
func (c *Config) normalizeBrain() {
	def := DefaultBrainConfig()
	if c.Brain.MemoryEndpoint == "" {
		c.Brain.MemoryEndpoint = def.MemoryEndpoint
	}
	if c.Brain.MemoryAPIKey == "" {
		c.Brain.MemoryAPIKey = def.MemoryAPIKey
	}
	if c.Brain.MemoryCLIPath == "" {
		c.Brain.MemoryCLIPath = def.MemoryCLIPath
	}
	if c.Brain.DailyProposalCap == 0 {
		c.Brain.DailyProposalCap = def.DailyProposalCap
	}
	if c.Brain.WeeklyProposalCap == 0 {
		c.Brain.WeeklyProposalCap = def.WeeklyProposalCap
	}
	if c.Brain.ConfidenceThreshold == 0 {
		c.Brain.ConfidenceThreshold = def.ConfidenceThreshold
	}
	if c.Brain.CronTime == "" {
		c.Brain.CronTime = def.CronTime
	}
	// Note: Enabled and CaptureEnabled default to false from struct literals,
	// NOT DefaultConfig (which sets them true). To honor "enabled by default on
	// fresh install", a *missing* brain key must mean true — but an *explicit*
	// `{"brain":{"enabled":false}}` must mean false. We can't distinguish those
	// two after unmarshal. Resolution: DefaultConfig sets them true, and
	// normalizeBrain does NOT touch them (explicit zero stays zero). The only
	// edge case: a config.json with `"brain": {}` (present but empty) disables.
	// That's acceptable and documented — opt-out is explicit.
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
