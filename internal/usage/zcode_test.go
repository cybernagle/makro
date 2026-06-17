package usage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// createZCodeDB builds a minimal ZCode-shaped DB (model_usage + session) at
// path and inserts the given model_usage rows. Each row: id, status, startedAtMs,
// input, output, cacheRead, cacheCreate, total, model, directory (session).
type zcRow struct {
	id                     string
	status                 string
	startedAtMs            int64
	input, output          int64
	cacheRead, cacheCreate int64
	total                  int64
	model                  string
	directory              string
}

func createZCodeDB(t *testing.T, path string, rows []zcRow) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE session (
		id text primary key, directory text not null, title text not null default ''
	)`)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE model_usage (
		id text primary key, session_id text not null, status text not null,
		started_at integer not null, completed_at integer,
		model_id text not null,
		input_tokens integer default 0, output_tokens integer default 0,
		cache_read_input_tokens integer default 0, cache_creation_input_tokens integer default 0,
		computed_total_tokens integer default 0
	)`)
	require.NoError(t, err)
	// One session per distinct directory.
	sessions := map[string]string{} // directory → session id
	for _, r := range rows {
		if _, ok := sessions[r.directory]; !ok {
			sid := "sess_" + r.directory
			sessions[r.directory] = sid
			_, err = db.Exec(`INSERT INTO session (id, directory) VALUES (?,?)`, sid, r.directory)
			require.NoError(t, err)
		}
	}
	for _, r := range rows {
		_, err = db.Exec(`INSERT INTO model_usage
			(id, session_id, status, started_at, completed_at, model_id,
			 input_tokens, output_tokens, cache_read_input_tokens, cache_creation_input_tokens,
			 computed_total_tokens)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			r.id, sessions[r.directory], r.status, r.startedAtMs, r.startedAtMs+1500, r.model,
			r.input, r.output, r.cacheRead, r.cacheCreate, r.total)
		require.NoError(t, err)
	}
}

func countZCode(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	require.NoError(t, s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM prompt_usage WHERE call_function='zcode'`).Scan(&n))
	return n
}

func TestIngestZCodeMappingAndDedup(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "u.db"))
	require.NoError(t, err)
	defer s.Close()

	zcPath := filepath.Join(dir, "db.sqlite")
	createZCodeDB(t, zcPath, []zcRow{
		// Two completed calls across two projects (→ two session labels).
		{id: "z1", status: "completed", startedAtMs: 1781614914001, input: 39753, output: 366, cacheRead: 38656, total: 40119, model: "GLM-5.2", directory: "/Users/x/Desktop/Code/Makro"},
		{id: "z2", status: "completed", startedAtMs: 1781614908824, input: 1000, output: 50, cacheRead: 0, total: 1050, model: "GLM-5.2", directory: "/Users/x/Desktop/Code/julia"},
		// A running call must be excluded.
		{id: "z3", status: "running", startedAtMs: 1781614920000, input: 999, output: 0, total: 999, model: "GLM-5.2", directory: "/Users/x/Desktop/Code/Makro"},
		// An error call must be excluded.
		{id: "z4", status: "error", startedAtMs: 1781614930000, input: 888, output: 0, total: 888, model: "GLM-5.2", directory: "/Users/x/Desktop/Code/Makro"},
	})

	s.IngestZCode(zcPath)
	require.Equal(t, 2, countZCode(t, s), "only completed calls ingested")

	// Verify field mapping on z1.
	var model, session, ctx string
	var input, total, dur int64
	require.NoError(t, s.db.QueryRowContext(context.Background(),
		`SELECT model_type, session_name, call_context, prompt_tokens, total_tokens, call_duration
		 FROM prompt_usage WHERE call_function='zcode' AND call_context='z1'`).
		Scan(&model, &session, &ctx, &input, &total, &dur))
	require.Equal(t, "GLM-5.2", model)
	require.Equal(t, "Makro", session, "session attributed by directory basename")
	require.Equal(t, "z1", ctx, "call_context is the stable model_usage.id")
	require.Equal(t, int64(39753), input)
	require.Equal(t, int64(40119), total)
	require.Equal(t, int64(1500), dur, "call_duration = completed_at - started_at (ms)")

	// Julia call attributed to its basename too.
	var julia int
	require.NoError(t, s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM prompt_usage WHERE call_function='zcode' AND session_name='julia'`).Scan(&julia))
	require.Equal(t, 1, julia)

	// Re-ingest must not duplicate (row-id dedup).
	s.IngestZCode(zcPath)
	require.Equal(t, 2, countZCode(t, s), "re-ingest must be idempotent")

	// A newly-completed call is picked up on the next poll.
	db, err := sql.Open("sqlite", "file:"+zcPath)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO session (id, directory) VALUES ('sess_new','/Users/x/Desktop/Code/new')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO model_usage
		(id, session_id, status, started_at, model_id, input_tokens, output_tokens, computed_total_tokens)
		VALUES ('z5','sess_new','completed',1781614940000,'GLM-5.2',500,10,510)`)
	require.NoError(t, err)
	db.Close()

	s.IngestZCode(zcPath)
	require.Equal(t, 3, countZCode(t, s), "newly completed call ingested on next poll")
}

func TestIngestZCodeMissingDB(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "u.db"))
	require.NoError(t, err)
	defer s.Close()

	// Non-existent path: silent no-op, no panic, no rows.
	s.IngestZCode(filepath.Join(dir, "does-not-exist.sqlite"))
	require.Equal(t, 0, countZCode(t, s))
}
