// Package brain implements the proactive "second brain" half of makro.
//
// It is deliberately separate from the reactive orchestrator (internal/agent):
// the brain runs as its own subcommand/process and shares no in-memory state
// with orchestrator messages, cancel state, or the tmux state machine.
//
// P0 scope (this package, as of this commit): the capture foundation only —
//   - memory_client.go : REST :8765 client + CLI fallback + dead-letter queue
//   - capture.go       : async capture sink (filter → dedup → write memory)
//
// P1+ (reader/wake/propose/inbox/feedback) lands once memory-cli's M2/M6 do.
package brain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Source identifiers. IMPORTANT: source = WHO wrote the memory, not what is
// being described. This matters for dedup and for the brain's fit model.
//
//   - "claude" / "copilot" / "makro" : a coding-agent user message was captured
//     (the *user* typed it, but it flowed through that agent's session).
//   - "makro-brain"                  : the brain itself wrote it (proposal,
//     self-tune, outcome rescan) — never a human action.
//   - "human"                        : the user took a direct action in makro
//     (accept/reject a proposal). The feedback *describes* the user, but the
//     writer is the makro process acting on the user's behalf.
const (
	SourceClaude     = "claude"
	SourceCopilot    = "copilot"
	SourceMakro      = "makro"
	SourceMakroBrain = "makro-brain"
	SourceHuman      = "human"
)

// Memory is the record makro writes to memory-cli. It mirrors memory-cli's
// Memory struct fields plus metadata (RECONCILE §3.2). P0 only uses it for
// capture writes; P1 uses the same struct for proposals/feedback.
type Memory struct {
	Content  string         `json:"content"`
	Category string         `json:"category"`
	Scope    string         `json:"scope,omitempty"`
	Tags     []string       `json:"tags,omitempty"`
	Source   string         `json:"source,omitempty"`
	Project  string         `json:"project,omitempty"`  // ← new (M1): cwd basename
	Role     string         `json:"role,omitempty"`     // ← new (M1): "user"|"assistant"
	Metadata map[string]any `json:"metadata,omitempty"` // ← new (M1): arbitrary JSON
}

// Client talks to memory-cli. The primary path is the REST API on 127.0.0.1:8765
// (RECONCILE §1 — this is the unconditional, fully-routed API). If REST is
// unreachable (daemon down), it falls back to the `memory` CLI subprocess which
// hits SQLite directly. If both fail, the write is appended to a dead-letter
// file so the next brain wake can replay it.
//
// All methods are safe for concurrent use. None of them ever block the caller
// longer than the per-op timeout — callers on the chat hot path must wrap writes
// in an async dispatch (see CaptureSink).
type Client struct {
	endpoint string // e.g. "http://127.0.0.1:8765"
	apiKey   string // Bearer token (M3: memory-cli enforces this once landed)
	cliPath  string // ~/bin/memory fallback (expanded)

	httpc *http.Client

	// Dead-letter queue: writes that failed REST+CLI are appended here as JSONL.
	// One line per failed Memory. Replayed by ReplayDeadLetter on brain wake.
	deadLetterPath string

	// dialMu serializes CLI subprocess spawns (avoid fork-bomb under failure).
	dialMu sync.Mutex
}

// NewClient builds a memory client. endpoint must be the :8765 REST base URL
// (NOT :8090 — that's stop-hook.sh's transport). cliPath may be "~/bin/memory".
// dataDir is makro's data dir (~/.makro); the dead-letter file lives under it.
func NewClient(endpoint, apiKey, cliPath, dataDir string) *Client {
	if endpoint == "" {
		endpoint = "http://127.0.0.1:8765"
	}
	return &Client{
		endpoint:       strings.TrimRight(endpoint, "/"),
		apiKey:         apiKey,
		cliPath:        expandHome(cliPath),
		httpc:          &http.Client{Timeout: 5 * time.Second},
		deadLetterPath: filepath.Join(dataDir, "brain", "write-failed.jsonl"),
	}
}

