package tmux

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClient(t *testing.T) {
	c := NewClient("/tmp/test.sock", true)
	assert.Equal(t, "/tmp/test.sock", c.socketPath)
	assert.NotNil(t, c.notifs)
	assert.NotNil(t, c.state)
}

func TestClientStartTwice(t *testing.T) {
	c := NewClient(t.TempDir()+"/test.sock", true)
	// Set running=true manually to simulate.
	c.mu.Lock()
	c.running = true
	c.mu.Unlock()
	err := c.Start(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already running")
	c.mu.Lock()
	c.running = false
	c.mu.Unlock()
}

func TestClientSendNotRunning(t *testing.T) {
	c := NewClient(t.TempDir()+"/test.sock", true)
	err := c.SendCommand("list-sessions")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not running")
}

func TestClientStopNotRunning(t *testing.T) {
	c := NewClient(t.TempDir()+"/test.sock", true)
	err := c.Stop()
	assert.NoError(t, err)
}

func TestClientNotificationChannel(t *testing.T) {
	c := NewClient(t.TempDir()+"/test.sock", true)
	ch := c.Notifications()
	assert.NotNil(t, ch)
}

func TestClientStateMirror(t *testing.T) {
	c := NewClient(t.TempDir()+"/test.sock", true)
	s := c.State()
	require.NotNil(t, s)
	assert.Empty(t, s.Sessions())
}

func TestParseNotificationFromRealOutput(t *testing.T) {
	// Simulate real tmux -CC output lines.
	lines := []string{
		"%session-changed $0 __fingersaver_control",
		"%window-add @0",
		"%window-pane-changed @0 %0",
		"%layout-change @0 0000x24,0{0;0,80,24,0} 0000x24,0{0;0,80,24,0} 0",
		"%output %0 \\033[?1034h",
	}

	expected := []NotifType{
		NotifSessionChanged,
		NotifWindowAdd,
		NotifWindowPaneChanged,
		NotifLayoutChange,
		NotifOutput,
	}

	for i, line := range lines {
		n, err := ParseNotification(line)
		require.NoError(t, err, "line %d: %s", i, line)
		assert.Equal(t, expected[i], n.Type, "line %d: %s", i, line)
	}
}

func TestStateMirrorFromRealSequence(t *testing.T) {
	sm := NewStateMirror()

	notifications := []Notification{
		{Type: NotifSessionChanged, SessionID: "$0", SessionName: "test-session"},
		{Type: NotifWindowAdd, WindowID: "@0"},
		{Type: NotifWindowPaneChanged, WindowID: "@0", PaneID: "%0"},
		{Type: NotifOutput, PaneID: "%0", Data: "hello"},
		{Type: NotifOutput, PaneID: "%0", Data: " world"},
		{Type: NotifSessionChanged, SessionID: "$1", SessionName: "second"},
		{Type: NotifWindowAdd, WindowID: "@1"},
		{Type: NotifWindowPaneChanged, WindowID: "@1", PaneID: "%1"},
		{Type: NotifSessionWindowChanged, SessionID: "$1", WindowID: "@1"},
	}

	for _, n := range notifications {
		require.NoError(t, sm.Apply(n))
	}

	sessions := sm.Sessions()
	assert.Len(t, sessions, 2)

	s0 := sm.FindSession("test-session")
	require.NotNil(t, s0)
	assert.Equal(t, "$0", s0.ID)
	require.Len(t, s0.Windows, 1)
	assert.Equal(t, "hello world", s0.Windows[0].Panes[0].Output)

	s1 := sm.FindSession("second")
	require.NotNil(t, s1)
	assert.Equal(t, "@1", s1.ActiveWindowID)
}

func TestClientContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c := NewClient(t.TempDir()+"/test.sock", true)

	// Start a goroutine that would be reading notifications.
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
	}()

	cancel()

	select {
	case <-done:
		// Context cancelled successfully.
	case <-time.After(time.Second):
		t.Fatal("context cancellation timed out")
	}

	err := c.Stop()
	assert.NoError(t, err)
}
