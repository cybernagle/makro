package usage

import (
	"context"
	"fmt"
	"strings"
)

// Stats is the /api/usage/stats payload for a window.
type Stats struct {
	Session          string                `json:"session"`
	WindowHours      int                   `json:"window_hours"`
	TotalPrompts     int64                 `json:"total_prompts"`
	TotalTokens      int64                 `json:"total_tokens"`
	PromptTokens     int64                 `json:"prompt_tokens"`
	CompletionTokens int64                 `json:"completion_tokens"`
	DuplicateCalls   int64                 `json:"duplicate_calls"`
	FrequentCalls    int64                 `json:"frequent_calls"`
	HighCostCalls    int64                 `json:"high_cost_calls"`
	IneffectiveCalls int64                 `json:"ineffective_calls"`
	QuotaUsed        int64                 `json:"quota_used"`
	QuotaTotal       int64                 `json:"quota_total"`
	QuotaPercent     float64               `json:"quota_percent"`
	ByModel          map[string]ModelStats `json:"by_model"`
}

// ModelStats is a per-model breakdown.
type ModelStats struct {
	Calls            int64 `json:"calls"`
	TotalTokens      int64 `json:"total_tokens"`
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
}

// DuplicatePattern summarizes a repeated session+function+context.
type DuplicatePattern struct {
	Function     string `json:"function"`
	Context      string `json:"context"`
	Count        int64  `json:"count"`
	WastedTokens int64  `json:"wasted_tokens"`
}

// FrequentPattern flags a function called too often within a window.
type FrequentPattern struct {
	Function        string `json:"function"`
	Count           int64  `json:"count"`
	TimespanMinutes int64  `json:"timespan_minutes"`
}

// Diagnostics is the /api/usage/diagnostics payload.
type Diagnostics struct {
	DuplicatePatterns []DuplicatePattern `json:"duplicate_patterns"`
	FrequentPatterns  []FrequentPattern  `json:"frequent_patterns"`
	IneffectiveCalls  int64              `json:"ineffective_calls"`
	Recommendations   []string           `json:"recommendations"`
}

