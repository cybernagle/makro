package brain

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/naglezhang/makro/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── noise filter (landmine 2: capture signal cleanliness) ──

func TestFilterNoiseDropsWrappedCommands(t *testing.T) {
	// Each sentinel from memory-stop.sh must zero the whole message.
	for _, sentinel := range noiseSentinels {
		got := filterNoise("real text " + sentinel + " more text")
		assert.Empty(t, got, "sentinel %q should drop the message", sentinel)
	}
}

func TestFilterNoiseKeepsRealPrompt(t *testing.T) {
	assert.Equal(t, "fix the auth bug", filterNoise("fix the auth bug"))
}

// ── isCaptureable: control input must not be captured ──

func TestIsCaptureableRejectsControlInput(t *testing.T) {
	// Slash commands, mentions, monitors are routing — not ideas.
	assert.False(t, isCaptureable("/brain wake"))
	assert.False(t, isCaptureable("/inbox accept 1"))
	assert.False(t, isCaptureable("@auth-service run the tests"))
	assert.False(t, isCaptureable("&worker"))
	assert.False(t, isCaptureable(""))
	assert.False(t, isCaptureable("   "))
}

func TestIsCaptureableAcceptsRealIdeas(t *testing.T) {
	assert.True(t, isCaptureable("我想学一下 rust"))
	assert.True(t, isCaptureable("考虑把 brain 模块再完善一下"))
	assert.True(t, isCaptureable("hello there"))
}

func TestFilterNoiseDropsShortMessages(t *testing.T) {
	// stop-hook.sh's 5-char floor.
	assert.Empty(t, filterNoise("hi"))
	assert.Empty(t, filterNoise("   "))
	assert.Empty(t, filterNoise("abc"))
	assert.NotEmpty(t, filterNoise("hello")) // exactly 5 → kept
}

func TestFilterNoiseTrimsWhitespace(t *testing.T) {
	assert.Equal(t, "real message", filterNoise("   real message\n\n"))
}

// ── dedup cache ──

func TestDedupCacheSeenThenUnseen(t *testing.T) {
	c := newDedupCache(4)
	assert.False(t, c.seen("aaa"), "first sight not a dup")
	assert.True(t, c.seen("aaa"), "second sight is a dup")
	assert.False(t, c.seen("bbb"))
	assert.True(t, c.seen("bbb"))
}

func TestDedupCacheEvictsOldest(t *testing.T) {
	// FIFO at cap=2: insert a, b, then c evicts a. Re-seeing a later would
	// evict b (that's correct FIFO behavior), so we only assert the post-c
	// state here without a re-see of a.
	c := newDedupCache(2)
	c.seen("a")
	c.seen("b")
	c.seen("c") // evicts "a" → keys={b,c}
	assert.True(t, c.seen("b"), "b still present after c's eviction of a")
	assert.True(t, c.seen("c"))
	// A NEW key at capacity evicts the oldest (b).
	c.seen("d") // evicts "b" → keys={c,d}
	assert.False(t, c.seen("b"), "b evicted after inserting d")
	assert.True(t, c.seen("d"))
}

func TestIsDuplicateNilCacheNeverDup(t *testing.T) {
	assert.False(t, isDuplicate(nil, "anything"))
}

// ── buildCaptureMemory shape (acceptance #1: full-field write) ──

func TestBuildCaptureMemoryShape(t *testing.T) {
	m := buildCaptureMemory(captureJob{
		source:  SourceClaude,
		session: "auth-service",
		prompt:  "refactor the login flow",
		cwd:     "/Users/me/code/auth-service",
	})
	assert.Equal(t, CategoryCapture, m.Category)
	assert.Equal(t, SourceClaude, m.Source)
	assert.Equal(t, "user", m.Role)
	assert.Equal(t, "agent:claude", m.Scope)
	assert.Equal(t, "auth-service", m.Project)
	assert.Contains(t, m.Tags, "chat-capture")
	assert.Contains(t, m.Tags, "auth-service")
	require.NotNil(t, m.Metadata)
	assert.Equal(t, "auth-service", m.Metadata["session"])
	assert.NotEmpty(t, m.Metadata["prompt_hash"])
}

func TestBuildCaptureMemoryTagsSanitized(t *testing.T) {
	// Session name with a comma must not break tag parsing downstream.
	m := buildCaptureMemory(captureJob{session: "weird,name", source: SourceClaude, prompt: "hello there"})
	for _, tag := range m.Tags {
		assert.NotContains(t, tag, ",")
	}
}

func TestBuildCaptureMemoryTruncates(t *testing.T) {
	long := strings.Repeat("x", 1000)
	m := buildCaptureMemory(captureJob{source: SourceMakro, prompt: long, cwd: "/p"})
	assert.LessOrEqual(t, len(m.Content), 503) // 500 + "..."
}

// ── memory client: REST success ──

