package usage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// ExportRow is one prompt_usage row for CSV / detail download.
type ExportRow struct {
	Timestamp           time.Time
	Session             string
	Function            string
	Model               string
	PromptTokens        int64
	CompletionTokens    int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	TotalTokens         int64
	IsDuplicate         bool
	DurationMS          int64
	Error               string
}

// Export returns raw usage rows (newest first) within the window + filter, for
// CSV/detail download. Capped at 10000 rows.
func (s *Store) Export(f Filter, hours int) ([]ExportRow, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	if hours <= 0 {
		hours = 24
	}
	ctx := context.Background()
	win := fmt.Sprintf("-%d hours", hours)
	cl, args := f.clause()
	q := `SELECT timestamp, session_name, call_function, model_type,
	         prompt_tokens, completion_tokens, cache_read_tokens, cache_creation_tokens,
	         total_tokens, is_duplicate, call_duration, error
	      FROM prompt_usage WHERE timestamp >= datetime('now','localtime',?)` + cl + `
	      ORDER BY timestamp DESC LIMIT 10000`
	rows, err := s.db.QueryContext(ctx, q, append([]any{win}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExportRow
	for rows.Next() {
		var r ExportRow
		var ts, fn, mdl string
		var errStr sql.NullString
		if err := rows.Scan(&ts, &r.Session, &fn, &mdl, &r.PromptTokens, &r.CompletionTokens,
			&r.CacheReadTokens, &r.CacheCreationTokens, &r.TotalTokens, &r.IsDuplicate,
			&r.DurationMS, &errStr); err != nil {
			continue
		}
		r.Timestamp, _ = time.ParseInLocation("2006-01-02 15:04:05", ts, time.Local)
		r.Function, r.Model, r.Error = fn, mdl, errStr.String
		out = append(out, r)
	}
	return out, nil
}
