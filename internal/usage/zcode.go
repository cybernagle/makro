package usage

import (
	"context"
	"database/sql"
	"log"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO)
)

// IngestZCode imports completed model calls from ZCode's own usage DB
// (~/.zcode/cli/db/db.sqlite → model_usage) into prompt_usage, attributed to
// call_function="zcode". Intended for the periodic ticker alongside the Claude
// Code transcript scan.
//
// ZCode stores per-call usage in a structured SQLite table (not an append-only
// transcript), and a row's status transitions running→completed. An offset
// model (like Claude's) does not fit, so we dedup by row id: a model_usage.id
// already present as a zcode call_context is skipped.
//
// The foreign DB is opened read-only so it can never compete with ZCode's own
// writes. Best-effort: a missing DB/table or read error is logged and skipped.
func (s *Store) IngestZCode(dbPath string) {
	if s == nil || s.db == nil || dbPath == "" {
		return
	}
	if _, err := os.Stat(dbPath); err != nil {
		return // ZCode not installed / not run yet
	}
	// Open with only a busy_timeout (we never write). We deliberately do NOT
	// force journal_mode=... here — that would mutate ZCode's DB settings as a
	// side effect of reading it. ZCode manages its own journal mode; we just
	// read whatever WAL/rollback state it's in.
	dsn := "file:" + dbPath + "?_pragma=busy_timeout(2000)"
	zdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		log.Printf("[usage] zcode open: %v", err)
		return
	}
	defer zdb.Close()
	// A single connection keeps the read snapshot consistent and cheap.
	zdb.SetMaxOpenConns(1)

	ctx := context.Background()

	// Already-ingested zcode call ids (the model_usage.id we stored as
	// call_context). Skip these to stay idempotent across ticker runs.
	seen := map[string]bool{}
	srows, err := s.db.QueryContext(ctx,
		`SELECT call_context FROM prompt_usage WHERE call_function='zcode'`)
	if err == nil {
		for srows.Next() {
			var id string
			if srows.Scan(&id) == nil {
				seen[id] = true
			}
		}
		srows.Close()
	}

	// JOIN session for working-directory attribution (basename, matching the
	// Claude Code sessionLabel convention so cross-source costs align by project).
	rows, err := zdb.QueryContext(ctx, `SELECT m.id, m.started_at, m.completed_at,
		m.model_id, m.input_tokens, m.output_tokens,
		m.cache_read_input_tokens, m.cache_creation_input_tokens,
		m.computed_total_tokens, s.directory
		FROM model_usage m JOIN session s ON s.id=m.session_id
		WHERE m.status='completed'
		ORDER BY m.started_at`)
	if err != nil {
		log.Printf("[usage] zcode query: %v", err)
		return
	}
	defer rows.Close()

	var n int
	for rows.Next() {
		var (
			id                                   string
			startedAt                            int64 // epoch ms; NOT NULL in ZCode schema
			completedAt                          sql.NullInt64
			model                                string
			input, output, cacheR, cacheC, total int64
			directory                            sql.NullString
		)
		if err := rows.Scan(&id, &startedAt, &completedAt, &model,
			&input, &output, &cacheR, &cacheC, &total, &directory); err != nil {
			continue
		}
		if seen[id] {
			continue
		}
		var dur int64
		if completedAt.Valid && completedAt.Int64 > startedAt {
			dur = completedAt.Int64 - startedAt
		}
		s.Record(Record{
			Timestamp:           time.UnixMilli(startedAt),
			SessionName:         sessionLabel(directory.String),
			ModelType:           model,
			PromptTokens:        input,
			CompletionTokens:    output,
			CacheReadTokens:     cacheR,
			CacheCreationTokens: cacheC,
			TotalTokens:         total,
			CallFunction:        "zcode",
			CallContext:         id, // stable per call → idempotent ingest
			CallDurationMS:      dur,
		})
		n++
	}
	if n > 0 {
		log.Printf("[usage] zcode ingested %d new calls", n)
	}
}

// ZCodeDBPath resolves the ZCode CLI usage DB location under the user's home
// (~/.zcode/cli/db/db.sqlite). Returns "" if the home dir is unknown.
func ZCodeDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".zcode", "cli", "db", "db.sqlite")
}
