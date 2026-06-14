// Package usage persists per-call LLM token usage to a SQLite database for
// consumption tracking and optimization diagnostics.
package usage

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO)
)

// Record is one logged LLM API call.
type Record struct {
	Timestamp        time.Time
	SessionName      string // best-effort: @mention target, else "orchestrator"
	ModelType        string
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	CallFunction     string // e.g. "orchestrator.complete", "cross_agent_relay"
	CallContext      string // e.g. "turn=3 tools=2"
	IsDuplicate      bool   // Phase 2 detection; always false in Phase 1
	CallDurationMS   int64
	Error            string
}

// Store wraps a SQLite connection for usage records.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS prompt_usage (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
    session_name TEXT NOT NULL,
    model_type TEXT NOT NULL,
    prompt_tokens INTEGER,
    completion_tokens INTEGER,
    total_tokens INTEGER,
    call_function TEXT,
    call_context TEXT,
    is_duplicate BOOLEAN DEFAULT 0,
    call_duration INTEGER,
    error TEXT
);
CREATE INDEX IF NOT EXISTS idx_session_time ON prompt_usage(session_name, timestamp);
CREATE INDEX IF NOT EXISTS idx_model_time ON prompt_usage(model_type, timestamp);

CREATE TABLE IF NOT EXISTS claude_sessions (
    claude_session_id TEXT PRIMARY KEY,
    tmux_session      TEXT NOT NULL,
    transcript_path   TEXT NOT NULL,
    cwd               TEXT,
    first_seen        DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS claude_ingest_offset (
    claude_session_id TEXT PRIMARY KEY,
    byte_offset       INTEGER NOT NULL DEFAULT 0,
    last_ingested_at  DATETIME
);
`

// Open creates or opens the usage database at path, creating the schema.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("usage: mkdir: %w", err)
	}
	// WAL + busy_timeout for safe concurrent appends from orchestrator goroutines.
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("usage: open: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite serializes writes; one conn avoids lock churn.
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("usage: schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Record inserts one usage record. It is best-effort: a DB error is logged but
// never returned to the caller, so tracking can't break the orchestrator.
// Timestamps are stored as local "2006-01-02 15:04:05" so SQLite datetime()
// windowing (datetime('now','localtime',...)) and lexicographic comparison line
// up. Duplicate detection marks is_duplicate when the same
// session+function+context was recorded within the last 5 minutes.
func (s *Store) Record(r Record) {
	if s == nil || s.db == nil {
		return
	}
	if r.Timestamp.IsZero() {
		r.Timestamp = time.Now()
	}
	ts := r.Timestamp.Local().Format("2006-01-02 15:04:05")
	ctx := context.Background()

	if !r.IsDuplicate {
		var dup int
		if err := s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM prompt_usage
			 WHERE session_name=? AND call_function=? AND call_context=?
			   AND timestamp >= datetime('now','localtime','-5 minutes')`,
			r.SessionName, r.CallFunction, r.CallContext).Scan(&dup); err == nil && dup > 0 {
			r.IsDuplicate = true
		}
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO prompt_usage
		   (timestamp, session_name, model_type, prompt_tokens, completion_tokens,
		    total_tokens, call_function, call_context, is_duplicate, call_duration, error)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		ts, r.SessionName, r.ModelType,
		r.PromptTokens, r.CompletionTokens, r.TotalTokens,
		r.CallFunction, r.CallContext, r.IsDuplicate, r.CallDurationMS, r.Error,
	)
	if err != nil {
		log.Printf("[usage] record insert: %v", err)
	}
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
