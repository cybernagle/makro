package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	fsNotifyHookSuffix      = ` notify "$(tmux display-message -p '#{session_name}')" done`
	fsStartHookSuffix       = ` notify "$(tmux display-message -p '#{session_name}')" start`
	fsPermissionHookSuffix  = ` permission "$(tmux display-message -p '#{session_name}')"`
	fsClaudeStartHookSuffix = ` claude-start "$(tmux display-message -p '#{session_name}')"`
	// fsCaptureHookSuffix pipes the UserPromptSubmit stdin (the user's prompt
	// JSON) into `makro capture <session>`. The hook fires ONLY on user
	// submits — agent replies never trigger it, so this is the precise split
	// point between user-side (captured) and agent-side (ignored) messages
	// (BRAIN_DESIGN §3.2).
	fsCaptureHookSuffix = ` capture "$(tmux display-message -p '#{session_name}')"`
)

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

// EnsureStopHook adds a makro notify Stop hook to Claude Code settings
// if one does not already exist. The function is idempotent.
func EnsureStopHook(claudeDir, executablePath string) error {
	settingsPath := filepath.Join(claudeDir, "settings.json")
	info, err := os.Stat(settingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat settings: %w", err)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return fmt.Errorf("read settings: %w", err)
	}

	var settings any
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("parse settings: %w", err)
	}
	root, ok := settings.(map[string]any)
	if !ok {
		return fmt.Errorf("parse settings: root must be an object")
	}

	hooks, err := ensureJSONObject(root, "hooks")
	if err != nil {
		return fmt.Errorf("parse settings: %w", err)
	}
	stopHooks, err := ensureJSONArray(hooks, "Stop")
	if err != nil {
		return fmt.Errorf("parse settings: %w", err)
	}

	exists, err := stopHookExists(stopHooks)
	if err != nil {
		return fmt.Errorf("parse settings: %w", err)
	}
	if exists {
		return nil
	}

	stopHooks = append(stopHooks, map[string]any{
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": buildStopHookCommand(executablePath),
			"timeout": 10,
		}},
	})
	hooks["Stop"] = stopHooks

	updated, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	updated = append(updated, '\n')
	return os.WriteFile(settingsPath, updated, info.Mode().Perm())
}

// EnsureStartHook adds a makro notify UserPromptSubmit hook to Claude Code
// settings if one does not already exist. The function is idempotent.
func EnsureStartHook(claudeDir, executablePath string) error {
	settingsPath := filepath.Join(claudeDir, "settings.json")
	info, err := os.Stat(settingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat settings: %w", err)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return fmt.Errorf("read settings: %w", err)
	}

	var settings any
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("parse settings: %w", err)
	}
	root, ok := settings.(map[string]any)
	if !ok {
		return fmt.Errorf("parse settings: root must be an object")
	}

	hooks, err := ensureJSONObject(root, "hooks")
	if err != nil {
		return fmt.Errorf("parse settings: %w", err)
	}
	startHooks, err := ensureJSONArray(hooks, "UserPromptSubmit")
	if err != nil {
		return fmt.Errorf("parse settings: %w", err)
	}

	exists, err := startHookExists(startHooks)
	if err != nil {
		return fmt.Errorf("parse settings: %w", err)
	}
	if exists {
		return nil
	}

	startHooks = append(startHooks, map[string]any{
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": buildStartHookCommand(executablePath),
			"timeout": 10,
		}},
	})
	hooks["UserPromptSubmit"] = startHooks

	updated, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	updated = append(updated, '\n')
	return os.WriteFile(settingsPath, updated, info.Mode().Perm())
}

// EnsureUserPromptCaptureHook adds a makro capture UserPromptSubmit hook to
// Claude Code settings if one does not already exist. This is the brain's
// capture entry point: every user prompt is piped (via `makro capture`) to the
// running makro, which writes it to memory-cli as a category=capture record.
// Idempotent. Coexists with any existing UserPromptSubmit hooks (appended as a
// new group, never removes the existing `notify ... start` entry).
func EnsureUserPromptCaptureHook(claudeDir, executablePath string) error {
	settingsPath := filepath.Join(claudeDir, "settings.json")
	info, err := os.Stat(settingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat settings: %w", err)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return fmt.Errorf("read settings: %w", err)
	}

	var settings any
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("parse settings: %w", err)
	}
	root, ok := settings.(map[string]any)
	if !ok {
		return fmt.Errorf("parse settings: root must be an object")
	}

	hooks, err := ensureJSONObject(root, "hooks")
	if err != nil {
		return fmt.Errorf("parse settings: %w", err)
	}
	submitHooks, err := ensureJSONArray(hooks, "UserPromptSubmit")
	if err != nil {
		return fmt.Errorf("parse settings: %w", err)
	}

	if captureHookExists(submitHooks) {
		return nil
	}

	submitHooks = append(submitHooks, map[string]any{
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": buildCaptureHookCommand(executablePath),
			"timeout": 10,
		}},
	})
	hooks["UserPromptSubmit"] = submitHooks

	updated, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	updated = append(updated, '\n')
	return os.WriteFile(settingsPath, updated, info.Mode().Perm())
}

