package agent

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dialAndNotify connects to the Unix socket, sends a payload, and returns.
func dialAndNotify(t *testing.T, sockPath, session, status string) {
	t.Helper()
	conn, err := net.Dial("unix", sockPath)
	require.NoError(t, err)
	msg, _ := json.Marshal(hookPayload{Session: session, Status: status})
	conn.Write(msg)
	// Signal EOF so server's io.ReadAll returns.
	if uc, ok := conn.(*net.UnixConn); ok {
		uc.CloseWrite()
	}
	conn.Close()
}

func TestNotifierNotifyWakesWaiter(t *testing.T) {
	n := NewAgentNotifier()
	ch, cancel := n.WaitAfter("auth", n.Snapshot("auth"))
	defer cancel()

	go func() {
		time.Sleep(50 * time.Millisecond)
		n.Notify("auth", "done")
	}()

	select {
	case <-ch:
		// Expected: channel closed.
	case <-time.After(2 * time.Second):
		t.Fatal("WaitCh was not closed after Notify")
	}
}

func TestNotifierMultipleWaiters(t *testing.T) {
	n := NewAgentNotifier()
	after := n.Snapshot("worker")

	var wg sync.WaitGroup
	woken := make([]bool, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		ch, cancel := n.WaitAfter("worker", after)
		idx := i
		go func() {
			defer wg.Done()
			defer cancel()
			<-ch
			woken[idx] = true
		}()
	}

	time.Sleep(50 * time.Millisecond)
	n.Notify("worker", "done")
	wg.Wait()

	for i, w := range woken {
		assert.True(t, w, "waiter %d should be woken", i)
	}
}

func TestNotifierNotifyBeforeWait(t *testing.T) {
	n := NewAgentNotifier()
	before := n.Snapshot("auth")
	n.Notify("auth", "done")

	ch, cancel := n.WaitAfter("auth", before)
	defer cancel()
	select {
	case <-ch:
		// Expected: already closed.
	default:
		t.Fatal("WaitAfter should be closed when Notify was called first")
	}
}

func TestNotifierWaitAfterCurrentSnapshotStaysOpen(t *testing.T) {
	n := NewAgentNotifier()
	n.Notify("auth", "done")
	after := n.Snapshot("auth")

	ch, cancel := n.WaitAfter("auth", after)
	defer cancel()
	select {
	case <-ch:
		t.Fatal("WaitAfter should not be closed at the current snapshot")
	case <-time.After(100 * time.Millisecond):
		// Expected: still open.
	}
}

func TestNotifierNoNotifyNoWake(t *testing.T) {
	n := NewAgentNotifier()
	ch, cancel := n.WaitAfter("auth", n.Snapshot("auth"))
	defer cancel()

	select {
	case <-ch:
		t.Fatal("WaitAfter should not close without Notify")
	case <-time.After(200 * time.Millisecond):
		// Expected: still open.
	}
}

func newNotifierWithShortPath(t *testing.T) *AgentNotifier {
	t.Helper()
	// macOS Unix socket path limit is ~104 chars. Use /tmp to stay short.
	sockDir, err := os.MkdirTemp("", "fs-ht")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	n := NewAgentNotifier()
	n.sockPath = filepath.Join(sockDir, "h.sock")
	return n
}

func TestNotifierUnixSocket(t *testing.T) {
	n := newNotifierWithShortPath(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, n.Start(ctx))
	defer n.Stop()

	dialAndNotify(t, n.sockPath, "auth", "done")

	// Verify waiter is woken.
	ch, cancelWait := n.WaitAfter("auth", 0)
	defer cancelWait()
	select {
	case <-ch:
		// Expected.
	case <-time.After(2 * time.Second):
		t.Fatal("WaitAfter not closed after socket notification")
	}
}

func TestNotifierUnixSocketWithoutCloseWrite(t *testing.T) {
	n := newNotifierWithShortPath(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, n.Start(ctx))
	defer n.Stop()

	conn, err := net.Dial("unix", n.sockPath)
	require.NoError(t, err)
	defer conn.Close()

	msg, err := json.Marshal(hookPayload{Session: "auth", Status: "done"})
	require.NoError(t, err)
	_, err = conn.Write(msg)
	require.NoError(t, err)

	buf := make([]byte, 64)
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	nr, err := conn.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "ok\n", string(buf[:nr]))

	ch, cancelWait := n.WaitAfter("auth", 0)
	defer cancelWait()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("WaitAfter not closed after socket notification without CloseWrite")
	}
}

func TestNotifierUnixSocketIgnoresEmptySession(t *testing.T) {
	n := newNotifierWithShortPath(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, n.Start(ctx))
	defer n.Stop()

	dialAndNotify(t, n.sockPath, "", "done")

	time.Sleep(100 * time.Millisecond)

	// No notification should have been recorded.
	ch, cancelWait := n.WaitAfter("auth", n.Snapshot("auth"))
	defer cancelWait()
	select {
	case <-ch:
		t.Fatal("empty session should not trigger notification")
	case <-time.After(100 * time.Millisecond):
		// Expected.
	}
}

