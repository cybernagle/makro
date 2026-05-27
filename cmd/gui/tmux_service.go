package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

func tmuxArgs(extra ...string) []string {
	home, _ := os.UserHomeDir()
	sock := filepath.Join(home, ".makro", "tmux.sock")
	if _, err := os.Stat(sock); err == nil {
		return append([]string{"-S", sock}, extra...)
	}
	return extra
}

func (s *TmuxService) ListSessions() ([]Session, error) {
	out, err := exec.Command(getTmuxBin(), tmuxArgs("list-sessions", "-F", "#{session_name} #{session_attached}")...).CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "no server running") || strings.Contains(string(out), "no sessions") {
			return []Session{}, nil
		}
		return nil, fmt.Errorf("tmux list-sessions: %s: %w", strings.TrimSpace(string(out)), err)
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
