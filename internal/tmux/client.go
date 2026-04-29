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
	notifs     chan Notification
	state      *StateMirror
	cancel     context.CancelFunc
	mu         sync.Mutex
	running    bool
}

func NewClient(socketPath string) *Client {
	return &Client{
		socketPath: socketPath,
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

	ctx, cancel := context.WithCancel(ctx)
	c.cancel = cancel

	// Start the dedicated tmux server.
	startCmd := exec.CommandContext(ctx, "tmux", "-S", c.socketPath, "start-server")
	if err := startCmd.Run(); err != nil {
		// Server may already be running, try to continue.
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

	killCmd := exec.Command("tmux", "-S", c.socketPath, "kill-server")
	killCmd.Run()
	os.Remove(c.socketPath)

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

	// Use "sh -c" to parse the command so quoted arguments work correctly.
	out, err := exec.CommandContext(ctx, "sh", "-c",
		fmt.Sprintf("tmux -S %s %s", c.socketPath, cmd)).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("tmux %s: %w (%s)", cmd, err, strings.TrimSpace(string(out)))
	}

	// Parse any notification-format output.
	c.parseAndSendOutput(out)

	return strings.TrimSpace(string(out)), nil
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