// TimelinePoint is one hourly bucket.
type TimelinePoint struct {
	Hour             string `json:"hour"`
	Calls            int64  `json:"calls"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	TotalTokens      int64  `json:"total_tokens"`
}

// Stats aggregates usage over the last `hours` (default 5). highCostModels are
// matched as prefixes; quotaTotal is the window quota (0 = unknown).
func (s *Store) Stats(session string, hours int, highCostModels []string, quotaTotal int64) (*Stats, error) {
	if s == nil || s.db == nil {
		return &Stats{ByModel: map[string]ModelStats{}}, nil
	}
	if hours <= 0 {
		hours = 5
	}
	ctx := context.Background()
	st := &Stats{Session: session, WindowHours: hours, QuotaTotal: quotaTotal, ByModel: map[string]ModelStats{}}
	win := fmt.Sprintf("-%d hours", hours)
	sess, sessArg := sessionCond(session)

	// Base aggregates + duplicates.
	q := `SELECT COUNT(*), COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0),
	      COALESCE(SUM(total_tokens),0), COALESCE(SUM(CASE WHEN is_duplicate=1 THEN 1 ELSE 0 END),0)
	      FROM prompt_usage WHERE timestamp >= datetime('now','localtime',?)` + sess
	var dup int64
	_ = s.db.QueryRowContext(ctx, q, append([]any{win}, sessArg...)...).Scan(
		&st.TotalPrompts, &st.PromptTokens, &st.CompletionTokens, &st.TotalTokens, &dup)
	st.DuplicateCalls = dup

	// High-cost calls (model matches a high-cost prefix).
	if len(highCostModels) > 0 {
		likes := make([]string, len(highCostModels))
		args := []any{win}
		for i, m := range highCostModels {
			likes[i] = "model_type LIKE ?"
			args = append(args, m+"%")
		}
		q2 := `SELECT COUNT(*) FROM prompt_usage WHERE timestamp >= datetime('now','localtime',?) AND (` +
			strings.Join(likes, " OR ") + `)` + sess
		_ = s.db.QueryRowContext(ctx, q2, append(args, sessArg...)...).Scan(&st.HighCostCalls)
	}

	// Ineffective: response < 10 tokens, no error.
	q3 := `SELECT COUNT(*) FROM prompt_usage WHERE timestamp >= datetime('now','localtime',?)
	        AND completion_tokens < 10 AND (error IS NULL OR error='')` + sess
	_ = s.db.QueryRowContext(ctx, q3, append([]any{win}, sessArg...)...).Scan(&st.IneffectiveCalls)

	// Frequent: total calls in 5 min from functions exceeding 30 calls/5min.
	st.FrequentCalls = s.countFrequent(ctx, session, 5, 30)

	// Per-model breakdown.
	qm := `SELECT model_type, COUNT(*), COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0), COALESCE(SUM(total_tokens),0)
	       FROM prompt_usage WHERE timestamp >= datetime('now','localtime',?)` + sess + ` GROUP BY model_type`
	rows, err := s.db.QueryContext(ctx, qm, append([]any{win}, sessArg...)...)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var m string
			var ms ModelStats
			if rows.Scan(&m, &ms.Calls, &ms.PromptTokens, &ms.CompletionTokens, &ms.TotalTokens) == nil {
				st.ByModel[m] = ms
			}
		}
	}

	// Quota (window quota counts prompts).
	st.QuotaUsed = st.TotalPrompts
	if quotaTotal > 0 {
		st.QuotaPercent = float64(st.QuotaUsed) / float64(quotaTotal) * 100
	}
	return st, nil
}

// countFrequent sums call counts of functions exceeding `threshold` calls in
// the last `minutes`.
func (s *Store) countFrequent(ctx context.Context, session string, minutes, threshold int) int64 {
	sess, sessArg := sessionCond(session)
	q := fmt.Sprintf(`SELECT COALESCE(SUM(cnt),0) FROM (
		  SELECT COUNT(*) as cnt FROM prompt_usage
		  WHERE timestamp >= datetime('now','localtime','-%d minutes')%s
		  GROUP BY call_function HAVING cnt > %d)`, minutes, sess, threshold)
	var n int64
	_ = s.db.QueryRowContext(ctx, q, sessArg...).Scan(&n)
	return n
}

// Diagnostics reports duplicate/frequent/ineffective patterns + recommendations.
func (s *Store) Diagnostics(session string) (*Diagnostics, error) {
	if s == nil || s.db == nil {
		return &Diagnostics{}, nil
	}
	ctx := context.Background()
	d := &Diagnostics{}
	sess, sessArg := sessionCond(session)

	// Duplicate patterns.
	q := `SELECT call_function, COALESCE(call_context,''), COUNT(*), COALESCE(SUM(total_tokens),0)
	      FROM prompt_usage WHERE is_duplicate=1` + sess + `
	      GROUP BY call_function, call_context ORDER BY COUNT(*) DESC LIMIT 20`
	rows, err := s.db.QueryContext(ctx, q, sessArg...)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var dp DuplicatePattern
			if rows.Scan(&dp.Function, &dp.Context, &dp.Count, &dp.WastedTokens) == nil {
				d.DuplicatePatterns = append(d.DuplicatePatterns, dp)
			}
		}
	}

	// Frequent patterns: 1-min (>10) then 5-min (>30), merged.
	seen := map[string]bool{}
	for _, w := range []struct{ min, thr int }{{1, 10}, {5, 30}} {
		fq := fmt.Sprintf(`SELECT call_function, COUNT(*) FROM prompt_usage
		                   WHERE timestamp >= datetime('now','localtime','-%d minutes')%s
		                   GROUP BY call_function HAVING COUNT(*) > %d ORDER BY COUNT(*) DESC`, w.min, sess, w.min)
		fr, err := s.db.QueryContext(ctx, fq, sessArg...)
		if err != nil {
			continue
		}
		for fr.Next() {
			var fp FrequentPattern
			if fr.Scan(&fp.Function, &fp.Count) == nil && !seen[fp.Function] {
				fp.TimespanMinutes = int64(w.min)
				seen[fp.Function] = true
				d.FrequentPatterns = append(d.FrequentPatterns, fp)
			}
		}
		fr.Close()
	}

	// Ineffective.
	qi := `SELECT COUNT(*) FROM prompt_usage WHERE completion_tokens < 10 AND (error IS NULL OR error='')` + sess
	_ = s.db.QueryRowContext(ctx, qi, sessArg...).Scan(&d.IneffectiveCalls)

	d.Recommendations = buildRecommendations(d)
	return d, nil
}

// Timeline returns hourly usage buckets over the last `hours`.
func (s *Store) Timeline(session string, hours int) ([]TimelinePoint, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	if hours <= 0 {
		hours = 24
	}
	ctx := context.Background()
	win := fmt.Sprintf("-%d hours", hours)
	sess, sessArg := sessionCond(session)
	q := `SELECT strftime('%Y-%m-%dT%H:00:00', timestamp), COUNT(*),
	      COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0), COALESCE(SUM(total_tokens),0)
	      FROM prompt_usage WHERE timestamp >= datetime('now','localtime',?)` + sess + `
	      GROUP BY strftime('%Y-%m-%dT%H:00:00', timestamp) ORDER BY 1`
	rows, err := s.db.QueryContext(ctx, q, append([]any{win}, sessArg...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TimelinePoint
	for rows.Next() {
		var p TimelinePoint
		if rows.Scan(&p.Hour, &p.Calls, &p.PromptTokens, &p.CompletionTokens, &p.TotalTokens) == nil {
			out = append(out, p)
		}
	}
	return out, nil
}

func buildRecommendations(d *Diagnostics) []string {
	var recs []string
	for _, dp := range d.DuplicatePatterns {
		if dp.Count >= 3 {
			recs = append(recs, fmt.Sprintf("%s 在 5 分钟内重复调用 %d 次（浪费 ~%d tokens），建议缓存结果或去重",
				dp.Function, dp.Count, dp.WastedTokens))
		}
	}
	for _, fp := range d.FrequentPatterns {
		recs = append(recs, fmt.Sprintf("%s 调用过于频繁（%d 次/%d 分钟），检查是否存在循环调用", fp.Function, fp.Count, fp.TimespanMinutes))
	}
	if d.IneffectiveCalls >= 5 {
		recs = append(recs, fmt.Sprintf("有 %d 次响应过短（<10 tokens），检查提示词或上下文是否有效", d.IneffectiveCalls))
	}
	return recs
}

// sessionCond returns a " AND session_name=?" clause (with its arg) when session
// is non-empty, else empty string and nil arg.
func sessionCond(session string) (string, []any) {
	if session == "" {
		return "", nil
	}
	return " AND session_name=?", []any{session}
}
