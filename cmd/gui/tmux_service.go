package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

var tmuxBin string
var tmuxOnce sync.Once

func getTmuxBin() string {
	tmuxOnce.Do(func() {
		if p, err := exec.LookPath("tmux"); err == nil {
			tmuxBin = p
			return
		}
		for _, p := range []string{"/opt/homebrew/bin/tmux", "/usr/local/bin/tmux", "/usr/bin/tmux"} {
			if _, err := os.Stat(p); err == nil {
				tmuxBin = p
				return
			}
		}
		tmuxBin = "tmux"
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

func tmuxArgs(extra ...string) []string {
	// Use the user's default tmux socket (no -S flag) so makro integrates with
	// their daily tmux server rather than running an isolated one. The user
	// expects to see and manage their daily sessions through makro.
	return extra
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
