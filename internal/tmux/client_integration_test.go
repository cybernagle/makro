//go:build integration

package tmux

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testClient(t *testing.T) *Client {
	t.Helper()
	// Use /tmp for short socket paths (macOS has a ~104 char limit for UNIX sockets).
	socketPath := filepath.Join("/tmp", fmt.Sprintf("fs-test-%d.sock", time.Now().UnixNano()))
	t.Cleanup(func() { os.Remove(socketPath) })
	client := NewClient(socketPath, true)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)

	require.NoError(t, client.Start(ctx))
	t.Cleanup(func() { client.Stop() })
	return client
}

func TestIntegrationClientStartsAndStops(t *testing.T) {
	socketPath := filepath.Join("/tmp", fmt.Sprintf("fs-stop-%d.sock", time.Now().UnixNano()))
	t.Cleanup(func() { os.Remove(socketPath) })
	client := NewClient(socketPath, true)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, client.Start(ctx))
	assert.NotNil(t, client.State())

	require.NoError(t, client.Stop())
}

func TestIntegrationCreateSession(t *testing.T) {
	client := testClient(t)

	err := client.SendCommand(NewSessionCmd("test-sess", "", "bash"))
	require.NoError(t, err)

	// Wait for poll to pick up the new session.
	Eventually(t, 3*time.Second, func() bool {
		return client.State().FindSession("test-sess") != nil
	})

	s := client.State().FindSession("test-sess")
	assert.NotNil(t, s)
}

func TestIntegrationSendKeysAndCapture(t *testing.T) {
	client := testClient(t)

	require.NoError(t, client.SendCommand(NewSessionCmd("echo-test", "", "bash")))

	Eventually(t, 3*time.Second, func() bool {
		return client.State().FindSession("echo-test") != nil
	})

	require.NoError(t, client.SendCommand(SendKeysCmd("echo-test", "echo 'hello makro'")))
	require.NoError(t, client.SendCommand(SendEnterCmd("echo-test")))

	// Give the command time to execute.
	time.Sleep(1 * time.Second)

	// Capture pane output directly from the session.
	output, err := client.Exec(CapturePaneCmd("echo-test"))
	require.NoError(t, err)
	assert.Contains(t, output, "hello makro")
}

func TestIntegrationMultipleSessions(t *testing.T) {
	client := testClient(t)

	require.NoError(t, client.SendCommand(NewSessionCmd("sess-a", "", "bash")))
	require.NoError(t, client.SendCommand(NewSessionCmd("sess-b", "", "bash")))

	Eventually(t, 3*time.Second, func() bool {
		return client.State().FindSession("sess-a") != nil &&
			client.State().FindSession("sess-b") != nil
	})

	assert.NotNil(t, client.State().FindSession("sess-a"))
	assert.NotNil(t, client.State().FindSession("sess-b"))
}

func TestIntegrationKillSession(t *testing.T) {
	client := testClient(t)

	require.NoError(t, client.SendCommand(NewSessionCmd("to-kill", "", "bash")))
	Eventually(t, 3*time.Second, func() bool {
		return client.State().FindSession("to-kill") != nil
	})

	require.NoError(t, client.SendCommand(KillSessionCmd("to-kill")))

	// Verify via exec that the session is gone.
	Eventually(t, 3*time.Second, func() bool {
		_, err := client.Exec(HasSessionCmd("to-kill"))
		return err != nil
	})
}

func TestIntegrationExec(t *testing.T) {
	client := testClient(t)

	// Exec should work for listing sessions when none exist.
	_, err := client.Exec("list-sessions")
	// Will error when no sessions, that's expected.
	// Just verify Exec doesn't panic.
	_ = err
}

// Eventually polls fn until it returns true or timeout.
func Eventually(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.After(timeout)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if fn() {
			return
		}
		select {
		case <-tick.C:
		case <-deadline:
			t.Fatal("condition not met within timeout")
		}
	}
}
