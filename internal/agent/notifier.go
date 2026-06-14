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
	Type            string `json:"type"` // "agent_stop", "chat", "session", "claude_session_start"
	Session         string `json:"session,omitempty"`
	Status          string `json:"status,omitempty"`
	Role            string `json:"role,omitempty"`              // for chat: "assistant", "system"
	Content         string `json:"content,omitempty"`           // for chat/session: message text
	ClaudeSessionID string `json:"claude_session_id,omitempty"` // for claude_session_start
	TranscriptPath  string `json:"transcript_path,omitempty"`   // for claude_session_start
	Cwd             string `json:"cwd,omitempty"`               // for claude_session_start
}

// AgentNotifier tracks stop notifications from coding agents and dispatches
// external messages (chat, session) via callbacks.
type AgentNotifier struct {
	mu              sync.Mutex
	waiters         map[string]map[uint64]chan struct{} // session → waiter ID → wait channel
	seq             map[string]uint64                   // session → latest notification sequence
	lastStatus      map[string]string                   // session → last notification type ("permission", "done", etc.)
	working         map[string]bool                     // session → agent is mid-turn (UserPromptSubmit → Stop)
	unread          map[string]int                      // session → finished-task notifications not yet viewed
	nextID          uint64
	sockPath        string
	listener        net.Listener
	done            chan struct{}
	stopOnce        sync.Once
	onChat          func(role, content string)
	onSession       func(session, content string) error
	onAgentStop     func(session, status string)
	onAgentStart    func(session string)
	onPermission    func(session string)
	onClaudeSession func(claudeSessionID, tmuxSession, transcriptPath, cwd string)
}

func NewAgentNotifier() *AgentNotifier {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/tmp"
	}
	return &AgentNotifier{
		waiters:    make(map[string]map[uint64]chan struct{}),
		seq:        make(map[string]uint64),
		lastStatus: make(map[string]string),
		working:    make(map[string]bool),
		unread:     make(map[string]int),
		sockPath:   filepath.Join(home, ".makro", "hooks.sock"),
		done:       make(chan struct{}),
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

// LastStatus returns the most recent notification type for a session (e.g. "permission", "done").
func (n *AgentNotifier) LastStatus(session string) string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.lastStatus[session]
}

// Working reports whether the session's agent is currently mid-turn — set true
// on an agent_start notification and false on the next agent_stop.
func (n *AgentNotifier) Working(session string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.working[session]
}

// Unread returns the count of finished-task notifications for a session that
// the user has not yet viewed. Incremented on each agent_stop.
func (n *AgentNotifier) Unread(session string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.unread[session]
}

// ClearUnread resets the unread count for a session (called when the user
// views it).
func (n *AgentNotifier) ClearUnread(session string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.unread, session)
}

// Notify marks a session as stopped and wakes all waiters.
func (n *AgentNotifier) Notify(session, status string) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.seq[session]++
	n.lastStatus[session] = status
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

// OnAgentStart sets the callback for agent start notifications (UserPromptSubmit).
func (n *AgentNotifier) OnAgentStart(fn func(session string)) {
	n.mu.Lock()
	n.onAgentStart = fn
	n.mu.Unlock()
}

// OnClaudeSession sets the callback for Claude Code session-start mapping, so
// usage ingested from transcripts can be attributed to a tmux session.
func (n *AgentNotifier) OnClaudeSession(fn func(claudeSessionID, tmuxSession, transcriptPath, cwd string)) {
	n.mu.Lock()
	n.onClaudeSession = fn
	n.mu.Unlock()
}

// OnPermission sets the callback for permission request notifications.
func (n *AgentNotifier) OnPermission(fn func(session string)) {
	n.mu.Lock()
	n.onPermission = fn
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
	onAgentStart := n.onAgentStart
	onPermission := n.onPermission
	onClaudeSession := n.onClaudeSession
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
	case "agent_start":
		if msg.Session == "" {
			return
		}
		n.mu.Lock()
		n.working[msg.Session] = true
		n.mu.Unlock()
		if onAgentStart != nil {
			onAgentStart(msg.Session)
		}
	case "claude_session_start":
		// Claude Code session started in a tmux pane — record the
		// session_id ↔ tmux_session ↔ transcript_path mapping.
		if onClaudeSession != nil && msg.ClaudeSessionID != "" && msg.TranscriptPath != "" {
			onClaudeSession(msg.ClaudeSessionID, msg.Session, msg.TranscriptPath, msg.Cwd)
		}
	case "permission":
		if msg.Session == "" {
			return
		}
		n.Notify(msg.Session, "permission")
		if onPermission != nil {
			onPermission(msg.Session)
		}
	default:
		// Backward compat: messages without type are agent_stop.
		if msg.Session == "" {
			return
		}
		n.Notify(msg.Session, msg.Status)
		n.mu.Lock()
		n.working[msg.Session] = false
		n.unread[msg.Session]++
		n.mu.Unlock()
		if onAgentStop != nil {
			onAgentStop(msg.Session, msg.Status)
		}
	}

	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte("ok\n")); err != nil {
		log.Printf("[notifier] write ack: %v", err)
	}
}
