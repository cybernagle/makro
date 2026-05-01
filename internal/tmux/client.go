package tmux

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// TmuxClient is the interface for interacting with tmux.
type TmuxClient interface {
	Start(ctx context.Context) error
	SendCommand(cmd string) error
	Notifications() <-chan Notification
	State() *StateMirror
	Stop() error
}

// Client manages a dedicated tmux server and communicates via CLI commands.
// Instead of tmux -CC (which requires an interactive terminal to stay alive),
// we use direct CLI commands and poll for output.
type Client struct {
	socketPath string
	owned      bool
	notifs     chan Notification
	state      *StateMirror
	cancel     context.CancelFunc
	mu         sync.Mutex
	running    bool
}

func NewClient(socketPath string, owned bool) *Client {
	return &Client{
		socketPath: socketPath,
		owned:      owned,
		notifs:     make(chan Notification, 256),
		state:      NewStateMirror(),
	}
}

func (c *Client) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.running {
		return fmt.Errorf("tmux client already running")
	}

	c.notifs = make(chan Notification, 256)

	ctx, cancel := context.WithCancel(ctx)
	c.cancel = cancel

	// Start the dedicated tmux server (only for owned servers).
	if c.owned {
		startCmd := exec.CommandContext(ctx, "tmux", "-S", c.socketPath, "start-server")
		if err := startCmd.Run(); err != nil {
			// Server may already be running, try to continue.
		}
	}

	c.running = true

	// Start polling for session state changes.
	go c.pollLoop(ctx)

	return nil
}

func (c *Client) SendCommand(cmd string) error {
	_, err := c.Exec(cmd)
	return err
}

func (c *Client) Notifications() <-chan Notification {
	return c.notifs
}

func (c *Client) State() *StateMirror {
	return c.state
}

func (c *Client) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.running {
		return nil
	}
	c.running = false

	if c.cancel != nil {
		c.cancel()
	}

	close(c.notifs)

	// Only kill owned servers; shared servers are left running.
	if c.owned {
		exec.Command("tmux", "-S", c.socketPath, "kill-server").Run()
		os.Remove(c.socketPath)
	}

	return nil
}

// Exec runs a tmux command and returns its output.
func (c *Client) Exec(cmd string) (string, error) {
	c.mu.Lock()
	running := c.running
	c.mu.Unlock()

	if !running {
		return "", fmt.Errorf("tmux client not running")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	args := []string{"-S", c.socketPath}
	args = append(args, parseTmuxArgs(cmd)...)
	out, err := exec.CommandContext(ctx, "tmux", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("tmux %s: %w (%s)", cmd, err, strings.TrimSpace(string(out)))
	}

	c.parseAndSendOutput(out)

	return strings.TrimSpace(string(out)), nil
}

// parseTmuxArgs splits a tmux command string into arguments,
// respecting single and double quotes.
func parseTmuxArgs(cmd string) []string {
	var args []string
	var current strings.Builder
	inSingle := false
	inDouble := false

	for i := 0; i < len(cmd); i++ {
		ch := cmd[i]
		switch {
		case inSingle:
			if ch == '\'' {
				inSingle = false
			} else {
				current.WriteByte(ch)
			}
		case inDouble:
			if ch == '"' {
				inDouble = false
			} else if ch == '\\' && i+1 < len(cmd) {
				i++
				current.WriteByte(cmd[i])
			} else {
				current.WriteByte(ch)
			}
		default:
			switch ch {
			case '\'':
				inSingle = true
			case '"':
				inDouble = true
			case ' ', '\t':
				if current.Len() > 0 {
					args = append(args, current.String())
					current.Reset()
				}
			default:
				current.WriteByte(ch)
			}
		}
	}
	if current.Len() > 0 {
		args = append(args, current.String())
	}
	return args
}

func (c *Client) parseAndSendOutput(data []byte) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		n, err := ParseNotification(line)
		if err != nil {
			continue
		}
		c.state.Apply(n)
		select {
		case c.notifs <- n:
		default:
		}
	}
}

func (c *Client) pollLoop(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	knownSessions := make(map[string]string) // name -> id

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.pollSessions(ctx, knownSessions)
		}
	}
}

func (c *Client) pollSessions(ctx context.Context, knownSessions map[string]string) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	args := []string{"-S", c.socketPath, "list-sessions", "-F", "#{session_id}:#{session_name}"}
	out, err := exec.CommandContext(ctx, "tmux", args...).Output()
	if err != nil {
		// No sessions or server not running.
		return
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	currentSessions := make(map[string]string)

	for _, line := range lines {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		id, name := parts[0], parts[1]
		currentSessions[name] = id

		if _, exists := knownSessions[name]; !exists {
			n := Notification{
				Type:        NotifSessionChanged,
				SessionID:   id,
				SessionName: name,
			}
			c.state.Apply(n)
			select {
			case c.notifs <- n:
			default:
			}
		}
	}

	// Check for removed sessions.
	for name, id := range knownSessions {
		if _, exists := currentSessions[name]; !exists {
			c.state.RemoveSession(id)
		}
	}

	// Update known sessions.
	for name := range knownSessions {
		delete(knownSessions, name)
	}
	for name, id := range currentSessions {
		knownSessions[name] = id
	}
}
