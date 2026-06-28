package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/naglezhang/makro/internal/tmux"
)

var tmuxBin string
var tmuxOnce sync.Once

// getTmuxBin resolves the tmux binary. Delegates to tmux.FindTmuxBin so the GUI
// shares one lookup implementation with *tmux.Client instead of maintaining a
// second copy. The sync.Once memoization is kept because FindTmuxBin does disk
// stats on every call and the GUI hits it on the hot capture/render path.
func getTmuxBin() string {
	tmuxOnce.Do(func() {
		tmuxBin = tmux.FindTmuxBin()
	})
	return tmuxBin
}

type TmuxService struct{}

type Session struct {
	Name    string `json:"name"`
	Active  bool   `json:"active"`
	Working bool   `json:"working,omitempty"`
	Unread  int    `json:"unread,omitempty"`
}

// tmuxArgs builds command-line args for the user's default tmux socket. Delegates
// to tmux.DefaultArgs so the socket convention (no -S flag → daily server) lives
// in one place, shared with *tmux.Client's socketPath == "" branch.
func tmuxArgs(extra ...string) []string {
	return tmux.DefaultArgs(extra...)
}

// tmuxSocketPath is kept for snapshot/recovery to detect if tmux has crashed.
// Returns the default socket path that tmux would use for the current user.
func tmuxSocketPath() string {
	uid := os.Getuid()
	return fmt.Sprintf("/tmp/tmux-%d/default", uid)
}

func (s *TmuxService) ListSessions() ([]Session, error) {
	out, err := exec.Command(getTmuxBin(), tmuxArgs("list-sessions", "-F", "#{session_name} #{session_attached}")...).CombinedOutput()
	if err != nil {
		s := string(out)
		if strings.Contains(s, "no server running") || strings.Contains(s, "no sessions") || strings.Contains(s, "No such file") || strings.Contains(s, "connect failed") {
			return []Session{}, nil
		}
		return nil, fmt.Errorf("tmux list-sessions: %s: %w", strings.TrimSpace(s), err)
	}

	var sessions []Session
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		name := parts[0]
		active := len(parts) > 1 && parts[1] == "1"
		sessions = append(sessions, Session{Name: name, Active: active})
	}
	return sessions, nil
}

func (s *TmuxService) CreateSession(name, workingDir string) error {
	args := tmuxArgs("new-session", "-d", "-s", name)
	if workingDir != "" {
		args = append(args, "-c", workingDir)
	}
	if _, err := exec.Command(getTmuxBin(), args...).CombinedOutput(); err != nil {
		return err
	}
	// Use latest client size so the active view always fills its container.
	exec.Command(getTmuxBin(), tmuxArgs("set-option", "-t", name, "window-size", "latest")...).Run()
	exec.Command(getTmuxBin(), tmuxArgs("set-option", "-t", name, "aggressive-resize", "on")...).Run()
	return nil
}

func (s *TmuxService) KillSession(name string) error {
	_, err := exec.Command(getTmuxBin(), tmuxArgs("kill-session", "-t", name)...).CombinedOutput()
	return err
}

// CapturePane returns the current visible content of a tmux session's pane.
func (s *TmuxService) CapturePane(name string) (string, error) {
	out, err := exec.Command(getTmuxBin(), tmuxArgs("capture-pane", "-t", name, "-p")...).Output()
	if err != nil {
		return "", fmt.Errorf("capture-pane %q: %w", name, err)
	}
	return string(out), nil
}
