package brain

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── propose.go: brace-scrape + JSON parse ──

func TestBraceScrapePlainJSON(t *testing.T) {
	got := braceScrape(`{"title":"x","confidence":0.5}`)
	assert.Equal(t, `{"title":"x","confidence":0.5}`, got)
}

func TestBraceScrapeWrappedInProse(t *testing.T) {
	in := `Sure! Here's your proposal:\n{"title":"do X","body":"because","confidence":0.7,"domain":"x","reason":"now"}\nHope it helps.`
	got := braceScrape(in)
	require.NotEmpty(t, got)
	var p Proposal
	require.NoError(t, json.Unmarshal([]byte(got), &p))
	assert.Equal(t, "do X", p.Title)
	assert.InDelta(t, 0.7, p.Confidence, 0.001)
}

func TestBraceScrapeNestedObject(t *testing.T) {
	// Metadata-like nesting shouldn't fool the depth counter.
	in := `{"title":"x","body":"y","confidence":0.5,"meta":{"a":1},"domain":"d","reason":"r"}`
	got := braceScrape(in)
	var p Proposal
	require.NoError(t, json.Unmarshal([]byte(got), &p))
	assert.Equal(t, "d", p.Domain)
}

func TestBraceScrapeNoBrace(t *testing.T) {
	assert.Empty(t, braceScrape("no json here at all"))
}

func TestBraceScrapeMarkdownFenced(t *testing.T) {
	in := "```json\n{\"title\":\"x\",\"body\":\"y\",\"confidence\":0.5,\"domain\":\"d\",\"reason\":\"r\"}\n```"
	got := braceScrape(in)
	var p Proposal
	require.NoError(t, json.Unmarshal([]byte(got), &p))
	assert.Equal(t, "x", p.Title)
}

func TestSummarizeMemoriesCapsLength(t *testing.T) {
	ms := make([]ListedMemory, 50)
	for i := range ms {
		ms[i] = ListedMemory{Content: strings.Repeat("x", 300)}
	}
	got := summarizeMemories(ms, 1000)
	assert.LessOrEqual(t, len(got), 1100) // some slack for the "...省略" line
	assert.Contains(t, got, "省略")
}

func TestSummarizeMemoriesEmpty(t *testing.T) {
	assert.Empty(t, summarizeMemories(nil, 1000))
}

func TestBuildProposeUserMessageIncludesSections(t *testing.T) {
	snap := ReadSnapshot{
		Captures: []ListedMemory{{Content: "我想学 rust"}},
		Profile:  []ListedMemory{{Content: "accept_rate: 0.5"}},
	}
	msg := buildProposeUserMessage(snap)
	assert.Contains(t, msg, "R1")
	assert.Contains(t, msg, "我想学 rust")
	assert.Contains(t, msg, "accept_rate: 0.5")
}

// ── inbox.go: SQLite CRUD ──

func newTestInbox(t *testing.T) *InboxStore {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "inbox.db"))
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

func TestInboxAddAndGet(t *testing.T) {
	s := newTestInbox(t)
	ctx := context.Background()
	id, err := s.Add(ctx, "mem-uuid-1", "Learn rust", "because", "rust", 0.7)
	require.NoError(t, err)
	assert.Greater(t, id, int64(0))

	got, err := s.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "mem-uuid-1", got.MemoryID)
	assert.Equal(t, "Learn rust", got.Title)
	assert.InDelta(t, 0.7, got.Confidence, 0.001)
	assert.Equal(t, "open", got.Status)
}

func TestInboxMarkStatus(t *testing.T) {
	s := newTestInbox(t)
	ctx := context.Background()
	id, _ := s.Add(ctx, "u", "t", "b", "d", 0.5)
	require.NoError(t, s.MarkStatus(ctx, id, "accepted"))
	got, _ := s.Get(ctx, id)
	assert.Equal(t, "accepted", got.Status)
}

func TestInboxListOpenExcludesClosed(t *testing.T) {
	s := newTestInbox(t)
	ctx := context.Background()
	id1, _ := s.Add(ctx, "u1", "open one", "b", "rust", 0.6)
	id2, _ := s.Add(ctx, "u2", "closed one", "b", "go", 0.7)
	require.NoError(t, s.MarkStatus(ctx, id2, "rejected"))

	open, err := s.ListOpen(ctx)
	require.NoError(t, err)
	require.Len(t, open, 1)
	assert.Equal(t, id1, open[0].ID)
}

