package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/naglezhang/fingersaver/internal/util"
)

func contextBaseDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return filepath.Join(home, ".fingersaver", "contexts"), nil
}

func NewSaveContextTool(tc TmuxClient) Tool {
	return Tool{
		Name:        "save_context",
		Description: "Save a session's structured output snapshot to disk for later restoration",
		Parameters: []Param{
			{Name: "name", Type: "string", Description: "Session name", Required: true},
			{Name: "label", Type: "string", Description: "Snapshot label (e.g. after_review, before_fix)"},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			name, _ := args["name"].(string)
			label, _ := args["label"].(string)
			if name == "" {
				return "", fmt.Errorf("name is required")
			}

			out, err := readStructured(tc, name)
			if err != nil {
				return "", err
			}

			baseDir, err := contextBaseDir()
			if err != nil {
				return "", err
			}
			sessionDir := filepath.Join(baseDir, name)
			if err := os.MkdirAll(sessionDir, 0o755); err != nil {
				return "", fmt.Errorf("create context dir: %w", err)
			}

			ts := time.Now().Format("2006-01-02_150405")
			filename := ts
			if label != "" {
				filename += "_" + label
			}
			filename += ".json"

			data, err := json.MarshalIndent(out, "", "  ")
			if err != nil {
				return "", fmt.Errorf("marshal context: %w", err)
			}

			snapshotPath := filepath.Join(sessionDir, filename)
			if err := os.WriteFile(snapshotPath, data, 0o644); err != nil {
				return "", fmt.Errorf("write snapshot: %w", err)
			}

			// Update latest.json.
			latestPath := filepath.Join(sessionDir, "latest.json")
			if err := os.WriteFile(latestPath, data, 0o644); err != nil {
				return "", fmt.Errorf("write latest: %w", err)
			}

			return fmt.Sprintf("Context saved: %s (%s, status=%s)", snapshotPath, ts, out.Status), nil
		},
	}
}

func NewRestoreContextTool(tc TmuxClient) Tool {
	return Tool{
		Name:        "restore_context",
		Description: "Restore a saved context snapshot and send it to a target session",
		Parameters: []Param{
			{Name: "name", Type: "string", Description: "Target session name", Required: true},
			{Name: "source_session", Type: "string", Description: "Session whose context to restore (defaults to name)"},
			{Name: "label", Type: "string", Description: "Snapshot label to restore (defaults to latest)"},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			name, _ := args["name"].(string)
			sourceSession, _ := args["source_session"].(string)
			label, _ := args["label"].(string)
			if name == "" {
				return "", fmt.Errorf("name is required")
			}
			if sourceSession == "" {
				sourceSession = name
			}

			baseDir, err := contextBaseDir()
			if err != nil {
				return "", err
			}
			sessionDir := filepath.Join(baseDir, sourceSession)

			snapshotFile := "latest.json"
			if label != "" && label != "latest" {
				// Find file matching label.
				entries, err := os.ReadDir(sessionDir)
				if err != nil {
					return "", fmt.Errorf("read context dir for %q: %w", sourceSession, err)
				}
				found := ""
				for _, e := range entries {
					if strings.Contains(e.Name(), "_"+label+".json") {
						found = e.Name()
						break
					}
				}
				if found == "" {
					return "", fmt.Errorf("no snapshot found with label %q for session %q", label, sourceSession)
				}
				snapshotFile = found
			}

			data, err := os.ReadFile(filepath.Join(sessionDir, snapshotFile))
			if err != nil {
				return "", fmt.Errorf("read snapshot: %w", err)
			}

			var out StructuredOutput
			if err := json.Unmarshal(data, &out); err != nil {
				return "", fmt.Errorf("parse snapshot: %w", err)
			}

			msg := formatRestoreMessage(sourceSession, snapshotFile, label, &out)
			if err := sendText(tc, name, msg); err != nil {
				return "", err
			}

			return fmt.Sprintf("Context restored from %s/%s to session %q", sourceSession, snapshotFile, name), nil
		},
	}
}

func formatRestoreMessage(source, filename, label string, out *StructuredOutput) string {
	var b strings.Builder
	b.WriteString("═══════════════════════════════════════════\n")
	b.WriteString(fmt.Sprintf("🔄 Context Restore\n"))
	b.WriteString(fmt.Sprintf("From: %s @ %s\n", source, strings.TrimSuffix(filename, ".json")))
	if label != "" {
		b.WriteString(fmt.Sprintf("Label: %s\n", label))
	}
	b.WriteString("═══════════════════════════════════════════\n\n")
	b.WriteString(fmt.Sprintf("Previous Status: %s\n", out.Status))
	if out.LastAssistantMessage != "" {
		b.WriteString(fmt.Sprintf("Last Action: %s\n", util.Truncate(out.LastAssistantMessage, 500)))
	}
	if len(out.FilesModified) > 0 {
		b.WriteString(fmt.Sprintf("Files Modified: %s\n", strings.Join(out.FilesModified, ", ")))
	}
	if len(out.Errors) > 0 {
		b.WriteString(fmt.Sprintf("Errors Encountered: %s\n", strings.Join(out.Errors, "; ")))
	}
	b.WriteString("\nResumed Message:\n")
	// Include last chunk of raw output as context.
	raw := strings.TrimSpace(out.RawOutput)
	if raw != "" {
		b.WriteString(util.Truncate(raw, 1000))
	}
	b.WriteString("\n═══════════════════════════════════════════")
	return b.String()
}
