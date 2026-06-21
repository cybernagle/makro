package brain

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Reader wraps a memory Client and exposes the brain's read-side queries
// (R1–R5 from BRAIN_DESIGN §9.2). Each method is one focused GET against the
// :8765 REST API. The brain's wake loop calls all of them, assembles a
// ReadSnapshot, then hands it to the proposer.
//
// Read failures are non-fatal: a query that errors returns an empty slice and
// the error is logged by the caller. The brain would rather generate a
// low-confidence proposal from partial memory than skip the whole wake because
// one read failed.
type Reader struct {
	client *Client
}

// NewReader builds a Reader over the given client.
func NewReader(c *Client) *Reader {
	return &Reader{client: c}
}

// ReadSnapshot is the assembled view of memory the proposer consumes. Fields
// are ordered by fit-signal importance: Feedback > Profile > Captures (a
// capture-only snapshot with no profile/feedback is a cold start — the proposer
// should produce low-confidence output or skip).
type ReadSnapshot struct {
	Captures   []ListedMemory // R1: recent user messages (the idea mine)
	Profile    []ListedMemory // R2: character/preferences/soul (what I care about)
	OpenProps  []ListedMemory // R3: proposals still open (= said I'd do, didn't)
	Feedback   []ListedMemory // R4: recent accept/reject (the fit signal — lifeblood)
	Recents    []ListedMemory // R5: recent proposal titles (dedup snapshot)
	ReadAt     time.Time
	ReadErrors []string // per-query errors, for logging; empty = all reads clean
}

// RecentCaptures (R1): the user's recent ideas — the raw idea mine. These are
// what the brain mines for "things you mentioned but didn't act on."
//
// Two sources are merged:
//   - category=capture (the P0 capture pipeline — live user messages going
//     forward, tagged chat-capture)
//   - category=knowledge (the historical goldmine: stop-hook.sh + memory-cli's
//     fact-processor have accumulated thousands of structured idea/knowledge
//     records over months. Ignoring these would blind the brain on day one.)
//
// The capture category is the future (every new chat lands here); knowledge is
// the past (already-mined, higher signal-per-record). Both are read so the
// brain has signal from the very first wake instead of a cold start.
func (r *Reader) RecentCaptures(ctx context.Context, days, limit int) ([]ListedMemory, error) {
	from := time.Now().AddDate(0, 0, -days)
	// Query both categories and merge. Split the limit so neither starves.
	half := limit / 2
	if half < 1 {
		half = limit
	}

	var all []ListedMemory
	// New pipeline: live captures.
	if caps, err := r.client.List(ctx, Query{
		Category: CategoryCapture, Tags: []string{"chat-capture"}, From: from, Limit: half,
	}); err == nil {
		all = append(all, caps...)
	}
	// Historical goldmine: knowledge records (fact-processor-distilled ideas).
	// No tag filter — we want all recent knowledge, not just one source.
	if know, err := r.client.List(ctx, Query{
		Category: "knowledge", From: from, Limit: half,
	}); err == nil {
		all = append(all, know...)
	}
	return all, nil
}

// Profile (R2): the synthesized user profile. memory-cli's ProfileTask (M7)
// writes these to category=character with metadata.type=profile. We pull the
// latest few — the proposer uses accept_rate and domain distribution from here.
// Also pulls preferences + soul for broader "what I care about" signal.
func (r *Reader) Profile(ctx context.Context, limit int) ([]ListedMemory, error) {
	// character holds the synthesized profile (M7). preferences/soul add color.
	// We issue one query per category and merge — simpler than a multi-category
	// OR which the API doesn't support in one call.
	var all []ListedMemory
	for _, cat := range []string{"character", "preferences", "soul"} {
		got, err := r.client.List(ctx, Query{Category: cat, Limit: limit})
		if err != nil {
			return all, fmt.Errorf("profile %s: %w", cat, err)
		}
		all = append(all, got...)
	}
	return all, nil
}

