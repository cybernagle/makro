package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type HistoryMessage struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	Timestamp string `json:"timestamp,omitempty"`
}

// ChatHistory stores messages in a JSONL file (one JSON object per line).
// Live context = last N messages loaded on startup.
// Full history = all messages on disk, searchable on demand.
type ChatHistory struct {
	mu   sync.Mutex
	path string
	file *os.File
}

func NewChatHistory(path string) (*ChatHistory, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir chat history: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open chat history: %w", err)
	}
	return &ChatHistory{path: path, file: f}, nil
}

func (ch *ChatHistory) Append(role, content string) error {
	ch.mu.Lock()
	defer ch.mu.Unlock()

	msg := HistoryMessage{
		Role:      role,
		Content:   content,
		Timestamp: time.Now().Format(time.RFC3339),
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if _, err := ch.file.Write(data); err != nil {
		return err
	}
	return ch.file.Sync()
}

// Load returns the last N messages (live context).
func (ch *ChatHistory) Load(n int) ([]HistoryMessage, error) {
	ch.mu.Lock()
	defer ch.mu.Unlock()

	if _, err := ch.file.Seek(0, 0); err != nil {
		return nil, err
	}

	var all []HistoryMessage
	scanner := bufio.NewScanner(ch.file)
	// Allow longer lines (tool results can be large)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var msg HistoryMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		all = append(all, msg)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// Return last N
	if n > 0 && len(all) > n {
		all = all[len(all)-n:]
	}
	return all, nil
}

// LoadAll returns all messages (long-lived context, for search/export).
func (ch *ChatHistory) LoadAll() ([]HistoryMessage, error) {
	return ch.Load(0)
}

// MigrateFromMarkdown reads the old markdown format and converts to JSONL.
func MigrateFromMarkdown(mdPath, jsonlPath string) error {
	data, err := os.ReadFile(mdPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(data) == 0 {
		return nil
	}

	// Parse markdown format
	var messages []HistoryMessage
	lines := strings.Split(string(data), "\n")
	var currentRole string
	var currentContent strings.Builder

	for _, line := range lines {
		if strings.HasPrefix(line, "### [") {
			if currentRole != "" {
				content := strings.TrimSpace(currentContent.String())
				if content != "" {
					messages = append(messages, HistoryMessage{Role: currentRole, Content: content})
				}
			}
			currentRole = parseRoleFromHeader(line)
			currentContent.Reset()
		} else if currentRole != "" {
			currentContent.WriteString(line)
			currentContent.WriteString("\n")
		}
	}
	if currentRole != "" {
		content := strings.TrimSpace(currentContent.String())
		if content != "" {
			messages = append(messages, HistoryMessage{Role: currentRole, Content: content})
		}
	}

	if len(messages) == 0 {
		return nil
	}

	// Write as JSONL
	f, err := os.OpenFile(jsonlPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	for _, msg := range messages {
		data, _ := json.Marshal(msg)
		data = append(data, '\n')
		f.Write(data)
	}
	return nil
}

func parseRoleFromHeader(header string) string {
	idx := strings.Index(header, "] ")
	if idx < 0 {
		return "system"
	}
	role := strings.TrimSpace(header[idx+2:])
	switch strings.ToLower(role) {
	case "user":
		return "user"
	case "assistant":
		return "assistant"
	default:
		return "system"
	}
}
