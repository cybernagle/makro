package main

import (
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"github.com/creack/pty"
	"github.com/wailsapp/wails/v3/pkg/application"
)

type terminalProcess struct {
	ptmx *os.File
	pid  int
}

type TerminalService struct {
	app       *application.App
	mu        sync.Mutex
	terminals map[string]*terminalProcess
}

func NewTerminalService() *TerminalService {
	return &TerminalService{
		terminals: make(map[string]*terminalProcess),
	}
}

func (t *TerminalService) SetApp(app *application.App) {
	t.app = app
}

func (t *TerminalService) Startup(ctx application.Context) {}

func (t *TerminalService) AttachSession(sessionName string, cols, rows int) error {
	t.mu.Lock()
	if _, exists := t.terminals[sessionName]; exists {
		t.mu.Unlock()
		return fmt.Errorf("already attached to session %q", sessionName)
	}
	t.mu.Unlock()

	if cols <= 0 {
		cols = 200
	}
	if rows <= 0 {
		rows = 50
	}

	// Detach stale clients for this session (leftover from previous Makro runs).
	detachStaleClients(sessionName)

	// tmux new-session -A: attach if exists, create if not. Initial size via -x/-y.
	args := tmuxArgs("new-session", "-A", "-s", sessionName,
		"-x", fmt.Sprintf("%d", cols),
		"-y", fmt.Sprintf("%d", rows))

	cmd := exec.Command(getTmuxBin(), args...)
	cmd.Env = ensureTerm(os.Environ())

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	if err != nil {
		return fmt.Errorf("start tmux pty: %w", err)
	}

	tp := &terminalProcess{ptmx: ptmx, pid: cmd.Process.Pid}

	t.mu.Lock()
	t.terminals[sessionName] = tp
	t.mu.Unlock()

	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				encoded := base64.StdEncoding.EncodeToString(buf[:n])
				t.app.Event.Emit("terminal:"+sessionName, encoded)
			}
			if err != nil {
				if err != io.EOF {
					log.Printf("[terminal] read error for %s: %v", sessionName, err)
				}
				t.mu.Lock()
				delete(t.terminals, sessionName)
				t.mu.Unlock()
				ptmx.Close()
				t.app.Event.Emit("terminal:exit:"+sessionName, "")
				return
			}
		}
	}()

	return nil
}

func (t *TerminalService) WriteInput(sessionName, data string) error {
	t.mu.Lock()
	tp, ok := t.terminals[sessionName]
	t.mu.Unlock()
	if !ok {
		return fmt.Errorf("not attached to session %q", sessionName)
	}

	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return fmt.Errorf("base64 decode: %w", err)
	}

	_, err = tp.ptmx.Write(decoded)
	return err
}

func (t *TerminalService) ResizeTerminal(sessionName string, cols, rows int) error {
	t.mu.Lock()
	tp, ok := t.terminals[sessionName]
	t.mu.Unlock()
	if !ok {
		return fmt.Errorf("not attached to session %q", sessionName)
	}
	return setWinsize(tp.ptmx, cols, rows)
}

func (t *TerminalService) DetachSession(sessionName string) error {
	t.mu.Lock()
	tp, ok := t.terminals[sessionName]
	delete(t.terminals, sessionName)
	t.mu.Unlock()
	if !ok {
		return nil
	}
	tp.ptmx.Close()
	syscall.Kill(tp.pid, syscall.SIGTERM)
	return nil
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
