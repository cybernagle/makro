package agent

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// hookPayload is the JSON message sent to the socket.
type hookPayload struct {
	Session string `json:"session"`
	Status  string `json:"status"`
}

// AgentNotifier tracks stop notifications from coding agents.
type AgentNotifier struct {
	mu       sync.Mutex
	waiters  map[string][]chan struct{} // session → wait channels
	recent   map[string]string          // session → status (arrived before WaitCh)
	sockPath string
	listener net.Listener
	done     chan struct{}
	stopOnce sync.Once
}

func NewAgentNotifier() *AgentNotifier {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/tmp"
	}
	return &AgentNotifier{
		waiters:  make(map[string][]chan struct{}),
		recent:   make(map[string]string),
		sockPath: filepath.Join(home, ".fingersaver", "hooks.sock"),
		done:     make(chan struct{}),
	}
}

// WaitCh returns a channel that closes when the session's agent stops.
// If a notification was already received (Notify called before WaitCh),
// returns an already-closed channel.
func (n *AgentNotifier) WaitCh(session string) <-chan struct{} {
	n.mu.Lock()
	defer n.mu.Unlock()

	// Check recent notifications first. Recent is sticky — multiple
	// WaitCh calls for the same session all get signaled until Clear.
	if _, ok := n.recent[session]; ok {
		ch := make(chan struct{})
		close(ch)
		return ch
	}

	ch := make(chan struct{})
	n.waiters[session] = append(n.waiters[session], ch)
	return ch
}

// Notify marks a session as stopped and wakes all waiters.
// Also stores in recent so subsequent WaitCh calls see the notification.
func (n *AgentNotifier) Notify(session, status string) {
	n.mu.Lock()
	defer n.mu.Unlock()

	channels := n.waiters[session]
	for _, ch := range channels {
		close(ch)
	}
	delete(n.waiters, session)

	// Always store in recent for late WaitCh callers.
	n.recent[session] = status
}

// Clear resets the notification state for a session.
// Closes any outstanding wait channels so callers don't block forever.
func (n *AgentNotifier) Clear(session string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, ch := range n.waiters[session] {
		close(ch)
	}
	delete(n.waiters, session)
	delete(n.recent, session)
}

// Start begins listening on the Unix socket for hook notifications.
func (n *AgentNotifier) Start(ctx context.Context) error {
	os.Remove(n.sockPath)

	if err := os.MkdirAll(filepath.Dir(n.sockPath), 0o755); err != nil {
		return err
	}

	lc := net.ListenConfig{}
	ln, err := lc.Listen(ctx, "unix", n.sockPath)
	if err != nil {
		return err
	}
	n.listener = ln

	go n.acceptLoop(ctx)
	return nil
}

// Stop closes the listener and cleans up the socket file.
// Safe to call multiple times.
func (n *AgentNotifier) Stop() {
	n.stopOnce.Do(func() {
		close(n.done)
		if n.listener != nil {
			n.listener.Close()
		}
		os.Remove(n.sockPath)
	})
}

func (n *AgentNotifier) acceptLoop(ctx context.Context) {
	backoff := 100 * time.Millisecond
	for {
		conn, err := n.listener.Accept()
		if err != nil {
			select {
			case <-n.done:
				return
			case <-ctx.Done():
				return
			default:
				log.Printf("[notifier] accept error: %v", err)
				select {
				case <-time.After(backoff):
				case <-n.done:
					return
				case <-ctx.Done():
					return
				}
				if backoff < 5*time.Second {
					backoff *= 2
				}
				continue
			}
		}
		backoff = 100 * time.Millisecond
		go n.handleConn(conn)
	}
}

func (n *AgentNotifier) handleConn(conn net.Conn) {
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	// Limit read to prevent abuse; hook payloads are <200 bytes.
	data, err := io.ReadAll(io.LimitReader(conn, 4096))
	if err != nil {
		return
	}

	var msg hookPayload
	if err := json.Unmarshal(data, &msg); err != nil {
		log.Printf("[notifier] invalid payload: %v", err)
		return
	}
	if msg.Session == "" {
		return
	}

	n.Notify(msg.Session, msg.Status)
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	conn.Write([]byte("ok\n"))
}
