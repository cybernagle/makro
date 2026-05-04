package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const fsNotifyHookCmd = "fingersaver notify"

// claudeSettings represents the relevant parts of ~/.claude/settings.json.
type claudeSettings struct {
	Env   map[string]string      `json:"env"`
	Hooks map[string][]hookGroup `json:"hooks"`
}

type hookGroup struct {
	Hooks []hookEntry `json:"hooks"`
}

type hookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

// EnsureStopHook adds a fingersaver notify Stop hook to Claude Code settings
// if one does not already exist. The function is idempotent.
func EnsureStopHook(claudeDir string) error {
	settingsPath := filepath.Join(claudeDir, "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read settings: %w", err)
	}

	var settings claudeSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("parse settings: %w", err)
	}

	if settings.Hooks == nil {
		settings.Hooks = make(map[string][]hookGroup)
	}

	// Check if fingersaver notify hook already exists.
	for _, group := range settings.Hooks["Stop"] {
		for _, h := range group.Hooks {
			if strings.Contains(h.Command, fsNotifyHookCmd) {
				return nil
			}
		}
	}

	// Add the hook.
	newEntry := hookGroup{
		Hooks: []hookEntry{{
			Type:    "command",
			Command: `fingersaver notify "$(tmux display-message -p '#{session_name}')" done`,
			Timeout: 10,
		}},
	}
	settings.Hooks["Stop"] = append(settings.Hooks["Stop"], newEntry)

	updated, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	return os.WriteFile(settingsPath, updated, 0o644)
}
