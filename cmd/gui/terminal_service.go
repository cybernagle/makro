package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"
)

// terminalProcess tracks a PTY-backed tmux client used by the xterm.js
// WebSocket handler.
type terminalProcess struct {
	ptmx *os.File
	pid  int
}

// detachStaleClients removes orphaned GUI PTY clients for a session.
// These accumulate when Makro restarts without cleaning up PTY processes.
// We identify stale clients by their small width (GUI PTY starts at 97 cols before clamp).
func detachStaleClients(sessionName string) {
	args := tmuxArgs("list-clients", "-t", sessionName, "-F", "#{client_tty}:#{client_width}")
	out, err := exec.Command(getTmuxBin(), args...).Output()
	if err != nil {
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		tty, wStr := parts[0], parts[1]
		var width int
		fmt.Sscanf(wStr, "%d", &width)
		// Detach clients smaller than 150 cols — these are stale GUI PTYs or xterm.js instances.
		if width > 0 && width < 150 {
			exec.Command(getTmuxBin(), tmuxArgs("detach-client", "-t", tty)...).Run()
		}
	}
}

func setWinsize(f *os.File, cols, rows int) error {
	ws := winsize{Rows: uint16(rows), Cols: uint16(cols)}
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&ws)))
	if e != 0 {
		return e
	}
	return nil
}

type winsize struct {
	Rows uint16
	Cols uint16
	X    uint16
	Y    uint16
}

// ensureTerm returns env with TERM forced to xterm-256color when missing or
// set to a value tmux refuses (empty / "dumb"). It also ensures LANG / LC_ALL
// pick a UTF-8 locale so tmux + downstream programs render CJK correctly.
func ensureTerm(env []string) []string {
	out := make([]string, 0, len(env)+2)
	hasGood := false
	hasLang := false
	hasLcAll := false
	for _, kv := range env {
		if strings.HasPrefix(kv, "TERM=") {
			val := strings.TrimPrefix(kv, "TERM=")
			if val != "" && val != "dumb" {
				hasGood = true
				out = append(out, kv)
			}
			continue
		}
		if strings.HasPrefix(kv, "LANG=") {
			hasLang = true
		}
		if strings.HasPrefix(kv, "LC_ALL=") {
			hasLcAll = true
		}
		out = append(out, kv)
	}
	if !hasGood {
		out = append(out, "TERM=xterm-256color")
	}
	if !hasLang && !hasLcAll {
		out = append(out, "LANG=en_US.UTF-8")
	}
	return out
}
