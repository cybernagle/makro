package agent

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
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
	ch := n.WaitCh("auth")

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

	var wg sync.WaitGroup
	woken := make([]bool, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		ch := n.WaitCh("worker")
		idx := i
		go func() {
			defer wg.Done()
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
	n.Notify("auth", "done")

	ch := n.WaitCh("auth")
	select {
	case <-ch:
		// Expected: already closed.
	default:
		t.Fatal("WaitCh should be closed when Notify was called first")
	}
}

func TestNotifierClearResets(t *testing.T) {
	n := NewAgentNotifier()
	n.Notify("auth", "done")
	n.Clear("auth")

	ch := n.WaitCh("auth")
	select {
	case <-ch:
		t.Fatal("WaitCh should not be closed after Clear")
	case <-time.After(100 * time.Millisecond):
		// Expected: still open.
	}
}

func TestNotifierNoNotifyNoWake(t *testing.T) {
	n := NewAgentNotifier()
	ch := n.WaitCh("auth")

	select {
	case <-ch:
		t.Fatal("WaitCh should not close without Notify")
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
	ch := n.WaitCh("auth")
	select {
	case <-ch:
		// Expected.
	case <-time.After(2 * time.Second):
		t.Fatal("WaitCh not closed after socket notification")
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
	ch := n.WaitCh("auth")
	select {
	case <-ch:
		t.Fatal("empty session should not trigger notification")
	case <-time.After(100 * time.Millisecond):
		// Expected.
	}
}

func TestNotifierRecentSticky(t *testing.T) {
	// Multiple WaitCh calls after Notify should all return closed channels.
	n := NewAgentNotifier()
	n.Notify("auth", "done")

	ch1 := n.WaitCh("auth")
	ch2 := n.WaitCh("auth")

	select {
	case <-ch1:
	default:
		t.Fatal("first WaitCh should be closed")
	}
	select {
	case <-ch2:
	default:
		t.Fatal("second WaitCh should also be closed (sticky recent)")
	}
}

func TestNotifierConcurrentNotifyAndClear(t *testing.T) {
	n := NewAgentNotifier()
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(iterations)
	for i := 0; i < iterations; i++ {
		go func() {
			defer wg.Done()
			n.Notify("auth", "done")
			n.Clear("auth")
		}()
	}
	wg.Wait()

	// Should not panic or deadlock. After all clears, WaitCh should be open.
	ch := n.WaitCh("auth")
	select {
	case <-ch:
		t.Fatal("WaitCh should be open after concurrent Notify+Clear")
	case <-time.After(100 * time.Millisecond):
		// Expected.
	}
}

func TestNotifierClearClosesWaiters(t *testing.T) {
	n := NewAgentNotifier()
	ch := n.WaitCh("auth")

	n.Clear("auth")

	select {
	case <-ch:
		// Expected: channel closed by Clear.
	case <-time.After(2 * time.Second):
		t.Fatal("Clear should close outstanding waiter channels")
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
	ch := n.WaitCh("auth")
	select {
	case <-ch:
		t.Fatal("malformed JSON should not trigger notification")
	case <-time.After(100 * time.Millisecond):
		// Expected.
	}
}
