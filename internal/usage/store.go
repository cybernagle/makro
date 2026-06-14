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
func (s *Store) Record(r Record) {
	if s == nil || s.db == nil {
		return
	}
	if r.Timestamp.IsZero() {
		r.Timestamp = time.Now()
	}
	_, err := s.db.ExecContext(context.Background(),
		`INSERT INTO prompt_usage
		   (timestamp, session_name, model_type, prompt_tokens, completion_tokens,
		    total_tokens, call_function, call_context, is_duplicate, call_duration, error)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		r.Timestamp.UTC().Format(time.RFC3339), r.SessionName, r.ModelType,
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
