package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

// ── Session (PTY-backed tmux client) ──

type ptySession struct {
	Name string
	cmd  *exec.Cmd
	ptmx *os.File
	once sync.Once
	done chan struct{}
}

func startPtySession(name string, cols, rows int) (*ptySession, error) {
	if name == "" {
		return nil, fmt.Errorf("session name required")
	}
	if cols <= 0 {
		cols = 200
	}
	if rows <= 0 {
		rows = 50
	}
	detachStaleClients(name)
	args := tmuxArgs("new-session", "-A", "-s", name,
		"-x", fmt.Sprintf("%d", cols),
		"-y", fmt.Sprintf("%d", rows))
	cmd := exec.Command(getTmuxBin(), args...)
	cmd.Env = ensureTerm(os.Environ())
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	if err != nil {
		return nil, fmt.Errorf("start tmux pty: %w", err)
	}
	return &ptySession{Name: name, cmd: cmd, ptmx: ptmx, done: make(chan struct{})}, nil
}

func (s *ptySession) Close() error {
	var err error
	s.once.Do(func() {
		close(s.done)
		err = s.ptmx.Close()
		if s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		_ = s.cmd.Wait()
	})
	return err
}

func (s *ptySession) Done() <-chan struct{} { return s.done }

// ── Chat event hub ──

type chatEvent struct {
	Type string `json:"type"`
	Data string `json:"data"`
}

type chatHub struct {
	mu      sync.Mutex
	subs    []chan chatEvent
	history []chatEvent
}

func newChatHub() *chatHub { return &chatHub{} }

func (h *chatHub) Subscribe() chan chatEvent {
	ch := make(chan chatEvent, 32)
	h.mu.Lock()
	h.subs = append(h.subs, ch)
	h.mu.Unlock()
	return ch
}

func (h *chatHub) Unsubscribe(ch chan chatEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i, s := range h.subs {
		if s == ch {
			h.subs = append(h.subs[:i], h.subs[i+1:]...)
			return
		}
	}
}

func (h *chatHub) Emit(typ, data string) {
	evt := chatEvent{Type: typ, Data: data}
	h.mu.Lock()
	h.history = append(h.history, evt)
	subs := make([]chan chatEvent, len(h.subs))
	copy(subs, h.subs)
	h.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- evt:
		default:
		}
	}
}

// ── Server ──

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func serve(addr string) error {
	hub := newChatHub()
	chatSvc := NewChatService(nil)
	chatSvc.hub = hub

	mux := http.NewServeMux()

	// CORS middleware for dev.
	handler := corsMiddleware(mux)

	// ── API routes ──
	mux.HandleFunc("/api/sessions", sessionsHandler)
	mux.HandleFunc("/api/sessions/", sessionHandler)
	mux.HandleFunc("/api/chat", chatHandler(chatSvc, hub))
	mux.HandleFunc("/api/chat/history", chatHistoryHandler(chatSvc))
	mux.HandleFunc("/ws/xterm/", xtermWSHandler)
	mux.HandleFunc("/ws/chat", chatWSHandler(hub))
	mux.HandleFunc("/api/chat/cancel", chatCancelHandler(chatSvc))

	// Task API
	taskStore, err := NewTaskStore()
	if err != nil {
		log.Printf("[server] task store init: %v", err)
	}
	mux.HandleFunc("/api/tasks", tasksHandler(taskStore))
	mux.HandleFunc("/api/tasks/", taskRouteHandler(taskStore))

	// Serve frontend static files (for Electron mode).
	// Look for frontend/dist relative to the binary, then fall back to working dir.
	staticDir := findStaticDir()
	if staticDir != "" {
		fileSrv := http.FileServer(http.Dir(staticDir))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
			// Serve static files; fall back to index.html for SPA routing.
			fpath := filepath.Join(staticDir, filepath.Clean(r.URL.Path))
			if _, err := os.Stat(fpath); err != nil {
				http.ServeFile(w, r, filepath.Join(staticDir, "index.html"))
				return
			}
			fileSrv.ServeHTTP(w, r)
		})
	}

	log.Printf("[server] listening on %s (static: %s)", addr, staticDir)
	srv := &http.Server{Addr: addr, Handler: handler, ReadTimeout: 0, WriteTimeout: 0}
	return srv.ListenAndServe()
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ── Session list/create ──

func sessionsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		svc := &TmuxService{}
		sessions, err := svc.ListSessions()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sessions)
	case "POST":
		var body struct {
			Name       string `json:"name"`
			WorkingDir string `json:"workingDir"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		svc := &TmuxService{}
		if err := svc.CreateSession(body.Name, body.WorkingDir); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ── Session kill ──

func sessionHandler(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	name = strings.TrimSuffix(name, "/")
	if name == "" {
		http.Error(w, "missing session name", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case "DELETE":
		svc := &TmuxService{}
		if err := svc.KillSession(name); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ── Chat send ──

func chatHandler(chatSvc *ChatService, hub *chatHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		if body.Text == "" {
			http.Error(w, "empty text", http.StatusBadRequest)
			return
		}
		go chatSvc.SendMessage(body.Text)
		w.WriteHeader(http.StatusAccepted)
	}
}

func chatCancelHandler(chatSvc *ChatService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		chatSvc.Cancel()
		w.WriteHeader(http.StatusNoContent)
	}
}

func chatHistoryHandler(chatSvc *ChatService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		msgs := chatSvc.LoadChatHistory()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(msgs)
	}
}

// ── WebSocket: Terminal (sin-golang pattern) ──

func xtermWSHandler(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/ws/xterm/")
	name = strings.TrimSuffix(name, "/")
	if name == "" {
		http.Error(w, "missing session name", http.StatusBadRequest)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	cols := atoiDefault(r.URL.Query().Get("cols"), 200)
	rows := atoiDefault(r.URL.Query().Get("rows"), 50)

	sess, err := startPtySession(name, cols, rows)
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"error","message":%q}`, err.Error())))
		return
	}
	defer sess.Close()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// PTY → WebSocket (binary frames)
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, rerr := sess.ptmx.Read(buf)
			if n > 0 {
				if werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					cancel()
					return
				}
			}
			if rerr != nil {
				cancel()
				return
			}
			select {
			case <-ctx.Done():
				return
			default:
			}
		}
	}()

	// WebSocket → PTY (binary=stdin, text=JSON control)
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		switch mt {
		case websocket.BinaryMessage:
			if _, werr := sess.ptmx.Write(data); werr != nil {
				return
			}
		case websocket.TextMessage:
			var msg struct {
				Type string `json:"type"`
				Cols int    `json:"cols"`
				Rows int    `json:"rows"`
			}
			if err := json.Unmarshal(data, &msg); err != nil {
				continue
			}
			switch msg.Type {
			case "resize":
				if msg.Cols > 0 && msg.Rows > 0 {
					setWinsize(sess.ptmx, msg.Cols, msg.Rows)
				}
			}
		}
	}
}

// ── WebSocket: Chat events ──

func chatWSHandler(hub *chatHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		ch := hub.Subscribe()
		defer hub.Unsubscribe(ch)

		// Send history first.
		hub.mu.Lock()
		for _, evt := range hub.history {
			data, _ := json.Marshal(evt)
			conn.WriteMessage(websocket.TextMessage, data)
		}
		hub.mu.Unlock()

		// Stream new events.
		for {
			select {
			case evt, ok := <-ch:
				if !ok {
					return
				}
				data, _ := json.Marshal(evt)
				if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
					return
				}
			case <-time.After(30 * time.Second):
				// Ping to keep connection alive.
				if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"ping"}`)); err != nil {
					return
				}
			}
		}
	}
}

// ── Task API ──

func tasksHandler(store *TaskStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			tasks, err := store.Load()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(tasks)
		case "POST":
			var body struct {
				Title   string `json:"title"`
				Content string `json:"content"`
				Column  string `json:"column"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "invalid json", http.StatusBadRequest)
				return
			}
			if body.Column == "" {
				body.Column = "todo"
			}
			t, err := store.Create(body.Title, body.Content, body.Column)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(t)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func taskRouteHandler(store *TaskStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// /api/tasks/:id or /api/tasks/:id/send
		path := strings.TrimPrefix(r.URL.Path, "/api/tasks/")
		path = strings.TrimSuffix(path, "/")
		if path == "" {
			http.Error(w, "missing task id", http.StatusBadRequest)
			return
		}

		// Check for /send sub-route
		if strings.HasSuffix(path, "/send") {
			id := strings.TrimSuffix(path, "/send")
			if r.Method != "POST" {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			handleTaskSend(store, w, r, id)
			return
		}

		id := path
		switch r.Method {
		case "PUT":
			var patch map[string]any
			if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
				http.Error(w, "invalid json", http.StatusBadRequest)
				return
			}
			t, err := store.Update(id, patch)
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(t)
		case "DELETE":
			if err := store.Delete(id); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func handleTaskSend(store *TaskStore, w http.ResponseWriter, r *http.Request, id string) {
	var body struct {
		Session string `json:"session"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if body.Session == "" {
		http.Error(w, "missing session", http.StatusBadRequest)
		return
	}
	task, err := store.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err := sendToTmuxSession(body.Session, task.Content); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Mark as assigned and move to in-progress
	session := body.Session
	store.Update(id, map[string]any{"assigned_session": session, "column": "in-progress"})
	w.WriteHeader(http.StatusNoContent)
}

func sendToTmuxSession(session, text string) error {
	if len(text) <= 10 {
		if err := tmuxSendKeys(session, text); err != nil {
			return err
		}
		return tmuxSendEnter(session)
	}
	payload := fmt.Sprintf("\033[200~%s\033[201~\r", text)
	return tmuxSendKeys(session, payload)
}

func tmuxSendKeys(session, text string) error {
	return exec.Command(getTmuxBin(), tmuxArgs("send-keys", "-t", session, "-l", text)...).Run()
}

func tmuxSendEnter(session string) error {
	return exec.Command(getTmuxBin(), tmuxArgs("send-keys", "-t", session, "Enter")...).Run()
}

// ── Helpers ──

func findStaticDir() string {
	// 1. Next to the running binary (Electron packaged app: Resources/bin/)
	if exe, err := os.Executable(); err == nil {
		d := filepath.Join(filepath.Dir(exe), "..", "frontend", "dist")
		if isDir(d) {
			return d
		}
	}
	// 2. Relative to working dir (dev mode)
	candidates := []string{"frontend/dist", "../frontend/dist", "cmd/gui/frontend/dist"}
	for _, c := range candidates {
		if isDir(c) {
			abs, _ := filepath.Abs(c)
			return abs
		}
	}
	return ""
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

var _ = fs.ReadDir // ensure fs import used

func atoiDefault(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
