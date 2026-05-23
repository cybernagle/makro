package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

type HistoryMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatHistory struct {
	mu   sync.Mutex
	path string
	file *os.File
}

func NewChatHistory(path string) (*ChatHistory, error) {
	if err := os.MkdirAll(strings.TrimSuffix(path, "/chat.md"), 0o755); err != nil {
		// path might not end with /chat.md — try parent dir
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

	timestamp := time.Now().Format("2006-01-02 15:04:05")
	var header string
	switch role {
	case "user":
		header = fmt.Sprintf("### [%s] User", timestamp)
	case "assistant":
		header = fmt.Sprintf("### [%s] Assistant", timestamp)
	default:
		header = fmt.Sprintf("### [%s] System", timestamp)
	}

	_, err := fmt.Fprintf(ch.file, "\n%s\n\n%s\n", header, content)
	if err != nil {
		return err
	}
	return ch.file.Sync()
}

func (ch *ChatHistory) Load() ([]HistoryMessage, error) {
	ch.mu.Lock()
	defer ch.mu.Unlock()

	if _, err := ch.file.Seek(0, 0); err != nil {
		return nil, err
	}

	var messages []HistoryMessage
	scanner := bufio.NewScanner(ch.file)
	var currentRole string
	var currentContent strings.Builder

	for scanner.Scan() {
		line := scanner.Text()
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

	return messages, scanner.Err()
}

func (ch *ChatHistory) Close() error {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	return ch.file.Close()
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