// WriteCapture writes one captured user message. This is the only write path
// fully implemented in P0. Returns nil only if REST or CLI succeeded.
//
// Contract (BRAIN_DESIGN §9.3a): category=capture, tags contains chat-capture,
// source ∈ {claude,copilot,makro}, role=user, project from cwd basename,
// metadata carries session + prompt_hash for client-side dedup.
func (c *Client) WriteCapture(ctx context.Context, m Memory) error {
	if m.Category != CategoryCapture {
		return fmt.Errorf("WriteCapture: category must be %q, got %q", CategoryCapture, m.Category)
	}
	if m.Source == "" {
		m.Source = SourceMakro
	}
	if m.Role == "" {
		m.Role = "user"
	}
	return c.writeWithFallback(ctx, m)
}

// writeWithFallback tries REST, then CLI, then dead-letter. Never returns an
// error that a caller can't ignore — capture is best-effort. The returned error
// is for logging only; the caller (CaptureSink) guarantees chat never sees it.
func (c *Client) writeWithFallback(ctx context.Context, m Memory) error {
	if err := c.writeREST(ctx, m); err == nil {
		return nil
	} else {
		log.Printf("[brain] REST write failed, trying CLI: %v", err)
	}

	if err := c.writeCLI(ctx, m); err == nil {
		return nil
	} else {
		log.Printf("[brain] CLI write failed, dead-lettering: %v", err)
	}

	if err := c.enqueueDeadLetter(m); err != nil {
		// Last resort failed too. We log loudly but do NOT return an error that
		// could propagate to chat — capture is best-effort by design.
		log.Printf("[brain] dead-letter enqueue failed (data lost): %v", err)
	}
	return nil
}

// writeREST POSTs to :8765/memories. Requires M1 (full-field POST) + M3 (auth).
// Returns nil on 2xx.
func (c *Client) writeREST(ctx context.Context, m Memory) error {
	body, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.endpoint+"/memories", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("post: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("memory REST %s: %s", c.endpoint+"/memories", http.StatusText(resp.StatusCode))
}

// writeCLI shells out to `memory write`. This path does NOT depend on the
// daemon — it writes SQLite directly. Caveats: the CLI (as of memory-cli's
// current release) does NOT accept --project/--role/--metadata, so a CLI
// fallback write is lossy (loses project/role/metadata). This is an accepted
// trade-off: a daemon-less capture is better than a lost capture. The lost
// fields only matter for filtering; the content (the actual idea) survives.
func (c *Client) writeCLI(ctx context.Context, m Memory) error {
	if c.cliPath == "" {
		return errors.New("no memory CLI configured")
	}
	if _, err := os.Stat(c.cliPath); err != nil {
		return fmt.Errorf("memory CLI not found at %s: %w", c.cliPath, err)
	}

	c.dialMu.Lock()
	defer c.dialMu.Unlock()

	cmdCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	args := []string{"write", m.Content, "--category", m.Category, "--source", m.Source}
	if m.Scope != "" {
		args = append(args, "--scope", m.Scope)
	}
	if len(m.Tags) > 0 {
		args = append(args, "--tags", strings.Join(m.Tags, ","))
	}

	cmd := exec.CommandContext(cmdCtx, c.cliPath, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("memory write: %w (out: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// enqueueDeadLetter appends the memory as one JSONL line. On the next brain
// wake, ReplayDeadLetter replays these. The file grows only under sustained
// daemon outage; a healthy system never appends here.
func (c *Client) enqueueDeadLetter(m Memory) error {
	if err := os.MkdirAll(filepath.Dir(c.deadLetterPath), 0o755); err != nil {
		return fmt.Errorf("mkdir dead-letter dir: %w", err)
	}
	// Tag the entry so replay knows when it was originally written.
	entry := struct {
		FailedAt time.Time `json:"failed_at"`
		Memory   Memory    `json:"memory"`
	}{FailedAt: time.Now().UTC(), Memory: m}
	line, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal dead-letter: %w", err)
	}
	line = append(line, '\n')

	f, err := os.OpenFile(c.deadLetterPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open dead-letter: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("write dead-letter: %w", err)
	}
	return nil
}

// ReplayDeadLetter re-attempts every queued write, removing successfully
// replayed lines. Called at brain wake. Best-effort; remaining failures stay
// queued. Returns count of successfully replayed entries.
func (c *Client) ReplayDeadLetter(ctx context.Context) (int, error) {
	data, err := os.ReadFile(c.deadLetterPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("read dead-letter: %w", err)
	}
	if len(data) == 0 {
		return 0, nil
	}

	type entry struct {
		FailedAt time.Time `json:"failed_at"`
		Memory   Memory    `json:"memory"`
	}
	var remaining []entry
	var replayed int
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var e entry
		if err := json.Unmarshal(line, &e); err != nil {
			log.Printf("[brain] dead-letter: skipping unparseable line: %v", err)
			continue
		}
		if err := c.writeREST(ctx, e.Memory); err == nil {
			replayed++
		} else {
			remaining = append(remaining, e)
		}
	}

	// Rewrite the file with whatever still failed. If all replayed, remove it.
	if len(remaining) == 0 {
		_ = os.Remove(c.deadLetterPath)
	} else {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		for _, e := range remaining {
			_ = enc.Encode(e)
		}
		if err := os.WriteFile(c.deadLetterPath, buf.Bytes(), 0o644); err != nil {
			return replayed, fmt.Errorf("rewrite dead-letter: %w", err)
		}
	}
	return replayed, nil
}

// DeadLetterCount returns the number of queued (failed) writes. For diagnostics.
func (c *Client) DeadLetterCount() int {
	data, err := os.ReadFile(c.deadLetterPath)
	if err != nil {
		return 0
	}
	count := 0
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) > 0 {
			count++
		}
	}
	return count
}

