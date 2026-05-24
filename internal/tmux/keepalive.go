package tmux

import (
	"io"
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
	closed  bool
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
// ctx is intentionally omitted — a timeout context would kill the
// long-running attach process (a previous bug).
func (k *KeepAlive) Add(sessionName string) error {
	k.mu.Lock()
	if k.closed {
		k.mu.Unlock()
		return nil
	}
	if _, exists := k.clients[sessionName]; exists {
		k.mu.Unlock()
		return nil
	}
	k.mu.Unlock()

	var args []string
	if k.socket != "" {
		args = []string{"-S", k.socket, "attach-session", "-t", sessionName}
	} else {
		args = []string{"attach-session", "-t", sessionName}
	}
	cmd := exec.Command("tmux", args...)
	// Clear TMUX* env vars to avoid nested-tmux detection causing immediate exit.
	var env []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "TMUX") {
			continue
		}
		env = append(env, e)
	}
	cmd.Env = env

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 50, Cols: 200})
	if err != nil {
		log.Printf("[keepalive] pty.Start %s error: %v", sessionName, err)
		return err
	}

	// Drain PTY output so tmux's full-screen renders don't fill the kernel
	// buffer and disconnect this client.
	go io.Copy(io.Discard, ptmx)

	log.Printf("[keepalive] added PID=%d for session %s", cmd.Process.Pid, sessionName)

	// Re-check closed flag after pty.Start (which is slow) to avoid leaking
	// a client into an already-shutting-down KeepAlive.
	k.mu.Lock()
	if k.closed {
		k.mu.Unlock()
		ptmx.Close()
		cmd.Process.Kill()
		return nil
	}
	k.clients[sessionName] = &keepAliveEntry{ptmx: ptmx, cmd: cmd}
	k.mu.Unlock()

	// When the attach process exits, clean up the map entry and close the fd.
	go func() {
		if err := cmd.Wait(); err != nil {
			log.Printf("[keepalive] session %s PID %d exited: %v", sessionName, cmd.Process.Pid, err)
		}
		k.mu.Lock()
		if entry, ok := k.clients[sessionName]; ok && entry.cmd == cmd {
			entry.ptmx.Close()
			delete(k.clients, sessionName)
		}
		k.mu.Unlock()
	}()

	return nil
}

// Remove closes the hidden PTY client for a session.
// Returns true if a client was actually removed.
func (k *KeepAlive) Remove(sessionName string) bool {
	k.mu.Lock()
	entry, exists := k.clients[sessionName]
	if !exists {
		k.mu.Unlock()
		return false
	}
	delete(k.clients, sessionName)
	k.mu.Unlock()

	entry.ptmx.Close()
	if entry.cmd.Process != nil {
		entry.cmd.Process.Kill()
	}
	log.Printf("[keepalive] removed hidden client for session %s", sessionName)
	return true
}

// Close shuts down all hidden PTY clients.
func (k *KeepAlive) Close() {
	k.mu.Lock()
	k.closed = true
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
