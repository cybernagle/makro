package notify

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"time"
)

// HookSocketPath is the Unix socket the running Makro instance listens on
// for hook events (agent stop, permission requests).
func HookSocketPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/makro-hooks.sock"
	}
	return filepath.Join(home, ".makro", "hooks.sock")
}

// SendHook forwards a hook payload to a running Makro instance over the socket.
// Best-effort: silently returns nil when Makro isn't running (e.g. the hook
// fires while Makro is quit) so the calling agent never blocks on it.
func SendHook(payload map[string]string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", HookSocketPath())
	if err != nil {
		return nil // Makro not running — nothing to do.
	}
	defer conn.Close()

	msg, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(msg); err != nil {
		return err
	}
	if uc, ok := conn.(*net.UnixConn); ok {
		_ = uc.CloseWrite()
	}
	return nil
}
