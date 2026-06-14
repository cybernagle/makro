package usage

import (
	"context"
	"fmt"
	"strings"
	"time"
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
	BySource         map[string]ModelStats `json:"by_source"`  // Claude Code vs Makro
	BySession        map[string]ModelStats `json:"by_session"` // per project/tmux session
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
func (s *Store) Stats(f Filter, hours int, highCostModels []string, quotaTotal int64) (*Stats, error) {
	if s == nil || s.db == nil {
		return &Stats{ByModel: map[string]ModelStats{}, BySource: map[string]ModelStats{}, BySession: map[string]ModelStats{}}, nil
	}
	if hours <= 0 {
		hours = 5
	}
	ctx := context.Background()
	st := &Stats{Session: f.Session, WindowHours: hours, QuotaTotal: quotaTotal,
		ByModel: map[string]ModelStats{}, BySource: map[string]ModelStats{}, BySession: map[string]ModelStats{}}
	win := fmt.Sprintf("-%d hours", hours)
	sess, sessArg := f.clause()

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

	// Ineffective: response < 10 tokens, no error. Excludes claude_code — Claude
	// Code's short glm-5.x routing turns are an efficient pattern, not waste.
	q3 := `SELECT COUNT(*) FROM prompt_usage WHERE timestamp >= datetime('now','localtime',?)
	        AND completion_tokens < 10 AND call_function!='claude_code' AND (error IS NULL OR error='')` + sess
	_ = s.db.QueryRowContext(ctx, q3, append([]any{win}, sessArg...)...).Scan(&st.IneffectiveCalls)

	// Frequent: total calls in 5 min from functions exceeding 30 calls/5min.
	st.FrequentCalls = s.countFrequent(ctx, f.Session, 5, 30)

	// Breakdowns: by model, by source (Claude Code vs Makro), by session/project.
	s.scanBreakdown(ctx, `SELECT model_type, COUNT(*), COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0), COALESCE(SUM(total_tokens),0) FROM prompt_usage WHERE timestamp >= datetime('now','localtime',?)`+sess+` GROUP BY model_type`, win, sessArg, st.ByModel)
	s.scanBreakdown(ctx, `SELECT CASE WHEN call_function='claude_code' THEN 'Claude Code' ELSE 'Makro' END, COUNT(*), COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0), COALESCE(SUM(total_tokens),0) FROM prompt_usage WHERE timestamp >= datetime('now','localtime',?)`+sess+` GROUP BY 1`, win, sessArg, st.BySource)
	s.scanBreakdown(ctx, `SELECT session_name, COUNT(*), COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0), COALESCE(SUM(total_tokens),0) FROM prompt_usage WHERE timestamp >= datetime('now','localtime',?)`+sess+` GROUP BY session_name ORDER BY COALESCE(SUM(total_tokens),0) DESC LIMIT 30`, win, sessArg, st.BySession)

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
		  WHERE timestamp >= datetime('now','localtime','-%d minutes')%s AND call_function!='claude_code'
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
		                   WHERE timestamp >= datetime('now','localtime','-%d minutes')%s AND call_function!='claude_code'
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

	// Ineffective (excludes claude_code — see Stats ineffective note).
	qi := `SELECT COUNT(*) FROM prompt_usage WHERE completion_tokens < 10 AND call_function!='claude_code' AND (error IS NULL OR error='')` + sess
	_ = s.db.QueryRowContext(ctx, qi, sessArg...).Scan(&d.IneffectiveCalls)

	d.Recommendations = buildRecommendations(d)
	return d, nil
}

// Timeline returns usage buckets over the last `hours`, quantized to
// granularityMin-minute buckets (default 60 = hourly). Bucket label is the
// local wall-clock start of the bucket ("2006-01-02T15:04").
func (s *Store) Timeline(f Filter, hours, granularityMin int) ([]TimelinePoint, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	if hours <= 0 {
		hours = 24
	}
	if granularityMin <= 0 {
		granularityMin = 60
	}
	bucket := int64(granularityMin) * 60 // seconds
	ctx := context.Background()
	win := fmt.Sprintf("-%d hours", hours)
	sess, sessArg := f.clause()
	// Quantize each timestamp's epoch to the bucket; group by bucket epoch.
	// strftime('%s', ts) treats the local-stored ts as UTC, so formatting the
	// bucket epoch back as UTC recovers the original local wall-clock time.
	q := `SELECT (CAST(strftime('%s', timestamp) AS INTEGER) / ?) * ? AS b,
	      COUNT(*), COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0), COALESCE(SUM(total_tokens),0)
	      FROM prompt_usage WHERE timestamp >= datetime('now','localtime',?)` + sess + `
	      GROUP BY b ORDER BY b`
	rows, err := s.db.QueryContext(ctx, q, append([]any{bucket, bucket, win}, sessArg...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TimelinePoint
	for rows.Next() {
		var b int64
		var p TimelinePoint
		if rows.Scan(&b, &p.Calls, &p.PromptTokens, &p.CompletionTokens, &p.TotalTokens) != nil {
			continue
		}
		p.Hour = time.Unix(b, 0).UTC().Format("2006-01-02T15:04")
		out = append(out, p)
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

// Filter narrows usage queries by session, source, and/or model.
type Filter struct {
	Session string // session_name (tmux/project)
	Source  string // "Claude Code" | "Makro" | "" (all)
	Model   string // model_type
}

// clause returns a " AND ..." SQL fragment + its args for the filter.
func (f Filter) clause() (string, []any) {
	var clauses []string
	var args []any
	if f.Session != "" {
		clauses = append(clauses, "session_name=?")
		args = append(args, f.Session)
	}
	switch f.Source {
	case "Claude Code":
		clauses = append(clauses, "call_function='claude_code'")
	case "Makro":
		clauses = append(clauses, "call_function!='claude_code'")
	}
	if f.Model != "" {
		clauses = append(clauses, "model_type=?")
		args = append(args, f.Model)
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return " AND " + strings.Join(clauses, " AND "), args
}

// scanBreakdown runs a `SELECT key, calls, prompt, completion, total ... GROUP
// BY key` query (window-bound) and fills dest. Errors are ignored (best-effort).
func (s *Store) scanBreakdown(ctx context.Context, q, win string, sessArg []any, dest map[string]ModelStats) {
	rows, err := s.db.QueryContext(ctx, q, append([]any{win}, sessArg...)...)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var ms ModelStats
		if rows.Scan(&k, &ms.Calls, &ms.PromptTokens, &ms.CompletionTokens, &ms.TotalTokens) == nil {
			dest[k] = ms
		}
	}
}
