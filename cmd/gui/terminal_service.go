package main

import (
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
)

type terminalProcess struct {
	masterFd int
	pid      int
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

func (t *TerminalService) Startup(ctx application.Context) {
	// Service lifecycle - nothing to do
}

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

	var masterFd, slaveFd int
	if err := openpty(&masterFd, &slaveFd); err != nil {
		return fmt.Errorf("openpty: %w", err)
	}

	setWinsize(masterFd, 80, 24)

	cmd := exec.Command(tmuxBin, tmuxArgs...)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	cmd.Stdin = os.NewFile(uintptr(slaveFd), "stdin")
	cmd.Stdout = os.NewFile(uintptr(slaveFd), "stdout")
	cmd.Stderr = os.NewFile(uintptr(slaveFd), "stderr")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		syscall.Close(masterFd)
		return fmt.Errorf("start tmux attach: %w", err)
	}

	tp := &terminalProcess{masterFd: masterFd, pid: cmd.Process.Pid}

	t.mu.Lock()
	t.terminals[sessionName] = tp
	t.mu.Unlock()

	// Read loop: masterFd → Wails events
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := syscall.Read(masterFd, buf)
			if n > 0 {
				encoded := base64.StdEncoding.EncodeToString(buf[:n])
				t.app.Event.Emit("terminal:"+sessionName, encoded)
			}
			if err != nil {
				log.Printf("[terminal] read error for %s: %v", sessionName, err)
				t.mu.Lock()
				delete(t.terminals, sessionName)
				t.mu.Unlock()
				syscall.Close(masterFd)
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

	_, err = syscall.Write(tp.masterFd, decoded)
	return err
}

func (t *TerminalService) ResizeTerminal(sessionName string, cols, rows int) error {
	t.mu.Lock()
	tp, ok := t.terminals[sessionName]
	t.mu.Unlock()
	if !ok {
		return fmt.Errorf("not attached to session %q", sessionName)
	}
	return setWinsize(tp.masterFd, cols, rows)
}

func (t *TerminalService) DetachSession(sessionName string) error {
	t.mu.Lock()
	tp, ok := t.terminals[sessionName]
	delete(t.terminals, sessionName)
	t.mu.Unlock()
	if !ok {
		return nil
	}
	syscall.Close(tp.masterFd)
	syscall.Kill(tp.pid, syscall.SIGTERM)
	return nil
}

// openpty creates a pseudo-terminal pair using posix_openpt.
func openpty(master, slave *int) error {
	m, err := syscall.Open("/dev/ptmx", syscall.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return err
	}

	var n uint32
	if err := ioctl(m, syscall.TIOCPTYGRANT, uintptr(unsafe.Pointer(&n))); err != nil {
		syscall.Close(m)
		return err
	}
	if err := ioctl(m, syscall.TIOCPTYUNLK, uintptr(unsafe.Pointer(&n))); err != nil {
		syscall.Close(m)
		return err
	}

	sname := ""
	var sn [256]byte
	if err := ioctl(m, syscall.TIOCPTYGNAME, uintptr(unsafe.Pointer(&sn))); err != nil {
		syscall.Close(m)
		return err
	}
	for i, c := range sn {
		if c == 0 {
			sname = string(sn[:i])
			break
		}
	}

	s, err := syscall.Open(sname, syscall.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		syscall.Close(m)
		return err
	}

	*master = m
	*slave = s
	return nil
}

func ioctl(fd int, req uintptr, arg uintptr) error {
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, arg)
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

func setWinsize(fd int, cols, rows int) error {
	ws := winsize{Rows: uint16(rows), Cols: uint16(cols)}
	return ioctl(fd, syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&ws)))
}