// EnsureClaudeStartHook adds a makro claude-start SessionStart hook to Claude
// Code settings if one does not already exist. The hook forwards the Claude
// Code session_id + transcript_path (read from stdin) and the tmux session name
// to the running Makro so its transcript ingester can attribute usage. Idempotent.
func EnsureClaudeStartHook(claudeDir, executablePath string) error {
	settingsPath := filepath.Join(claudeDir, "settings.json")
	info, err := os.Stat(settingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat settings: %w", err)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return fmt.Errorf("read settings: %w", err)
	}

	var settings any
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("parse settings: %w", err)
	}
	root, ok := settings.(map[string]any)
	if !ok {
		return fmt.Errorf("parse settings: root must be an object")
	}

	hooks, err := ensureJSONObject(root, "hooks")
	if err != nil {
		return fmt.Errorf("parse settings: %w", err)
	}
	ccHooks, err := ensureJSONArray(hooks, "SessionStart")
	if err != nil {
		return fmt.Errorf("parse settings: %w", err)
	}

	exists, err := claudeStartHookExists(ccHooks)
	if err != nil {
		return fmt.Errorf("parse settings: %w", err)
	}
	if exists {
		return nil
	}

	ccHooks = append(ccHooks, map[string]any{
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": buildClaudeStartHookCommand(executablePath),
			"timeout": 10,
		}},
	})
	hooks["SessionStart"] = ccHooks

	updated, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	updated = append(updated, '\n')
	return os.WriteFile(settingsPath, updated, info.Mode().Perm())
}

// EnsurePermissionHook adds a makro permission PermissionRequest hook to
// Claude Code settings if one does not already exist. The function is idempotent.
func EnsurePermissionHook(claudeDir, executablePath string) error {
	settingsPath := filepath.Join(claudeDir, "settings.json")
	info, err := os.Stat(settingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat settings: %w", err)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return fmt.Errorf("read settings: %w", err)
	}

	var settings any
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("parse settings: %w", err)
	}
	root, ok := settings.(map[string]any)
	if !ok {
		return fmt.Errorf("parse settings: root must be an object")
	}

	hooksObj, err := ensureJSONObject(root, "hooks")
	if err != nil {
		return fmt.Errorf("parse settings: %w", err)
	}
	permHooks, err := ensureJSONArray(hooksObj, "PermissionRequest")
	if err != nil {
		return fmt.Errorf("parse settings: %w", err)
	}

	if permissionHookExists(permHooks) {
		return nil
	}

	permHooks = append(permHooks, map[string]any{
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": buildPermissionHookCommand(executablePath),
			"timeout": 10,
		}},
	})
	hooksObj["PermissionRequest"] = permHooks

	updated, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	updated = append(updated, '\n')
	return os.WriteFile(settingsPath, updated, info.Mode().Perm())
}

func ensureJSONObject(parent map[string]any, key string) (map[string]any, error) {
	if value, ok := parent[key]; ok {
		object, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s must be an object", key)
		}
		return object, nil
	}
	object := make(map[string]any)
	parent[key] = object
	return object, nil
}

func ensureJSONArray(parent map[string]any, key string) ([]any, error) {
	if value, ok := parent[key]; ok {
		array, ok := value.([]any)
		if !ok {
			return nil, fmt.Errorf("%s must be an array", key)
		}
		return array, nil
	}
	return []any{}, nil
}

func stopHookExists(stopGroups []any) (bool, error) {
	for _, groupValue := range stopGroups {
		group, ok := groupValue.(map[string]any)
		if !ok {
			return false, fmt.Errorf("hooks.Stop entries must be objects")
		}
		hookValues, ok := group["hooks"]
		if !ok {
			continue
		}
		hooks, ok := hookValues.([]any)
		if !ok {
			return false, fmt.Errorf("hooks.Stop[].hooks must be an array")
		}
		for _, hookValue := range hooks {
			hook, ok := hookValue.(map[string]any)
			if !ok {
				return false, fmt.Errorf("hooks.Stop[].hooks[] must be objects")
			}
			command, _ := hook["command"].(string)
			if isMakroNotifyHook(command) {
				return true, nil
			}
		}
	}
	return false, nil
}