func TestWriteCaptureRESTSuccess(t *testing.T) {
	var received Memory
	var authHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		require.Equal(t, "/memories", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&received))
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "secret-key", "", t.TempDir())
	err := c.WriteCapture(context.Background(), Memory{
		Content: "test idea", Category: CategoryCapture, Source: SourceClaude, Role: "user",
		Project: "x", Metadata: map[string]any{"session": "s"},
	})
	require.NoError(t, err)
	assert.Equal(t, "Bearer secret-key", authHeader)
	assert.Equal(t, "test idea", received.Content)
	assert.Equal(t, "x", received.Project)
	assert.Equal(t, "user", received.Role)
}

func TestWriteCaptureRejectsWrongCategory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not call REST for wrong category")
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "k", "", t.TempDir())
	err := c.WriteCapture(context.Background(), Memory{Content: "x", Category: "knowledge"})
	require.Error(t, err)
}

// ── memory client: fallback chain (acceptance #2: daemon down → CLI → dead-letter) ──

// writeCLI looks for the binary at cliPath. We point it at a fake script.
func TestWriteCaptureFallsBackToCLIWhenRESTDown(t *testing.T) {
	dir := t.TempDir()
	// Fake memory CLI that records its argv and exits 0.
	cliPath := filepath.Join(dir, "memory")
	script := "#!/bin/sh\necho \"argv: $@\" > " + filepath.Join(dir, "argv.txt") + "\nexit 0\n"
	require.NoError(t, os.WriteFile(cliPath, []byte(script), 0o755))

	// REST endpoint that's down: use a closed listener.
	c := NewClient("http://127.0.0.1:1", "", cliPath, dir)
	err := c.WriteCapture(context.Background(), Memory{
		Content: "fallback idea", Category: CategoryCapture, Source: SourceClaude,
		Tags: []string{"chat-capture"}, Scope: "agent:claude",
	})
	require.NoError(t, err)

	// CLI fallback is lossy (no project/role/metadata) but content survives.
	argv, err := os.ReadFile(filepath.Join(dir, "argv.txt"))
	require.NoError(t, err)
	assert.Contains(t, string(argv), "fallback idea")
	assert.Contains(t, string(argv), "--category capture")
	assert.Contains(t, string(argv), "--source claude")

	// No dead-letter entry: CLI succeeded.
	assert.Equal(t, 0, c.DeadLetterCount())
}

func TestWriteCaptureDeadLettersWhenAllPathsFail(t *testing.T) {
	dir := t.TempDir()
	// REST down, CLI nonexistent.
	c := NewClient("http://127.0.0.1:1", "k", "/nonexistent/memory-cli", dir)

	err := c.WriteCapture(context.Background(), Memory{
		Content: "lost idea", Category: CategoryCapture, Source: SourceClaude,
	})
	// writeWithFallback never returns a fatal error to the caller — it dead-letters.
	require.NoError(t, err)
	assert.Equal(t, 1, c.DeadLetterCount(), "should be one dead-lettered entry")
}

func TestDeadLetterReplaySucceeds(t *testing.T) {
	dir := t.TempDir()
	// First: all paths fail → dead-letter.
	c := NewClient("http://127.0.0.1:1", "k", "/nonexistent/memory-cli", dir)
	require.NoError(t, c.WriteCapture(context.Background(), Memory{
		Content: "queued idea", Category: CategoryCapture, Source: SourceClaude,
	}))
	require.Equal(t, 1, c.DeadLetterCount())

	// Now stand up a working REST server and replay.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	c2 := NewClient(srv.URL, "k", "", dir) // same deadLetterPath (same dir)
	n, err := c2.ReplayDeadLetter(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, 0, c2.DeadLetterCount(), "dead-letter file cleared after replay")
	_, statErr := os.Stat(c2.deadLetterPath)
	assert.True(t, os.IsNotExist(statErr), "dead-letter file removed")
}

// ── capture sink: never blocks (landmine 3: hot-path safety) ──

// sinkWithClosedClient returns a sink whose client can never succeed (REST down,
// no CLI) — every write dead-letters, slowly (retries: 0/1s/4s/16s). We use it
// to prove Capture() returns instantly regardless.
func sinkWithClosedClient(t *testing.T) (*CaptureSink, *Client) {
	t.Helper()
	dir := t.TempDir()
	c := NewClient("http://127.0.0.1:1", "k", "/nonexistent/memory", dir)
	cfg := config.DefaultBrainConfig()
	s := NewCaptureSink(cfg, c)
	return s, c
}

