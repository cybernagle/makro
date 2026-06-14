package usage

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// insertRaw inserts a record with an explicit timestamp + duplicate flag,
// bypassing Record()'s "now" stamping for deterministic window tests.
func insertRaw(t *testing.T, s *Store, ts time.Time, session, model string, prompt, comp int64, fn, ctx string, dup bool) {
	t.Helper()
	flag := 0
	if dup {
		flag = 1
	}
	_, err := s.db.Exec(`INSERT INTO prompt_usage
	  (timestamp, session_name, model_type, prompt_tokens, completion_tokens, total_tokens, call_function, call_context, is_duplicate)
	  VALUES (?,?,?,?,?,?,?,?,?)`,
		ts.Local().Format("2006-01-02 15:04:05"), session, model, prompt, comp, prompt+comp, fn, ctx, flag)
	require.NoError(t, err)
}

func TestRecordDuplicateDetection(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "u.db"))
	require.NoError(t, err)
	defer s.Close()

	// Two identical calls (same session+function+context) within 5 minutes →
	// the second is flagged is_duplicate by Record().
	s.Record(Record{SessionName: "dev", CallFunction: "f", CallContext: "c", ModelType: "m"})
	s.Record(Record{SessionName: "dev", CallFunction: "f", CallContext: "c", ModelType: "m"})

	var dupCount int
	require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM prompt_usage WHERE is_duplicate=1`).Scan(&dupCount))
	require.Equal(t, 1, dupCount, "second identical call should be flagged duplicate")
}

func TestStatsWindowModelHighCostIneffective(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "u.db"))
	require.NoError(t, err)
	defer s.Close()

	now := time.Now()
	insertRaw(t, s, now.Add(-1*time.Hour), "dev", "glm-5.2", 100, 50, "orchestrator.complete", "ctx", false)
	insertRaw(t, s, now.Add(-30*time.Minute), "dev", "glm-5.2", 200, 30, "orchestrator.complete", "ctx", true)
	insertRaw(t, s, now.Add(-10*time.Minute), "dev", "glm-4.7", 50, 5, "orchestrator.complete", "ctx", false)  // comp<10 → ineffective
	insertRaw(t, s, now.Add(-48*time.Hour), "dev", "glm-5.2", 999, 999, "orchestrator.complete", "ctx", false) // outside window

	st, err := s.Stats("dev", 5, []string{"glm-5.2"}, 80)
	require.NoError(t, err)
	require.Equal(t, int64(3), st.TotalPrompts, "3 recent calls in 5h window")
	require.Equal(t, int64(1), st.DuplicateCalls)
	require.Equal(t, int64(2), st.HighCostCalls, "2 glm-5.2 calls in window")
	require.Equal(t, int64(1), st.IneffectiveCalls, "1 call with comp<10")
	require.Equal(t, int64(80), st.QuotaTotal)
	require.Greater(t, st.QuotaPercent, 0.0)
	require.Contains(t, st.ByModel, "glm-5.2")
	require.Equal(t, int64(2), st.ByModel["glm-5.2"].Calls)
	require.Equal(t, int64(1), st.ByModel["glm-4.7"].Calls)
}

func TestDiagnosticsFrequentAndDuplicate(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "u.db"))
	require.NoError(t, err)
	defer s.Close()

	now := time.Now()
	// 11 calls to send_to_session within 1 minute → over-frequent (>10/min).
	for i := 0; i < 11; i++ {
		insertRaw(t, s, now.Add(-time.Duration(i)*time.Second), "dev", "glm-5.2", 10, 20, "send_to_session", "ctx", false)
	}
	// 3 duplicate-flagged read_file calls.
	for i := 0; i < 3; i++ {
		insertRaw(t, s, now.Add(-time.Duration(i)*time.Minute), "dev", "glm-5.2", 30, 10, "read_file", "package.json", true)
	}

	d, err := s.Diagnostics("dev")
	require.NoError(t, err)

	require.NotEmpty(t, d.FrequentPatterns, "send_to_session should trigger frequent detection")
	found := false
	for _, fp := range d.FrequentPatterns {
		if fp.Function == "send_to_session" {
			found = true
			require.GreaterOrEqual(t, fp.Count, int64(11))
		}
	}
	require.True(t, found)

	require.NotEmpty(t, d.DuplicatePatterns, "read_file duplicates should appear")
	require.NotEmpty(t, d.Recommendations, "recommendations should be generated")
}

func TestTimelineBuckets(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "u.db"))
	require.NoError(t, err)
	defer s.Close()

	now := time.Now()
	insertRaw(t, s, now.Add(-10*time.Minute), "dev", "glm-5.2", 100, 50, "f", "c", false)
	insertRaw(t, s, now.Add(-90*time.Minute), "dev", "glm-5.2", 200, 60, "f", "c", false)

	tl, err := s.Timeline("dev", 3)
	require.NoError(t, err)
	require.Len(t, tl, 2, "two distinct hour buckets")
	require.NotEmpty(t, tl[0].Hour)
	require.Equal(t, int64(410), tl[0].TotalTokens+tl[1].TotalTokens)
}