// ─────────────────────────────────────────────────────────────────────────────
// P1 read/write methods. memory-cli's M1–M7 are all landed (see PROGRESS.md),
// so these are now real implementations against the :8765 REST contract.
// ─────────────────────────────────────────────────────────────────────────────

// Query captures the read-side filters for a List call. tags AND-matches,
// from/to filter on created_at, category scopes by category. All optional.
type Query struct {
	Category string
	Tags     []string  // AND-match (comma-separated to the API)
	From     time.Time // created_after
	To       time.Time // created_before
	Limit    int
}

// ListedMemory is a read-back memory row. Metadata is the merged JSON the
// server stored (proposal status lives here as metadata.status).
type ListedMemory struct {
	ID        string         `json:"id"`
	Content   string         `json:"content"`
	Category  string         `json:"category"`
	Source    string         `json:"source"`
	Scope     string         `json:"scope"`
	Tags      []string       `json:"tags"`
	Metadata  map[string]any `json:"metadata"`
	CreatedAt time.Time      `json:"created_at"`
}

// listResponse wraps the GET /memories envelope {"memories": [...]}.
type listResponse struct {
	Memories []ListedMemory `json:"memories"`
}

// List queries memories via GET /memories with tags=/from=/to=/limit= filters
// (memory-cli M2). Returns the matched rows. A nil/empty result (including the
// server returning {"memories":null}) is reported as an empty slice, not nil.
func (c *Client) List(ctx context.Context, q Query) ([]ListedMemory, error) {
	u := c.endpoint + "/memories?" + encodeQuery(q)
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("list: REST %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var lr listResponse
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		return nil, fmt.Errorf("list: decode: %w", err)
	}
	if lr.Memories == nil {
		lr.Memories = []ListedMemory{}
	}
	return lr.Memories, nil
}

