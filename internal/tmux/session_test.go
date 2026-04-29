package tmux

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStateMirrorSessionLifecycle(t *testing.T) {
	sm := NewStateMirror()

	// Create session.
	require.NoError(t, sm.Apply(Notification{Type: NotifSessionChanged, SessionID: "$0", SessionName: "main"}))
	s := sm.FindSession("main")
	require.NotNil(t, s)
	assert.Equal(t, "$0", s.ID)

	// Rename session.
	require.NoError(t, sm.Apply(Notification{Type: NotifSessionRenamed, SessionID: "$0", SessionName: "renamed"}))
	assert.Nil(t, sm.FindSession("main"))
	assert.Equal(t, "renamed", sm.FindSession("renamed").Name)

	// Remove session.
	sm.RemoveSession("$0")
	assert.Nil(t, sm.FindSession("renamed"))
	assert.Empty(t, sm.Sessions())
}

func TestStateMirrorWindowLifecycle(t *testing.T) {
	sm := NewStateMirror()
	require.NoError(t, sm.Apply(Notification{Type: NotifSessionChanged, SessionID: "$0", SessionName: "main"}))

	// Add window.
	require.NoError(t, sm.Apply(Notification{Type: NotifWindowAdd, WindowID: "@1"}))
	s := sm.FindSession("main")
	require.Len(t, s.Windows, 1)
	assert.Equal(t, "@1", s.Windows[0].ID)

	// Rename window.
	require.NoError(t, sm.Apply(Notification{Type: NotifWindowRenamed, WindowID: "@1", SessionName: "editor"}))
	assert.Equal(t, "editor", s.Windows[0].Name)

	// Close window.
	require.NoError(t, sm.Apply(Notification{Type: NotifWindowClose, WindowID: "@1"}))
	assert.Empty(t, s.Windows)
}

func TestStateMirrorPaneOutput(t *testing.T) {
	sm := NewStateMirror()
	require.NoError(t, sm.Apply(Notification{Type: NotifSessionChanged, SessionID: "$0", SessionName: "main"}))
	require.NoError(t, sm.Apply(Notification{Type: NotifWindowAdd, WindowID: "@1"}))
	require.NoError(t, sm.Apply(Notification{Type: NotifWindowPaneChanged, WindowID: "@1", PaneID: "%0"}))

	// Stream output.
	require.NoError(t, sm.Apply(Notification{Type: NotifOutput, PaneID: "%0", Data: "hello "}))
	require.NoError(t, sm.Apply(Notification{Type: NotifOutput, PaneID: "%0", Data: "world"}))

	s := sm.FindSession("main")
	p := s.Windows[0].Panes[0]
	assert.Equal(t, "hello world", p.Output)
	assert.True(t, p.Active)
}

func TestStateMirrorActiveWindowAndPane(t *testing.T) {
	sm := NewStateMirror()
	require.NoError(t, sm.Apply(Notification{Type: NotifSessionChanged, SessionID: "$0", SessionName: "main"}))
	require.NoError(t, sm.Apply(Notification{Type: NotifWindowAdd, WindowID: "@1"}))
	require.NoError(t, sm.Apply(Notification{Type: NotifWindowAdd, WindowID: "@2"}))
	require.NoError(t, sm.Apply(Notification{Type: NotifWindowPaneChanged, WindowID: "@1", PaneID: "%0"}))
	require.NoError(t, sm.Apply(Notification{Type: NotifWindowPaneChanged, WindowID: "@2", PaneID: "%1"}))

	s := sm.FindSession("main")

	// Set active window.
	require.NoError(t, sm.Apply(Notification{Type: NotifSessionWindowChanged, SessionID: "$0", WindowID: "@2"}))
	w := s.ActiveWindow()
	require.NotNil(t, w)
	assert.Equal(t, "@2", w.ID)

	// Active pane in active window.
	p := s.ActivePane()
	require.NotNil(t, p)
	assert.Equal(t, "%1", p.ID)
	assert.True(t, p.Active)
}

