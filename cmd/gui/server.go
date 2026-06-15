package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
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
	if len(h.history) > 200 {
		h.history = h.history[len(h.history)-200:]
	}
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
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true // non-browser clients (iOS app, curl)
		}
		return isLocalOrigin(origin)
	},
}

func serve(addr string, tlsCert, tlsKey, password string) error {
	hub := newChatHub()
	chatSvc := NewChatService()
	chatSvc.hub = hub
	// Eager init so the notifier socket + Claude hooks are live before serving,
	// letting agent-stop notifications fire even if no chat message is sent.
	chatSvc.init()

	// Recover tmux sessions from snapshot (if tmux crashed) before serving.
	if n := RecoverFromSnapshot(); n > 0 {
		hub.Emit("system", fmt.Sprintf("Recovered %d tmux sessions from snapshot", n))
	}

	// Start periodic snapshot loop (every 5 minutes).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	StartSnapshotLoop(ctx, 5*time.Minute)

	mux := http.NewServeMux()

	var handler http.Handler = mux
	if password != "" {
		handler = authMiddleware(handler, password)
	}
	handler = corsMiddleware(handler)

	// ── API routes ──
	mux.HandleFunc("/api/sessions", sessionsHandler(chatSvc))
	mux.HandleFunc("/api/sessions/", sessionHandler(chatSvc))
	mux.HandleFunc("/api/chat", chatHandler(chatSvc, hub))
	mux.HandleFunc("/api/chat/history", chatHistoryHandler(chatSvc))
	mux.HandleFunc("/api/device-token", deviceTokenHandler(chatSvc))
	mux.HandleFunc("/api/usage/stats", usageStatsHandler(chatSvc))
	mux.HandleFunc("/api/usage/diagnostics", usageDiagnosticsHandler(chatSvc))
	mux.HandleFunc("/api/usage/timeline", usageTimelineHandler(chatSvc))
	mux.HandleFunc("/api/usage/export", usageExportHandler(chatSvc))
	mux.HandleFunc("/ws/xterm/", xtermWSHandler)
	mux.HandleFunc("/ws/snapshot/", wsSnapshotHandler)
	mux.HandleFunc("/ws/chat", chatWSHandler(hub))
	mux.HandleFunc("/api/chat/cancel", chatCancelHandler(chatSvc))
	mux.HandleFunc("/api/snapshot", snapshotHandler)
	mux.HandleFunc("/api/recover", recoveryHandler)

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

	proto := "http"
	if tlsCert != "" && tlsKey != "" {
		proto = "https"
	}
	authStatus := "no auth"
	if password != "" {
		authStatus = "password protected"
	}
	log.Printf("[server] listening on %s://%s (static: %s, %s)", proto, addr, staticDir, authStatus)
	srv := &http.Server{Addr: addr, Handler: handler, ReadTimeout: 0, WriteTimeout: 0}
	if tlsCert != "" && tlsKey != "" {
		return srv.ListenAndServeTLS(tlsCert, tlsKey)
	}
	return srv.ListenAndServe()
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			if isLocalOrigin(origin) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
			}
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isLocalOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	h := u.Hostname()
	if h == "localhost" || h == "127.0.0.1" {
		return true
	}
	ip := net.ParseIP(h)
	if ip == nil {
		return false
	}
	return ip.IsPrivate() || ip.IsLoopback()
}

