package tmux

import (
	"context"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"

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
// The attach process is long-running so we use background context,
// not the caller's context which may have a timeout.
func (k *KeepAlive) Add(_ context.Context, sessionName string) error {
	k.mu.Lock()
	if _, exists := k.clients[sessionName]; exists {
		k.mu.Unlock()
		return nil
	}
	k.mu.Unlock()

	cmd := exec.Command("tmux", "-S", k.socket, "attach-session", "-t", sessionName)
	// Clear TMUX* env vars to avoid nested-tmux detection causing immediate exit.
	var env []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "TMUX") {
			continue
		}
		env = append(env, e)
	}
	cmd.Env = env

	ptmx, err := pty.Start(cmd)
	if err != nil {
		log.Printf("[keepalive] pty.Start %s error: %v", sessionName, err)
		return err
	}

	log.Printf("[keepalive] added PID=%d for session %s", cmd.Process.Pid, sessionName)

	k.mu.Lock()
	k.clients[sessionName] = &keepAliveEntry{ptmx: ptmx, cmd: cmd}
	k.mu.Unlock()

	// When the attach process exits, remove the stale map entry so the next
	// poll cycle can re-attach instead of seeing a live entry and skipping.
	go func() {
		if err := cmd.Wait(); err != nil {
			log.Printf("[keepalive] session %s PID %d exited: %v", sessionName, cmd.Process.Pid, err)
		}
		k.mu.Lock()
		if entry, ok := k.clients[sessionName]; ok && entry.cmd == cmd {
			delete(k.clients, sessionName)
		}
		k.mu.Unlock()
	}()

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