func TestInboxCountToday(t *testing.T) {
	s := newTestInbox(t)
	ctx := context.Background()
	_, _ = s.Add(ctx, "u1", "a", "b", "d", 0.5)
	_, _ = s.Add(ctx, "u2", "b", "b", "d", 0.5)
	n, err := s.CountTodayProposals(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
}

func TestInboxRecentDomains(t *testing.T) {
	s := newTestInbox(t)
	ctx := context.Background()
	_, _ = s.Add(ctx, "u1", "a", "b", "rust", 0.5)
	_, _ = s.Add(ctx, "u2", "b", "b", "rust", 0.5)
	_, _ = s.Add(ctx, "u3", "c", "b", "go", 0.5)
	domains, err := s.RecentDomains(ctx, 7)
	require.NoError(t, err)
	assert.Equal(t, 2, domains["rust"])
	assert.Equal(t, 1, domains["go"])
}

func TestInboxStateGetSet(t *testing.T) {
	s := newTestInbox(t)
	ctx := context.Background()
	v, err := s.StateGet(ctx, "missing")
	require.NoError(t, err)
	assert.Empty(t, v)
	require.NoError(t, s.StateSet(ctx, "k", "v"))
	v, _ = s.StateGet(ctx, "k")
	assert.Equal(t, "v", v)
	// Upsert overwrites.
	require.NoError(t, s.StateSet(ctx, "k", "v2"))
	v, _ = s.StateGet(ctx, "k")
	assert.Equal(t, "v2", v)
}

func TestInboxNilSafe(t *testing.T) {
	// A nil store must not panic (mirrors usage Store's nil-guard convention).
	var s *InboxStore
	ctx := context.Background()
	_, err := s.Add(ctx, "u", "t", "b", "d", 0.5)
	assert.Error(t, err)
	_, err = s.ListOpen(ctx)
	require.NoError(t, err) // returns nil, nil
	n, err := s.CountTodayProposals(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}

func TestFormatProposalForChat(t *testing.T) {
	p := InboxProposal{ID: 7, Title: "Do X", Body: "because Y", Confidence: 0.72, Domain: "rust"}
	out := FormatProposalForChat(p)
	assert.Contains(t, out, "[#7]")
	assert.Contains(t, out, "Do X")
	assert.Contains(t, out, "72%")
	assert.Contains(t, out, "/inbox accept 7")
}

// ── reader.go: OpenProposals status filter ──

func TestReaderOpenProposalsFiltersByStatus(t *testing.T) {
	// Mock memory server returning proposals of mixed status.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return 3 proposals: open, accepted, open.
		_, _ = w.Write([]byte(`{"memories":[
			{"id":"a","content":"t1","category":"proposals","metadata":{"status":"open"}},
			{"id":"b","content":"t2","category":"proposals","metadata":{"status":"accepted"}},
			{"id":"c","content":"t3","category":"proposals","metadata":{"status":"open"}}
		]}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "k", "", t.TempDir())
	r := NewReader(c)
	open, err := r.OpenProposals(context.Background())
	require.NoError(t, err)
	require.Len(t, open, 2)
	assert.Equal(t, "a", open[0].ID)
	assert.Equal(t, "c", open[1].ID)
}

func TestReadSnapshotHasSignal(t *testing.T) {
	assert.False(t, ReadSnapshot{}.HasSignal(), "no captures = no signal")
	assert.True(t, ReadSnapshot{Captures: []ListedMemory{{}}}.HasSignal())
}

// ── feedback.go: end-to-end apply (mock memory) ──

func TestFeedbackApplyAccept(t *testing.T) {
	var patchedID, patchedStatus string
	var writtenFB Memory
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPatch:
			patchedID = r.URL.Path[len("/memories/"):]
			body, _ := io.ReadAll(r.Body)
			var m struct {
				Metadata map[string]string `json:"metadata"`
			}
			_ = json.Unmarshal(body, &m)
			patchedStatus = m.Metadata["status"]
			w.WriteHeader(http.StatusOK)
		case http.MethodPost:
			// feedback write
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &writtenFB)
			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "k", "", t.TempDir())
	inbox := newTestInbox(t)
	ctx := context.Background()
	id, _ := inbox.Add(ctx, "mem-uuid-99", "Test prop", "body", "rust", 0.7)

	fh := NewFeedbackHandler(c, inbox)
	reply, err := fh.Apply(ctx, id, VerdictAccept, "looks good")
	require.NoError(t, err)
	assert.Contains(t, reply, "已")
	assert.Contains(t, reply, "accepted")

	// memory PATCH'd to accepted.
	assert.Equal(t, "mem-uuid-99", patchedID)
	assert.Equal(t, "accepted", patchedStatus)
	// feedback memory written with correct shape.
	assert.Equal(t, CategoryFeedback, writtenFB.Category)
	assert.Contains(t, writtenFB.Tags, "brain-feedback")
	assert.Contains(t, writtenFB.Tags, "accept")
	assert.Contains(t, writtenFB.Tags, "rust")
	assert.Equal(t, SourceHuman, writtenFB.Source)
	// inbox cache updated.
	got, _ := inbox.Get(ctx, id)
	assert.Equal(t, "accepted", got.Status)
}

func TestFeedbackApplyRejectWithReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "k", "", t.TempDir())
	inbox := newTestInbox(t)
	ctx := context.Background()
	id, _ := inbox.Add(ctx, "u", "t", "b", "go", 0.5)

	fh := NewFeedbackHandler(c, inbox)
	_, err := fh.Apply(ctx, id, VerdictReject, "not now")
	require.NoError(t, err)
	got, _ := inbox.Get(ctx, id)
	assert.Equal(t, "rejected", got.Status)
}

func TestFeedbackApplyOnNonOpen(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	c := NewClient(srv.URL, "k", "", t.TempDir())
	inbox := newTestInbox(t)
	ctx := context.Background()
	id, _ := inbox.Add(ctx, "u", "t", "b", "go", 0.5)
	require.NoError(t, inbox.MarkStatus(ctx, id, "rejected"))

	fh := NewFeedbackHandler(c, inbox)
	reply, err := fh.Apply(ctx, id, VerdictAccept, "")
	require.NoError(t, err) // not an error — returns a "already X" message
	assert.Contains(t, reply, "已经是")
}

func TestFeedbackApplyUnknownID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	c := NewClient(srv.URL, "k", "", t.TempDir())
	inbox := newTestInbox(t)
	fh := NewFeedbackHandler(c, inbox)
	_, err := fh.Apply(context.Background(), 99999, VerdictAccept, "")
	require.Error(t, err)
}

// ── commands.go: /inbox dispatch ──

func TestInboxCommandRequiresBrain(t *testing.T) {
	// RegisterCommands with nil registry/brain is a safe no-op.
	RegisterCommands(nil, nil)
	// If we reach here without panic, the nil-guard works.
}

// keep io + time referenced (used across the test file).
var _ = io.Discard
var _ = time.Now