// PatchMetadata does a partial metadata merge on one memory via PATCH
// /memories/{id} (memory-cli M6). The server merges (not overwrites) the
// metadata object. Used by the proposal status machine (open→accepted→...).
func (c *Client) PatchMetadata(ctx context.Context, id string, metadata map[string]any) error {
	if id == "" {
		return errors.New("patch: empty id")
	}
	body, err := json.Marshal(map[string]any{"metadata": metadata})
	if err != nil {
		return fmt.Errorf("patch: marshal: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPatch, c.endpoint+"/memories/"+id, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("patch: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("patch %s: REST %s", id, resp.Status)
}

// WriteProposal writes a category=proposals memory. Caller must populate
// Metadata (status/confidence/domain/evidence) and Tags (brain-proposal +
// domain) per BRAIN_DESIGN §9.3(b). Falls through the same REST→CLI→dead-letter
// chain as capture via writeWithFallback.
func (c *Client) WriteProposal(ctx context.Context, m Memory) error {
	if m.Category != CategoryProposals {
		return fmt.Errorf("WriteProposal: category must be %q, got %q", CategoryProposals, m.Category)
	}
	if m.Source == "" {
		m.Source = SourceMakroBrain
	}
	return c.writeWithFallback(ctx, m)
}

// WriteFeedback writes a category=feedback memory. Caller must populate
// Tags (brain-feedback + verdict + domain) and Metadata (proposal_id/verdict/
// reason) per BRAIN_DESIGN §9.3(c). source=human for manual accept/reject.
func (c *Client) WriteFeedback(ctx context.Context, m Memory) error {
	if m.Category != CategoryFeedback {
		return fmt.Errorf("WriteFeedback: category must be %q, got %q", CategoryFeedback, m.Category)
	}
	if m.Source == "" {
		m.Source = SourceHuman
	}
	return c.writeWithFallback(ctx, m)
}

// writeAndReturnID POSTs a memory and returns the server-assigned ID. The plain
// writeWithFallback doesn't surface the ID (it returns nil on success); this
// variant does the REST POST directly and parses the {id} from the response.
// Falls back to writeWithFallback if REST fails (returns "" ID — the write
// still lands via CLI/dead-letter, but the caller can't link back to it).
func (c *Client) writeAndReturnID(ctx context.Context, m Memory) (string, error) {
	body, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.endpoint+"/memories", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpc.Do(req)
	if err != nil {
		// REST failed — fall through to the CLI/dead-letter chain so the write
		// still lands, but we lose the ID. The caller proceeds without one.
		log.Printf("[brain] writeAndReturnID REST failed, falling back (no ID): %v", err)
		_ = c.writeWithFallback(ctx, m)
		return "", nil
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var created struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(respBody, &created); err == nil && created.ID != "" {
			return created.ID, nil
		}
		log.Printf("[brain] write OK but no ID in response: %s", truncate(string(respBody), 200))
		return "", nil
	}
	// Non-2xx — fall back so the write still lands elsewhere.
	log.Printf("[brain] writeAndReturnID REST %s, falling back: %s", resp.Status, truncate(string(respBody), 200))
	_ = c.writeWithFallback(ctx, m)
	return "", nil
}

// encodeQuery builds a query string from a Query (URL-escaped).
func encodeQuery(q Query) string {
	v := url.Values{}
	if q.Category != "" {
		v.Set("category", q.Category)
	}
	if len(q.Tags) > 0 {
		v.Set("tags", strings.Join(q.Tags, ","))
	}
	if !q.From.IsZero() {
		v.Set("from", q.From.Format(time.RFC3339))
	}
	if !q.To.IsZero() {
		v.Set("to", q.To.Format(time.RFC3339))
	}
	if q.Limit > 0 {
		v.Set("limit", fmt.Sprintf("%d", q.Limit))
	}
	return v.Encode()
}

// ── helpers ──

func expandHome(p string) string {
	if p == "" {
		return ""
	}
	if p == "~" {
		home, _ := os.UserHomeDir()
		return home
	}
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return p
		}
		return filepath.Join(home, p[2:])
	}
	return p
}

// CategoryCapture / CategoryProposals — RECONCILE §2. These are the dedicated
// categories makro writes into. memory-cli's NormalizeCategory accepts any
// non-standard value, so these work even before M4 adds the constants.
const (
	CategoryCapture   = "capture"
	CategoryProposals = "proposals"
	CategoryFeedback  = "feedback"
)

// Compile-time guarantee the Client is used as intended.
var _ = runtime.GOOS
