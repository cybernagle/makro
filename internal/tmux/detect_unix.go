//go:build !windows

package tmux

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// DetectServer attempts to find a running tmux server.
// Returns nil if no server is found.
func DetectServer() *ServerInfo {
	// Method 1: $TMUX env var (set when running inside tmux).
	if socket := socketFromEnv(); socket != "" {
		if info := validateSocket(socket); info != nil {
			return info
		}
	}

	// Method 2: Probe default socket path.
	if info := validateSocket(defaultSocketPath()); info != nil {
		return info
	}

	return nil
}

// socketFromEnv extracts the socket path from the $TMUX environment variable.
// Format: <socket_path>,<pid>,<session_id>
func socketFromEnv() string {
	tmux := os.Getenv("TMUX")
	if tmux == "" {
		return ""
	}
	part, _, found := strings.Cut(tmux, ",")
	if !found || part == "" {
		return ""
	}
	return part
}

// defaultSocketPath returns the standard tmux socket path: $TMUX_TMPDIR/tmux-$UID/default
func defaultSocketPath() string {
	tmpDir := os.Getenv("TMUX_TMPDIR")
	if tmpDir == "" {
		tmpDir = "/tmp"
	}
	return tmpDir + "/tmux-" + strconv.Itoa(os.Getuid()) + "/default"
}

// validateSocket checks if a tmux server is listening on the given socket.
func validateSocket(socketPath string) *ServerInfo {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "tmux", "-S", socketPath, "list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		return nil
	}

	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return &ServerInfo{SocketPath: socketPath}
	}

	return &ServerInfo{SocketPath: socketPath, Sessions: strings.Split(trimmed, "\n")}
}