// OpenProposals (R3): proposals whose metadata.status is still open. The API
// can't filter on metadata server-side yet (RECONCILE §5.2), so we pull all
// proposals and filter client-side. Proposal volume is small (a few/day), so
// this is cheap.
func (r *Reader) OpenProposals(ctx context.Context) ([]ListedMemory, error) {
	all, err := r.client.List(ctx, Query{Category: CategoryProposals, Limit: 100})
	if err != nil {
		return nil, err
	}
	var open []ListedMemory
	for _, m := range all {
		if metadataStatus(m) == "open" || metadataStatus(m) == "" {
			open = append(open, m)
		}
	}
	return open, nil
}

// RecentFeedback (R4): the fit signal — my recent accept/reject/ignore. This is
// the single most important read: it's how the brain learns what I actually
// do vs. what I just talk about.
func (r *Reader) RecentFeedback(ctx context.Context, days, limit int) ([]ListedMemory, error) {
	from := time.Now().AddDate(0, 0, -days)
	return r.client.List(ctx, Query{
		Category: CategoryFeedback,
		Tags:     []string{"brain-feedback"},
		From:     from,
		Limit:    limit,
	})
}

// RecentProposalTitles (R5): the dedup snapshot — recent proposals so the brain
// doesn't re-push the same idea. Returns just titles (content first line) for
// cheap comparison.
func (r *Reader) RecentProposalTitles(ctx context.Context, days int) ([]string, error) {
	from := time.Now().AddDate(0, 0, -days)
	ms, err := r.client.List(ctx, Query{
		Category: CategoryProposals,
		From:     from,
		Limit:    30,
	})
	if err != nil {
		return nil, err
	}
	titles := make([]string, 0, len(ms))
	for _, m := range ms {
		if t := firstLine(m.Content); t != "" {
			titles = append(titles, t)
		}
	}
	return titles, nil
}

// ReadAll runs R1–R5 and assembles a snapshot. Errors per-query are collected
// into ReadErrors (non-fatal) so a single failing endpoint doesn't abort the
// wake — the brain would rather propose from partial memory than skip. The
// caller logs the errors. Returns the snapshot even if some reads failed.
func (r *Reader) ReadAll(ctx context.Context) ReadSnapshot {
	snap := ReadSnapshot{ReadAt: time.Now()}

	if ms, err := r.RecentCaptures(ctx, 7, 100); err != nil {
		snap.ReadErrors = append(snap.ReadErrors, "R1.captures: "+err.Error())
	} else {
		snap.Captures = ms
	}
	if ms, err := r.Profile(ctx, 5); err != nil {
		snap.ReadErrors = append(snap.ReadErrors, "R2.profile: "+err.Error())
	} else {
		snap.Profile = ms
	}
	if ms, err := r.OpenProposals(ctx); err != nil {
		snap.ReadErrors = append(snap.ReadErrors, "R3.open: "+err.Error())
	} else {
		snap.OpenProps = ms
	}
	if ms, err := r.RecentFeedback(ctx, 30, 20); err != nil {
		snap.ReadErrors = append(snap.ReadErrors, "R4.feedback: "+err.Error())
	} else {
		snap.Feedback = ms
	}
	if titles, err := r.RecentProposalTitles(ctx, 30); err != nil {
		snap.ReadErrors = append(snap.ReadErrors, "R5.titles: "+err.Error())
	} else {
		snap.Recents = titlesToMemories(titles)
	}
	return snap
}

// HasSignal reports whether the snapshot has enough for the proposer to try.
// The absolute floor is some captures (an idea mine) — with zero captures the
// brain has nothing to propose from. Profile/feedback improve quality but
// aren't required (cold start).
func (s ReadSnapshot) HasSignal() bool {
	return len(s.Captures) > 0
}

// ── helpers ──

func metadataStatus(m ListedMemory) string {
	if m.Metadata == nil {
		return ""
	}
	if s, ok := m.Metadata["status"].(string); ok {
		return s
	}
	return ""
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// titlesToMemories wraps a title slice into ListedMemory shells so it fits the
// snapshot's Recents field type (kept uniform with the other R-fields).
func titlesToMemories(titles []string) []ListedMemory {
	if len(titles) == 0 {
		return nil
	}
	out := make([]ListedMemory, len(titles))
	for i, t := range titles {
		out[i] = ListedMemory{Content: t}
	}
	return out
}
