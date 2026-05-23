package main

import (
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
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

func (t *TerminalService) AttachSession(sessionName string) error {
	t.mu.Lock()
	if _, exists := t.terminals[sessionName]; exists {
		t.mu.Unlock()
		return fmt.Errorf("already attached to session %q", sessionName)
	}
	t.mu.Unlock()

	home, _ := os.UserHomeDir()
	sockPath := filepath.Join(home, ".fingersaver", "tmux.sock")
	tmuxArgs := []string{"attach", "-t", sessionName}
	if _, err := os.Stat(sockPath); err == nil {
		tmuxArgs = append([]string{"-S", sockPath}, tmuxArgs...)
	}

	cmd := exec.Command(tmuxBin, tmuxArgs...)
	var env []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "TMUX") {
			continue
		}
		env = append(env, e)
	}
	cmd.Env = append(env, "TERM=xterm-256color")

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("start tmux attach: %w", err)
	}

	tp := &terminalProcess{ptmx: ptmx, pid: cmd.Process.Pid}

	t.mu.Lock()
	t.terminals[sessionName] = tp
	t.mu.Unlock()

	go func() {
		buf := make([]byte, 8192)
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