func authMiddleware(next http.Handler, password string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow static files without auth
		if r.URL.Path == "/" || strings.HasPrefix(r.URL.Path, "/assets/") || strings.HasSuffix(r.URL.Path, ".js") || strings.HasSuffix(r.URL.Path, ".css") || strings.HasSuffix(r.URL.Path, ".html") || strings.HasSuffix(r.URL.Path, ".ico") {
			next.ServeHTTP(w, r)
			return
		}

		// Check Authorization: Bearer <password>
		auth := r.Header.Get("Authorization")
		if strings.HasPrefix(auth, "Bearer ") && strings.TrimPrefix(auth, "Bearer ") == password {
			next.ServeHTTP(w, r)
			return
		}

		// WebSocket connections cannot set headers during handshake;
		// accept token via query param on /ws/ paths only.
		if strings.HasPrefix(r.URL.Path, "/ws/") && r.URL.Query().Get("token") == password {
			next.ServeHTTP(w, r)
			return
		}

		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

// ── Session list/create ──

func sessionsHandler(chatSvc *ChatService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			svc := &TmuxService{}
			sessions, err := svc.ListSessions()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			chatSvc.ApplySessionState(sessions)
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
}

// ── Session kill ──

func sessionHandler(chatSvc *ChatService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
		path = strings.TrimSuffix(path, "/")
		if path == "" {
			http.Error(w, "missing session name", http.StatusBadRequest)
			return
		}

		// Sub-routes: /api/sessions/{name}/capture, /api/sessions/{name}/send
		if idx := strings.IndexByte(path, '/'); idx >= 0 {
			name := path[:idx]
			sub := path[idx+1:]
			switch sub {
			case "capture":
				sessionCaptureHandler(w, r, name)
				return
			case "send":
				sessionSendHandler(w, r, name)
				return
			case "viewed":
				sessionViewedHandler(chatSvc, w, r, name)
				return
			default:
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
		}

		// /api/sessions/{name}
		name := path
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
}

// sessionCaptureHandler returns the current visible pane content of a tmux
// session. Used by the iOS snapshot-mode terminal viewer.
func sessionCaptureHandler(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	svc := &TmuxService{}
	content, err := svc.CapturePane(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Optional: include scrollback history (?history=500 includes last 500 lines).
	if h := r.URL.Query().Get("history"); h != "" && h != "0" {
		out, err := exec.Command(getTmuxBin(), tmuxArgs("capture-pane", "-t", name, "-p", "-S", "-"+h)...).Output()
		if err == nil {
			content = string(out)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"content": content})
}

// sessionSendHandler sends text to a tmux session via send-keys. Reuses the
// existing sendToTmuxSession helper (short → send-keys -l + Enter; long →
// bracketed paste).
func sessionSendHandler(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodPost {
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
	if err := sendToTmuxSession(name, body.Text); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// sessionViewedHandler clears the unread badge for a session (POST, no body).
func sessionViewedHandler(chatSvc *ChatService, w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	chatSvc.MarkSessionViewed(name)
	w.WriteHeader(http.StatusNoContent)
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

func deviceTokenHandler(chatSvc *ChatService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			DeviceID string `json:"device_id"`
			Token    string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		if body.DeviceID == "" || body.Token == "" {
			http.Error(w, "device_id and token required", http.StatusBadRequest)
			return
		}
		chatSvc.RegisterDeviceToken(body.DeviceID, body.Token)
		w.WriteHeader(http.StatusNoContent)
	}
}

// ── Usage tracking (prompt consumption) ──

func usageStatsHandler(chatSvc *ChatService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		q := r.URL.Query()
		st, err := chatSvc.UsageStats(q.Get("session"), q.Get("source"), q.Get("model"), queryHours(r, 5))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(st)
	}
}

func usageDiagnosticsHandler(chatSvc *ChatService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		d, err := chatSvc.UsageDiagnostics(r.URL.Query().Get("session"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(d)
	}
}

func usageTimelineHandler(chatSvc *ChatService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		q := r.URL.Query()
		tl, err := chatSvc.UsageTimeline(q.Get("session"), q.Get("source"), q.Get("model"), queryHours(r, 24), queryInt(r, "granularity", 60))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tl)
	}
}

// queryHours parses an "hours" query param with a default fallback.
func queryHours(r *http.Request, def int) int {
	return queryInt(r, "hours", def)
}

// usageExportHandler streams the raw prompt_usage rows as CSV (newest first),
// scoped by the same session/source/model filter + window as the dashboard.
func usageExportHandler(chatSvc *ChatService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		q := r.URL.Query()
		rows, err := chatSvc.UsageExport(q.Get("session"), q.Get("source"), q.Get("model"), queryHours(r, 24))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="prompt_usage.csv"`)
		w.Write([]byte("\xef\xbb\xbf")) // UTF-8 BOM so Excel opens it correctly
		cw := csv.NewWriter(w)
		cw.Write([]string{"timestamp", "session", "function", "model", "prompt_tokens",
			"completion_tokens", "cache_read", "cache_creation", "total_tokens",
			"is_duplicate", "duration_ms", "error"})
		for _, row := range rows {
			cw.Write([]string{
				row.Timestamp.Format("2006-01-02 15:04:05"), row.Session, row.Function, row.Model,
				strconv.FormatInt(row.PromptTokens, 10), strconv.FormatInt(row.CompletionTokens, 10),
				strconv.FormatInt(row.CacheReadTokens, 10), strconv.FormatInt(row.CacheCreationTokens, 10),
				strconv.FormatInt(row.TotalTokens, 10), strconv.FormatBool(row.IsDuplicate),
				strconv.FormatInt(row.DurationMS, 10), row.Error,
			})
		}
		cw.Flush()
	}
}

// queryInt parses a query param as a positive int with a default fallback.
func queryInt(r *http.Request, key string, def int) int {
	if v := r.URL.Query().Get(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
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

// ── WebSocket: Terminal snapshot (iOS snapshot-mode viewer) ──

// wsSnapshotHandler pushes periodic tmux capture-pane snapshots to the client.
// Unlike xtermWSHandler (which streams raw PTY bytes), this sends JSON frames
// {content, ts} containing the current visible pane. iOS renders them as
// direct Text replacements — no client-side terminal state machine, which
// kills the cursor-residue bug.
//
// Throttled to ~10fps with content-hash dedup to avoid redundant sends.
func wsSnapshotHandler(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/ws/snapshot/")
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

	historyLines := atoiDefault(r.URL.Query().Get("history"), 0)
	interval := 100 * time.Millisecond
	if d := r.URL.Query().Get("interval"); d != "" {
		if ms, err := time.ParseDuration(d + "ms"); err == nil && ms >= 30*time.Millisecond {
			interval = ms
		}
	}

	svc := &TmuxService{}
	var lastHash uint64
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Initial snapshot immediately (don't wait one interval).
	push := func() bool {
		var content string
		var err error
		if historyLines > 0 {
			out, e := exec.Command(getTmuxBin(), tmuxArgs("capture-pane", "-t", name, "-p", "-S", "-"+strconv.Itoa(historyLines))...).Output()
			if e != nil {
				err = e
			} else {
				content = string(out)
			}
		} else {
			content, err = svc.CapturePane(name)
		}
		if err != nil {
			// Session may have died — tell client and close.
			conn.WriteMessage(websocket.TextMessage,
				[]byte(fmt.Sprintf(`{"type":"error","message":%q}`, err.Error())))
			return false
		}
		h := fnv64(content)
		if h == lastHash {
			return true // unchanged, skip
		}
		lastHash = h
		payload, _ := json.Marshal(map[string]any{
			"type":    "snapshot",
			"content": content,
			"ts":      time.Now().UnixMilli(),
		})
		if werr := conn.WriteMessage(websocket.TextMessage, payload); werr != nil {
			return false
		}
		return true
	}

	if !push() {
		return
	}

	for {
		select {
		case <-ticker.C:
			if !push() {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

// fnv64 is a fast 64-bit hash for change detection.
func fnv64(s string) uint64 {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	h := uint64(offset64)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime64
	}
	return h
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

		// Stream new events only. Clients load history via
		// GET /api/chat/history — replaying it here duplicated messages
		// on every (re)connect.
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
	text = strings.TrimRight(text, "\r\n")
	if text == "" {
		return tmuxSendEnter(session)
	}
	if len(text) <= 10 {
		// Send text and Enter atomically in a single tmux invocation
		// to avoid timing issues between two separate exec.Command calls.
		args := tmuxArgs("send-keys", "-t", session, "-l", text)
		args = append(args, ";", "send-keys", "-t", session, "Enter")
		return exec.Command(getTmuxBin(), args...).Run()
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

// snapshotHandler triggers an immediate TakeSnapshot. Useful for testing and
// for users who want to force a snapshot before a risky operation.
func snapshotHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	snap, err := TakeSnapshot()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(snap)
}

// recoveryHandler manually triggers RecoverFromSnapshot. Useful when tmux has
// been killed outside the server lifecycle.
func recoveryHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	n := RecoverFromSnapshot()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{"recovered": n})
}
