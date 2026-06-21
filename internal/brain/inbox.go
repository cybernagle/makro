package brain

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO) — already a makro dep
)

// InboxStore is the brain's local proposal cache. It mirrors internal/usage's
// Store pattern verbatim (WAL, MaxOpenConns(1), best-effort writes).
//
// IMPORTANT (BRAIN_DESIGN §6.3 / RECONCILE §4.2): this is a UI/push CACHE, not
// the source of truth. memory-cli is the truth — proposal status there
// (category=proposals, metadata.status) wins. This store exists so the brain
// can: (a) assign short numeric IDs for /inbox commands, (b) count daily
// proposals for the rate limiter, (c) avoid re-pushing recently-pushed domains.
// On any disagreement, PATCH memory first; this store follows.
type InboxStore struct {
	db *sql.DB
}

// InboxProposal is one row of the local cache. The MemoryID links back to the
// truth-source row in memory-cli (UUID).
type InboxProposal struct {
	ID         int64  // local autoincrement — the short ID for /inbox commands
	MemoryID   string // memory-cli UUID (truth source)
	Title      string
	Body       string
	Confidence float64
	Domain     string
	Status     string // open | accepted | rejected | expired
	CreatedAt  time.Time
}

const inboxSchema = `
CREATE TABLE IF NOT EXISTS proposals (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    memory_id TEXT NOT NULL,
    title TEXT NOT NULL,
    body TEXT NOT NULL,
    confidence REAL NOT NULL DEFAULT 0,
    domain TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'open',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_proposals_status ON proposals(status);
CREATE INDEX IF NOT EXISTS idx_proposals_domain ON proposals(domain);
CREATE INDEX IF NOT EXISTS idx_proposals_created ON proposals(created_at);

CREATE TABLE IF NOT EXISTS brain_state (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
`

// Open creates or opens the inbox database at path, creating the schema.
// Mirrors usage.Open: mkdir parent, WAL DSN, MaxOpenConns(1), exec schema.
func Open(path string) (*InboxStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("brain inbox: mkdir: %w", err)
	}
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("brain inbox: open: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite serializes writes; one conn avoids lock churn.
	if _, err := db.Exec(inboxSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("brain inbox: schema: %w", err)
	}
	return &InboxStore{db: db}, nil
}

// Close closes the underlying connection. Nil-safe via the *sql.DB guard.
func (s *InboxStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Add inserts a new proposal and returns its local ID. Best-effort: a DB error
// is logged and returns (0, err) — the caller decides whether to proceed
// (pushing without a local ID is allowed; memory is the truth source).
func (s *InboxStore) Add(ctx context.Context, memoryID, title, body, domain string, confidence float64) (int64, error) {
	if s == nil {
		return 0, fmt.Errorf("brain inbox: nil store")
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO proposals (memory_id, title, body, confidence, domain, status)
		 VALUES (?, ?, ?, ?, ?, 'open')`,
		memoryID, title, body, confidence, domain)
	if err != nil {
		log.Printf("[brain] inbox Add error: %v", err)
		return 0, fmt.Errorf("brain inbox: add: %w", err)
	}
	return res.LastInsertId()
}

// MarkStatus updates a proposal's status by local ID. Used by the feedback
// loop (accept/reject) to keep the cache in sync with memory's PATCH.
func (s *InboxStore) MarkStatus(ctx context.Context, id int64, status string) error {
	if s == nil {
		return fmt.Errorf("brain inbox: nil store")
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE proposals SET status = ? WHERE id = ?`, status, id)
	if err != nil {
		log.Printf("[brain] inbox MarkStatus error: %v", err)
		return fmt.Errorf("brain inbox: mark status: %w", err)
	}
	return nil
}

// Get returns one proposal by local ID. Used by /inbox accept|reject to look
// up the memory_id before PATCHing memory.
func (s *InboxStore) Get(ctx context.Context, id int64) (*InboxProposal, error) {
	if s == nil {
		return nil, fmt.Errorf("brain inbox: nil store")
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT id, memory_id, title, body, confidence, domain, status, created_at
		 FROM proposals WHERE id = ?`, id)
	var p InboxProposal
	if err := row.Scan(&p.ID, &p.MemoryID, &p.Title, &p.Body, &p.Confidence, &p.Domain, &p.Status, &p.CreatedAt); err != nil {
		return nil, fmt.Errorf("brain inbox: get %d: %w", id, err)
	}
	return &p, nil
}

// ListOpen returns all open proposals, newest first. Powers the /inbox command.
func (s *InboxStore) ListOpen(ctx context.Context) ([]InboxProposal, error) {
	if s == nil {
		return nil, nil
	}
	return s.query(ctx, `SELECT id, memory_id, title, body, confidence, domain, status, created_at
		FROM proposals WHERE status = 'open' ORDER BY created_at DESC`)
}

// RecentDomains returns domains that received a proposal in the last `days`,
// for dedup (don't re-push the same domain too soon). Returns domain→count.
func (s *InboxStore) RecentDomains(ctx context.Context, days int) (map[string]int, error) {
	if s == nil {
		return map[string]int{}, nil
	}
	since := time.Now().AddDate(0, 0, -days)
	rows, err := s.db.QueryContext(ctx,
		`SELECT domain, COUNT(*) FROM proposals
		 WHERE created_at >= ? AND domain != '' GROUP BY domain`, since)
	if err != nil {
		return nil, fmt.Errorf("brain inbox: recent domains: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var d string
		var n int
		if err := rows.Scan(&d, &n); err == nil {
			out[d] = n
		}
	}
	return out, nil
}

// CountTodayProposals returns how many proposals were added today (local time),
// for the daily-cap rate limiter.
func (s *InboxStore) CountTodayProposals(ctx context.Context) (int, error) {
	if s == nil {
		return 0, nil
	}
	today := time.Now().Format("2006-01-02")
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM proposals WHERE date(created_at) = date(?)`, today).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("brain inbox: count today: %w", err)
	}
	return n, nil
}

// query is the shared row-scan helper for ListOpen / ListAll.
func (s *InboxStore) query(ctx context.Context, q string, args ...any) ([]InboxProposal, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("brain inbox: query: %w", err)
	}
	defer rows.Close()
	var out []InboxProposal
	for rows.Next() {
		var p InboxProposal
		if err := rows.Scan(&p.ID, &p.MemoryID, &p.Title, &p.Body, &p.Confidence, &p.Domain, &p.Status, &p.CreatedAt); err != nil {
			log.Printf("[brain] inbox scan error: %v", err)
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// ── brain_state key/value (for future use: self-tuning knobs, cursors) ──

// StateGet reads a key from brain_state. Returns "" if missing.
func (s *InboxStore) StateGet(ctx context.Context, key string) (string, error) {
	if s == nil {
		return "", nil
	}
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM brain_state WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

// StateSet upserts a key/value into brain_state.
func (s *InboxStore) StateSet(ctx context.Context, key, value string) error {
	if s == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO brain_state (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// FormatProposalForChat renders a proposal as the text shown in the chat system
// message. Short, with the local ID for /inbox commands.
func FormatProposalForChat(p InboxProposal) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🧠 [#%d] %s (confidence %.0f%%, domain: %s)\n", p.ID, p.Title, p.Confidence*100, p.Domain)
	b.WriteString(p.Body)
	b.WriteString("\n\n")
	b.WriteString("/inbox accept ")
	fmt.Fprintf(&b, "%d   /inbox reject %d [原因]\n", p.ID, p.ID)
	return b.String()
}