func TestCaptureNeverBlocks(t *testing.T) {
	s, _ := sinkWithClosedClient(t)
	defer s.Stop()

	// Fire many captures back-to-back. Capture must return in microseconds each;
	// if it blocked on the worker, this loop would take 21s+ (retries).
	start := time.Now()
	const n = 500
	for i := 0; i < n; i++ {
		// Distinct prefixes so dedup doesn't drop them; channel fills, oldest dropped.
		s.Capture(SourceMakro, "s", strings.Repeat("a", 60)+string(rune(i)), "/p")
	}
	elapsed := time.Since(start)
	assert.Less(t, elapsed, 500*time.Millisecond, "Capture loop of %d must be sub-second, got %v", n, elapsed)
}

func TestCaptureDisabledIsNoOp(t *testing.T) {
	dir := t.TempDir()
	c := NewClient("http://127.0.0.1:1", "k", "", dir)
	cfg := config.BrainConfig{Enabled: false, CaptureEnabled: false} // disabled
	s := NewCaptureSink(cfg, c)
	defer s.Stop()

	// The worker isn't running when disabled, so nothing gets dead-lettered.
	s.Capture(SourceMakro, "s", "some real message here", "/p")
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 0, c.DeadLetterCount(), "disabled sink must not write or dead-letter")
}

func TestCaptureDropsNoiseAndDup(t *testing.T) {
	s, _ := sinkWithClosedClient(t)
	defer s.Stop()

	// Noise → dropped before enqueue.
	s.Capture(SourceMakro, "s", "<bash-input>echo hi</bash-input>", "/p")
	// Dup → second identical dropped.
	s.Capture(SourceMakro, "s", "fix the auth bug now please", "/p")
	s.Capture(SourceMakro, "s", "fix the auth bug now please", "/p")
	// Give the worker a moment, then check dead-letter count. The noise msg is
	// dropped entirely; the dup's second instance is dropped; only the first
	// real message should have been attempted (and dead-lettered).
	time.Sleep(50 * time.Millisecond)
	// Exactly one dead-letter entry expected (the first real message; noise
	// never enqueued, dup never enqueued).
	assert.Equal(t, 1, s.client.DeadLetterCount())
}

// ── config integration ──

func TestCaptureSinkFromConfig(t *testing.T) {
	cfg := config.DefaultBrainConfig()
	cfg.MemoryEndpoint = "http://127.0.0.1:1" // down, but sink construction must still work
	c := NewClient(cfg.MemoryEndpoint, cfg.MemoryAPIKey, cfg.MemoryCLIPath, t.TempDir())
	s := NewCaptureSink(cfg, c)
	defer s.Stop()
	assert.NotNil(t, s)
}

// ── P1 methods (List / PatchMetadata / WriteProposal / WriteFeedback) ──

func TestListParsesMemories(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/memories", r.URL.Path)
		// Verify filter params forward correctly.
		assert.Equal(t, "capture", r.URL.Query().Get("category"))
		assert.Equal(t, "chat-capture,a", r.URL.Query().Get("tags"))
		assert.Equal(t, "10", r.URL.Query().Get("limit"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"memories":[{"id":"abc","content":"x","category":"capture","metadata":{"session":"s"},"created_at":"2026-06-19T00:00:00Z"}]}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "k", "", t.TempDir())
	got, err := c.List(context.Background(), Query{
		Category: "capture", Tags: []string{"chat-capture", "a"}, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "abc", got[0].ID)
	assert.Equal(t, "s", got[0].Metadata["session"])
}

func TestListNullMemoriesReturnsEmpty(t *testing.T) {
	// memory-cli returns {"memories":null} when nothing matches.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"memories":null}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "k", "", t.TempDir())
	got, err := c.List(context.Background(), Query{Category: "capture"})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestPatchMetadataMerges(t *testing.T) {
	var gotPath, gotMethod, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "k", "", t.TempDir())
	err := c.PatchMetadata(context.Background(), "abc-123", map[string]any{"status": "accepted"})
	require.NoError(t, err)
	assert.Equal(t, http.MethodPatch, gotMethod)
	assert.Equal(t, "/memories/abc-123", gotPath)
	assert.Contains(t, gotBody, `"status":"accepted"`)
}

func TestWriteProposalValidatesCategory(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "k", "", t.TempDir())
	err := c.WriteProposal(context.Background(), Memory{Content: "x", Category: "wrong"})
	assert.Error(t, err)
}

func TestWriteFeedbackValidatesCategory(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "k", "", t.TempDir())
	err := c.WriteFeedback(context.Background(), Memory{Content: "x", Category: "wrong"})
	assert.Error(t, err)
}

// ── source semantics (documented contract) ──

func TestSourcesAreDistinct(t *testing.T) {
	// These are string consts referenced by callers; ensure they don't collide.
	seen := map[string]bool{}
	for _, s := range []string{SourceClaude, SourceCopilot, SourceMakro, SourceMakroBrain, SourceHuman} {
		assert.False(t, seen[s], "source %q duplicated", s)
		seen[s] = true
	}
}

// keep io imported (used by httptest internals in some toolchains).
var _ = io.Discard
