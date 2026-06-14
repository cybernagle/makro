package usage

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseAssistantUsage(t *testing.T) {
	line := []byte(`{"type":"assistant","uuid":"u1","timestamp":"2026-06-14T10:00:00Z","message":{"model":"glm-5.2","usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":10,"cache_creation_input_tokens":5}}}`)
	rec, ok := parseAssistantUsage(line)
	require.True(t, ok)
	require.Equal(t, "glm-5.2", rec.ModelType)
	require.Equal(t, int64(100), rec.PromptTokens)
	require.Equal(t, int64(50), rec.CompletionTokens)
	require.Equal(t, int64(150), rec.TotalTokens)
	require.Equal(t, "u1", rec.CallContext)

	// Non-assistant line skipped.
	_, ok = parseAssistantUsage([]byte(`{"type":"user","message":{"usage":{"input_tokens":1}}}`))
	require.False(t, ok)
	// Assistant without usage skipped.
	_, ok = parseAssistantUsage([]byte(`{"type":"assistant","message":{"model":"x"}}`))
	require.False(t, ok)
}

func assistantLine(uuid string, in, out int64) string {
	return `{"type":"assistant","uuid":"` + uuid + `","timestamp":"2026-06-14T10:00:00Z","message":{"model":"glm-5.2","usage":{"input_tokens":` +
		strconv.FormatInt(in, 10) + `,"output_tokens":` + strconv.FormatInt(out, 10) + `}}}` + "\n"
}

func TestIngestTranscriptsOffsetAndDedup(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "u.db"))
	require.NoError(t, err)
	defer s.Close()

	transcript := filepath.Join(dir, "sess.jsonl")
	// A third assistant turn, written in two parts to simulate Claude Code
	// mid-write: first as a prefix (no newline), then completed next poll.
	a3 := assistantLine("a3", 300, 70)
	partial := a3[:len(a3)-12] // cut off the last 12 chars (incl. trailing newline)

	// Two complete assistant turns + a user line + the partial a3 prefix.
	content := assistantLine("a1", 100, 50) + assistantLine("a2", 200, 60) +
		`{"type":"user","message":{"content":"hi"}}` + "\n" + partial
	require.NoError(t, os.WriteFile(transcript, []byte(content), 0o644))

	s.RecordClaudeSession("cc-1", "dev", transcript, "/tmp")
	s.IngestTranscripts()

	var n int
	require.NoError(t, s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM prompt_usage WHERE call_function='claude_code' AND session_name='dev'`).Scan(&n))
	require.Equal(t, 2, n, "two complete assistant turns ingested; partial trailing line skipped")

	// Offset advanced past complete lines only; re-ingest must not double-count.
	var offset int64
	require.NoError(t, s.db.QueryRowContext(context.Background(),
		`SELECT byte_offset FROM claude_ingest_offset WHERE claude_session_id='cc-1'`).Scan(&offset))
	require.Greater(t, offset, int64(0))
	require.Less(t, int(offset), len(content), "offset must stop before the partial trailing line")

	s.IngestTranscripts()
	require.NoError(t, s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM prompt_usage WHERE call_function='claude_code' AND session_name='dev'`).Scan(&n))
	require.Equal(t, 2, n, "re-ingest of unchanged file must not duplicate")

	// Complete the trailing a3 turn → next ingest picks it up exactly once.
	require.NoError(t, os.WriteFile(transcript, []byte(content+a3[len(a3)-12:]), 0o644))
	s.IngestTranscripts()
	require.NoError(t, s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM prompt_usage WHERE call_function='claude_code' AND session_name='dev'`).Scan(&n))
	require.Equal(t, 3, n, "completed trailing turn ingested on next poll")
}
