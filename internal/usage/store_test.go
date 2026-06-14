package usage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStoreRecordAndQuery(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "usage.db"))
	require.NoError(t, err)
	defer s.Close()

	s.Record(Record{
		SessionName: "dev", ModelType: "glm-5.2",
		PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150,
		CallFunction: "orchestrator.complete", CallDurationMS: 1234,
	})
	s.Record(Record{
		SessionName: "dev", ModelType: "glm-5.2",
		PromptTokens: 200, CompletionTokens: 100, TotalTokens: 300,
		CallFunction: "orchestrator.complete", CallDurationMS: 567,
	})

	rows, err := s.db.QueryContext(context.Background(),
		`SELECT COUNT(*), COALESCE(SUM(total_tokens),0) FROM prompt_usage WHERE session_name=?`, "dev")
	require.NoError(t, err)
	defer rows.Close()
	require.True(t, rows.Next())
	var n int
	var sumTotal int64
	require.NoError(t, rows.Scan(&n, &sumTotal))
	require.Equal(t, 2, n)
	require.Equal(t, int64(450), sumTotal)
}

func TestStoreNilSafe(t *testing.T) {
	var s *Store
	require.NotPanics(t, func() { s.Record(Record{SessionName: "x"}) })
	require.NoError(t, s.Close())
}
