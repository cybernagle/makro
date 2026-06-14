package main

import (
	"encoding/json"
	"testing"

	"github.com/naglezhang/makro/internal/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEmitSessionStateBroadcasts verifies the Stage 3 wiring: emitSessionState
// marshals the notifier's per-session state and pushes a session_state event
// onto the chat hub that every connected client reads.
func TestEmitSessionStateBroadcasts(t *testing.T) {
	hub := newChatHub()
	s := &ChatService{hub: hub, notifier: agent.NewAgentNotifier()}
	ch := hub.Subscribe()

	s.emitSessionState("auth")

	evt := <-ch
	assert.Equal(t, "session_state", evt.Type)

	var payload struct {
		Session string `json:"session"`
		Working bool   `json:"working"`
		Unread  int    `json:"unread"`
	}
	require.NoError(t, json.Unmarshal([]byte(evt.Data), &payload))
	assert.Equal(t, "auth", payload.Session)
	assert.False(t, payload.Working, "fresh session → not working")
	assert.Equal(t, 0, payload.Unread, "fresh session → 0 unread")
}

// TestEmitSessionStateNoNotifierMustNotPanic guards the nil-check path
// (chatSvc before init completes).
func TestEmitSessionStateNoNotifierMustNotPanic(t *testing.T) {
	s := &ChatService{hub: newChatHub()}
	assert.NotPanics(t, func() { s.emitSessionState("auth") })
}