func TestStateMirrorMultipleSessions(t *testing.T) {
	sm := NewStateMirror()
	require.NoError(t, sm.Apply(Notification{Type: NotifSessionChanged, SessionID: "$0", SessionName: "auth"}))
	require.NoError(t, sm.Apply(Notification{Type: NotifSessionChanged, SessionID: "$1", SessionName: "frontend"}))

	sessions := sm.Sessions()
	assert.Len(t, sessions, 2)

	assert.NotNil(t, sm.FindSession("auth"))
	assert.NotNil(t, sm.FindSession("frontend"))
	assert.Equal(t, "auth", sm.FindSessionByID("$0").Name)
	assert.Equal(t, "frontend", sm.FindSessionByID("$1").Name)
}

func TestStateMirrorRenameUpdatesByNameIndex(t *testing.T) {
	sm := NewStateMirror()
	require.NoError(t, sm.Apply(Notification{Type: NotifSessionChanged, SessionID: "$0", SessionName: "old"}))

	require.NoError(t, sm.Apply(Notification{Type: NotifSessionRenamed, SessionID: "$0", SessionName: "new"}))
	assert.Nil(t, sm.FindSession("old"))
	assert.NotNil(t, sm.FindSession("new"))
	assert.Equal(t, "new", sm.FindSessionByID("$0").Name)
}

func TestStateMirrorSessionChangedUpsert(t *testing.T) {
	sm := NewStateMirror()
	require.NoError(t, sm.Apply(Notification{Type: NotifSessionChanged, SessionID: "$0", SessionName: "first"}))
	require.NoError(t, sm.Apply(Notification{Type: NotifSessionChanged, SessionID: "$0", SessionName: "first"}))

	assert.Len(t, sm.Sessions(), 1)
	assert.Equal(t, "$0", sm.FindSession("first").ID)
}

func TestStateMirrorRenameMissingSession(t *testing.T) {
	sm := NewStateMirror()
	err := sm.Apply(Notification{Type: NotifSessionRenamed, SessionID: "$99", SessionName: "ghost"})
	assert.Error(t, err)
}

func TestStateMirrorNoopNotifications(t *testing.T) {
	sm := NewStateMirror()
	// These should not error and not change state.
	require.NoError(t, sm.Apply(Notification{Type: NotifSessionsChanged}))
	require.NoError(t, sm.Apply(Notification{Type: NotifBegin, Timestamp: 1, Number: 1, Flags: 0}))
	require.NoError(t, sm.Apply(Notification{Type: NotifEnd, Timestamp: 1, Number: 1, Flags: 0}))
	require.NoError(t, sm.Apply(Notification{Type: NotifError, Timestamp: 1, Number: 1, Flags: 0}))
	require.NoError(t, sm.Apply(Notification{Type: NotifPause, PaneID: "%0"}))
	require.NoError(t, sm.Apply(Notification{Type: NotifContinue, PaneID: "%0"}))
	require.NoError(t, sm.Apply(Notification{Type: NotifClientDetached, ClientName: "/dev/ttys000"}))
	require.NoError(t, sm.Apply(Notification{Type: NotifLayoutChange, WindowID: "@1"}))
	require.NoError(t, sm.Apply(Notification{Type: NotifPaneModeChanged, PaneID: "%0"}))
	require.NoError(t, sm.Apply(Notification{Type: NotifSubscriptionChanged, Data: "x"}))
	require.NoError(t, sm.Apply(Notification{Type: NotifUnknown, Data: "%something"}))
	assert.Empty(t, sm.Sessions())
}

func TestStateMirrorActivePaneEmpty(t *testing.T) {
	sm := NewStateMirror()
	require.NoError(t, sm.Apply(Notification{Type: NotifSessionChanged, SessionID: "$0", SessionName: "empty"}))

	s := sm.FindSession("empty")
	assert.Nil(t, s.ActiveWindow())
	assert.Nil(t, s.ActivePane())
}
