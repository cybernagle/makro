package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

var stateMu sync.Mutex

func stateFilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return filepath.Join(home, ".fingersaver", "state.json"), nil
}

func readStateMap(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]string), nil
		}
		return nil, fmt.Errorf("read state: %w", err)
	}
	m := make(map[string]string)
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse state: %w", err)
	}
	return m, nil
}

func writeStateMap(path string, m map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

func NewSetStateTool() Tool {
	return Tool{
		Name:        "set_state",
		Description: "Persist a key-value pair to ~/.fingersaver/state.json",
		Parameters: []Param{
			{Name: "key", Type: "string", Description: "State key", Required: true},
			{Name: "value", Type: "string", Description: "State value", Required: true},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			key, _ := args["key"].(string)
			value, _ := args["value"].(string)
			if key == "" {
				return "", fmt.Errorf("key is required")
			}

			stateMu.Lock()
			defer stateMu.Unlock()

			path, err := stateFilePath()
			if err != nil {
				return "", err
			}

			m, err := readStateMap(path)
			if err != nil {
				return "", err
			}
			m[key] = value
			if err := writeStateMap(path, m); err != nil {
				return "", err
			}

			result, _ := json.Marshal(map[string]string{"key": key, "value": value})
			return string(result), nil
		},
	}
}

func NewGetStateTool() Tool {
	return Tool{
		Name:        "get_state",
		Description: "Read a key from ~/.fingersaver/state.json",
		Parameters: []Param{
			{Name: "key", Type: "string", Description: "State key", Required: true},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			key, _ := args["key"].(string)
			if key == "" {
				return "", fmt.Errorf("key is required")
			}

			stateMu.Lock()
			defer stateMu.Unlock()

			path, err := stateFilePath()
			if err != nil {
				return "", err
			}

			m, err := readStateMap(path)
			if err != nil {
				return "", err
			}

			value, exists := m[key]
			if !exists {
				value = ""
			}
			result, _ := json.Marshal(map[string]any{"key": key, "value": value})
			return string(result), nil
		},
	}
}
