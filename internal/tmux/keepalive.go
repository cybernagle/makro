package tmux

import (
	"context"
	"log"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"github.com/creack/pty"
)

// KeepAlive manages hidden PTY clients that keep tmux sessions attached.
// Copilot's TUI only processes tmux send-keys when the session is attached,
// so we maintain a background PTY client per session.
type KeepAlive struct {
	mu      sync.Mutex
	clients map[string]*keepAliveEntry
	socket  string
}

type keepAliveEntry struct {
	ptmx *os.File
	cmd  *exec.Cmd
}

func NewKeepAlive(socketPath string) *KeepAlive {
	return &KeepAlive{
		clients: make(map[string]*keepAliveEntry),
		socket:  socketPath,
	}
}

// Add creates a hidden PTY client attached to the session.
// The PTY master fd is kept open so tmux sees the session as attached.
func (k *KeepAlive) Add(ctx context.Context, sessionName string) error {
	k.mu.Lock()
	if entry, exists := k.clients[sessionName]; exists {
		if isProcessAlive(entry.cmd) {
			k.mu.Unlock()
			return nil
		}
		// Process died; clean up stale entry.
		entry.ptmx.Close()
		delete(k.clients, sessionName)
	}
	k.mu.Unlock()

	cmd := exec.CommandContext(ctx, "tmux", "-S", k.socket, "attach-session", "-t", sessionName)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return err
	}

	k.mu.Lock()
	k.clients[sessionName] = &keepAliveEntry{ptmx: ptmx, cmd: cmd}
	k.mu.Unlock()

	log.Printf("[keepalive] added hidden client for session %s", sessionName)
	return nil
}

// Remove closes the hidden PTY client for a session.
func (k *KeepAlive) Remove(sessionName string) {
	k.mu.Lock()
	entry, exists := k.clients[sessionName]
	if !exists {
		k.mu.Unlock()
		return
	}
	delete(k.clients, sessionName)
	k.mu.Unlock()

	entry.ptmx.Close()
	if entry.cmd.Process != nil {
		entry.cmd.Process.Kill()
	}
	log.Printf("[keepalive] removed hidden client for session %s", sessionName)
}

// Close shuts down all hidden PTY clients.
func (k *KeepAlive) Close() {
	k.mu.Lock()
	entries := make([]*keepAliveEntry, 0, len(k.clients))
	for name, entry := range k.clients {
		entries = append(entries, entry)
		delete(k.clients, name)
	}
	k.mu.Unlock()

	for _, entry := range entries {
		entry.ptmx.Close()
		if entry.cmd.Process != nil {
			entry.cmd.Process.Kill()
		}
	}
	log.Printf("[keepalive] closed all hidden clients (%d)", len(entries))
}

func isProcessAlive(cmd *exec.Cmd) bool {
	if cmd.Process == nil {
		return false
	}
	return cmd.Process.Signal(syscall.Signal(0)) == nil
}