func TestNotifierRecentSticky(t *testing.T) {
	n := NewAgentNotifier()
	before := n.Snapshot("auth")
	n.Notify("auth", "done")

	ch1, cancel1 := n.WaitAfter("auth", before)
	defer cancel1()
	ch2, cancel2 := n.WaitAfter("auth", before)
	defer cancel2()

	select {
	case <-ch1:
	default:
		t.Fatal("first WaitAfter should be closed")
	}
	select {
	case <-ch2:
	default:
		t.Fatal("second WaitAfter should also be closed for the same earlier snapshot")
	}
}

func TestNotifierConcurrentNotifyAndWaitAfter(t *testing.T) {
	n := NewAgentNotifier()
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(iterations)
	for i := 0; i < iterations; i++ {
		go func() {
			defer wg.Done()
			_, cancel := n.WaitAfter("auth", n.Snapshot("auth"))
			cancel()
			n.Notify("auth", "done")
		}()
	}
	wg.Wait()

	// Should not panic or deadlock. Waiting at the current snapshot should stay open.
	ch, cancel := n.WaitAfter("auth", n.Snapshot("auth"))
	defer cancel()
	select {
	case <-ch:
		t.Fatal("WaitAfter should be open at the current snapshot")
	case <-time.After(100 * time.Millisecond):
		// Expected.
	}
}

func TestNotifierCancelRemovesWaiter(t *testing.T) {
	n := NewAgentNotifier()
	ch, cancel := n.WaitAfter("auth", n.Snapshot("auth"))

	cancel()
	n.Notify("auth", "done")

	select {
	case <-ch:
		t.Fatal("cancelled waiter should not be woken")
	case <-time.After(100 * time.Millisecond):
		// Expected.
	}
}

func TestNotifierUnixSocketMalformedJSON(t *testing.T) {
	n := newNotifierWithShortPath(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, n.Start(ctx))
	defer n.Stop()

	conn, err := net.Dial("unix", n.sockPath)
	require.NoError(t, err)
	conn.Write([]byte("not json at all"))
	if uc, ok := conn.(*net.UnixConn); ok {
		uc.CloseWrite()
	}
	conn.Close()

	time.Sleep(100 * time.Millisecond)

	// No notification should be recorded.
	ch, cancelWait := n.WaitAfter("auth", n.Snapshot("auth"))
	defer cancelWait()
	select {
	case <-ch:
		t.Fatal("malformed JSON should not trigger notification")
	case <-time.After(100 * time.Millisecond):
		// Expected.
	}
}

// dialAndSend connects to the Unix socket and sends a fully-typed payload.
func dialAndSend(t *testing.T, sockPath string, payload hookPayload) {
	t.Helper()
	conn, err := net.Dial("unix", sockPath)
	require.NoError(t, err)
	msg, _ := json.Marshal(payload)
	conn.Write(msg)
	if uc, ok := conn.(*net.UnixConn); ok {
		uc.CloseWrite()
	}
	conn.Close()
}

func TestNotifierWorkingStartAndStop(t *testing.T) {
	n := newNotifierWithShortPath(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, n.Start(ctx))
	defer n.Stop()

	assert.False(t, n.Working("auth"), "no event yet → not working")

	dialAndSend(t, n.sockPath, hookPayload{Type: "agent_start", Session: "auth"})
	require.Eventually(t, func() bool { return n.Working("auth") }, time.Second, 10*time.Millisecond,
		"agent_start should mark session working")

	// A permission event mid-turn must NOT clear working.
	dialAndSend(t, n.sockPath, hookPayload{Type: "permission", Session: "auth"})
	time.Sleep(50 * time.Millisecond)
	assert.True(t, n.Working("auth"), "permission must not clear working")

	dialAndSend(t, n.sockPath, hookPayload{Type: "agent_stop", Session: "auth", Status: "done"})
	require.Eventually(t, func() bool { return !n.Working("auth") }, time.Second, 10*time.Millisecond,
		"agent_stop should clear working")
}

func TestNotifierOnAgentStartCallback(t *testing.T) {
	n := newNotifierWithShortPath(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, n.Start(ctx))
	defer n.Stop()

	var got atomic.Value
	n.OnAgentStart(func(session string) { got.Store(session) })

	dialAndSend(t, n.sockPath, hookPayload{Type: "agent_start", Session: "worker"})

	require.Eventually(t, func() bool {
		v := got.Load()
		return v != nil && v.(string) == "worker"
	}, time.Second, 10*time.Millisecond, "OnAgentStart should fire with the session name")

	assert.True(t, n.Working("worker"), "callback and working flag set together")
}