func startHookExists(startGroups []any) (bool, error) {
	for _, groupValue := range startGroups {
		group, ok := groupValue.(map[string]any)
		if !ok {
			return false, fmt.Errorf("hooks.UserPromptSubmit entries must be objects")
		}
		hookValues, ok := group["hooks"]
		if !ok {
			continue
		}
		hooks, ok := hookValues.([]any)
		if !ok {
			return false, fmt.Errorf("hooks.UserPromptSubmit[].hooks must be an array")
		}
		for _, hookValue := range hooks {
			hook, ok := hookValue.(map[string]any)
			if !ok {
				return false, fmt.Errorf("hooks.UserPromptSubmit[].hooks[] must be objects")
			}
			command, _ := hook["command"].(string)
			if isMakroStartHook(command) {
				return true, nil
			}
		}
	}
	return false, nil
}

func permissionHookExists(permGroups []any) bool {
	for _, groupValue := range permGroups {
		group, ok := groupValue.(map[string]any)
		if !ok {
			continue
		}
		hookValues, ok := group["hooks"]
		if !ok {
			continue
		}
		hookEntries, ok := hookValues.([]any)
		if !ok {
			continue
		}
		for _, hookValue := range hookEntries {
			hook, ok := hookValue.(map[string]any)
			if !ok {
				continue
			}
			command, _ := hook["command"].(string)
			if isMakroPermissionHook(command) {
				return true
			}
		}
	}
	return false
}

func isMakroNotifyHook(command string) bool {
	return strings.Contains(command, "makro") && strings.Contains(command, fsNotifyHookSuffix)
}

func isMakroPermissionHook(command string) bool {
	return strings.Contains(command, "makro") && strings.Contains(command, fsPermissionHookSuffix)
}

func isMakroStartHook(command string) bool {
	return strings.Contains(command, "makro") && strings.Contains(command, fsStartHookSuffix)
}

// captureHookExists reports whether any UserPromptSubmit group already contains
// a makro capture hook. Used for idempotent injection.
func captureHookExists(submitGroups []any) bool {
	for _, groupValue := range submitGroups {
		group, ok := groupValue.(map[string]any)
		if !ok {
			continue
		}
		hookValues, ok := group["hooks"]
		if !ok {
			continue
		}
		hooks, ok := hookValues.([]any)
		if !ok {
			continue
		}
		for _, hookValue := range hooks {
			hook, ok := hookValue.(map[string]any)
			if !ok {
				continue
			}
			command, _ := hook["command"].(string)
			if isMakroCaptureHook(command) {
				return true
			}
		}
	}
	return false
}

func isMakroCaptureHook(command string) bool {
	return strings.Contains(command, "makro") && strings.Contains(command, fsCaptureHookSuffix)
}

func buildCaptureHookCommand(executablePath string) string {
	return shellQuote(executablePath) + fsCaptureHookSuffix
}

func claudeStartHookExists(groups []any) (bool, error) {
	for _, groupValue := range groups {
		group, ok := groupValue.(map[string]any)
		if !ok {
			return false, fmt.Errorf("hooks.SessionStart entries must be objects")
		}
		hookValues, ok := group["hooks"]
		if !ok {
			continue
		}
		hooks, ok := hookValues.([]any)
		if !ok {
			return false, fmt.Errorf("hooks.SessionStart[].hooks must be an array")
		}
		for _, hookValue := range hooks {
			hook, ok := hookValue.(map[string]any)
			if !ok {
				return false, fmt.Errorf("hooks.SessionStart[].hooks[] must be objects")
			}
			command, _ := hook["command"].(string)
			if isMakroClaudeStartHook(command) {
				return true, nil
			}
		}
	}
	return false, nil
}

func isMakroClaudeStartHook(command string) bool {
	return strings.Contains(command, "makro") && strings.Contains(command, fsClaudeStartHookSuffix)
}

func buildStopHookCommand(executablePath string) string {
	return shellQuote(executablePath) + fsNotifyHookSuffix
}

func buildStartHookCommand(executablePath string) string {
	return shellQuote(executablePath) + fsStartHookSuffix
}

func buildClaudeStartHookCommand(executablePath string) string {
	return shellQuote(executablePath) + fsClaudeStartHookSuffix
}

func buildPermissionHookCommand(executablePath string) string {
	return shellQuote(executablePath) + fsPermissionHookSuffix
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
