package tui

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// ChatHistory handles reading/writing chat messages as Markdown.
type ChatHistory struct {
	mu   sync.Mutex
	path string
	file *os.File
}

// NewChatHistory opens or creates the chat history file.
func NewChatHistory(path string) (*ChatHistory, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open chat history: %w", err)
	}
	return &ChatHistory{path: path, file: f}, nil
}

// Append writes a message to the history file.
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

// Load reads all messages from the history file.
func (ch *ChatHistory) Load() ([]ChatMessage, error) {
	ch.mu.Lock()
	defer ch.mu.Unlock()

	if _, err := ch.file.Seek(0, 0); err != nil {
		return nil, err
	}

	var messages []ChatMessage
	scanner := bufio.NewScanner(ch.file)
	var currentRole string
	var currentContent strings.Builder

	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "### [") {
			// Flush previous message.
			if currentRole != "" {
				content := strings.TrimSpace(currentContent.String())
				if content != "" {
					messages = append(messages, ChatMessage{Role: currentRole, Content: content})
				}
			}
			// Parse new header.
			currentRole = parseRoleFromHeader(line)
			currentContent.Reset()
		} else if currentRole != "" {
			currentContent.WriteString(line)
			currentContent.WriteString("\n")
		}
	}

	// Flush last message.
	if currentRole != "" {
		content := strings.TrimSpace(currentContent.String())
		if content != "" {
			messages = append(messages, ChatMessage{Role: currentRole, Content: content})
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
	// Header format: "### [2006-01-02 15:04:05] Role"
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
