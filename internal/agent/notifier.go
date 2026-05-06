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
	Type    string `json:"type"` // "agent_stop", "chat", "session"
	Session string `json:"session,omitempty"`
	Status  string `json:"status,omitempty"`
	Role    string `json:"role,omitempty"`    // for chat: "assistant", "system"
	Content string `json:"content,omitempty"` // for chat/session: message text
}

// AgentNotifier tracks stop notifications from coding agents and dispatches
// external messages (chat, session) via callbacks.
type AgentNotifier struct {
	mu          sync.Mutex
	waiters     map[string]map[uint64]chan struct{} // session → waiter ID → wait channel
	seq         map[string]uint64                   // session → latest notification sequence
	nextID      uint64
	sockPath    string
	listener    net.Listener
	done        chan struct{}
	stopOnce    sync.Once
	onChat      func(role, content string)
	onSession   func(session, content string) error
	onAgentStop func(session, status string)
}

func NewAgentNotifier() *AgentNotifier {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/tmp"
	}
	return &AgentNotifier{
		waiters:  make(map[string]map[uint64]chan struct{}),
		seq:      make(map[string]uint64),
		sockPath: filepath.Join(home, ".fingersaver", "hooks.sock"),
		done:     make(chan struct{}),
	}
}

// Snapshot returns the latest notification sequence for a session.
func (n *AgentNotifier) Snapshot(session string) uint64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.seq[session]
}

// WaitAfter returns a channel that closes after the session receives a
// notification newer than after. The returned cancel function unregisters the
// waiter without waking other waiters for the same session.
func (n *AgentNotifier) WaitAfter(session string, after uint64) (<-chan struct{}, func()) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.seq[session] > after {
		ch := make(chan struct{})
		close(ch)
		return ch, func() {}
	}

	ch := make(chan struct{})
	n.nextID++
	waiterID := n.nextID
	if n.waiters[session] == nil {
		n.waiters[session] = make(map[uint64]chan struct{})
	}
	n.waiters[session][waiterID] = ch

	cancel := func() {
		n.mu.Lock()
		defer n.mu.Unlock()
		waiters := n.waiters[session]
		if waiters == nil {
			return
		}
		delete(waiters, waiterID)
		if len(waiters) == 0 {
			delete(n.waiters, session)
		}
	}
	return ch, cancel
}

// Notify marks a session as stopped and wakes all waiters.
func (n *AgentNotifier) Notify(session, status string) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.seq[session]++
	waiters := n.waiters[session]
	for _, ch := range waiters {
		close(ch)
	}
	delete(n.waiters, session)
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

// OnChat sets the callback for incoming chat messages from the socket.
func (n *AgentNotifier) OnChat(fn func(role, content string)) {
	n.mu.Lock()
	n.onChat = fn
	n.mu.Unlock()
}

// OnSession sets the callback for incoming session messages from the socket.
func (n *AgentNotifier) OnSession(fn func(session, content string) error) {
	n.mu.Lock()
	n.onSession = fn
	n.mu.Unlock()
}

// OnAgentStop sets the callback for agent stop notifications.
func (n *AgentNotifier) OnAgentStop(fn func(session, status string)) {
	n.mu.Lock()
	n.onAgentStop = fn
	n.mu.Unlock()
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

	dec := json.NewDecoder(io.LimitReader(conn, 4096))
	var msg hookPayload
	if err := dec.Decode(&msg); err != nil {
		log.Printf("[notifier] invalid payload: %v", err)
		return
	}

	n.mu.Lock()
	onChat := n.onChat
	onSession := n.onSession
	onAgentStop := n.onAgentStop
	n.mu.Unlock()

	switch msg.Type {
	case "chat":
		if onChat != nil && msg.Content != "" {
			role := msg.Role
			if role == "" {
				role = "system"
			}
			onChat(role, msg.Content)
		}
	case "session":
		if onSession != nil && msg.Session != "" && msg.Content != "" {
			if err := onSession(msg.Session, msg.Content); err != nil {
				conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
				conn.Write([]byte("error: " + err.Error() + "\n"))
				return
			}
		}
	default:
		// Backward compat: messages without type are agent_stop.
		if msg.Session == "" {
			return
		}
		n.Notify(msg.Session, msg.Status)
		if onAgentStop != nil {
			onAgentStop(msg.Session, msg.Status)
		}
	}

	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte("ok\n")); err != nil {
		log.Printf("[notifier] write ack: %v", err)
	}
}
