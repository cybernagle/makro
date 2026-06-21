package brain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/naglezhang/makro/internal/config"
)

// CaptureSink is the single entry point for "the user typed something". It is
// safe to call from the chat hot path (TUI processOrchestratorInput, GUI
// SendMessage, the notifier's OnCapture callback). The contract is absolute:
//
//	Capture() MUST return immediately and MUST NEVER error into the caller.
//
// It does three things, all off the hot path:
//  1. Filter noise (agent echoes, command tags, tool-output wrappers) — ported
//     verbatim from ~/.claude/hooks/memory-stop.sh. The fit model's entire
//     quality rests on this table (BRAIN_DESIGN §11 R3).
//  2. Dedup via an LRU keyed on the first 64 chars of the prompt. A user
//     re-sending the same message within the window is dropped.
//  3. Enqueue onto a buffered channel; a worker goroutine writes to memory
//     (Client.WriteCapture) with retry→dead-letter. Channel full → drop oldest.
//
// If the sink is disabled (config.Brain.CaptureEnabled == false) or the brain
// is off, Capture is a no-op.
type CaptureSink struct {
	enabled bool
	client  *Client
	ch      chan captureJob
	wg      sync.WaitGroup
	stop    chan struct{}

	// dedup: recent prompt-prefix hashes. Rolled our own (capacity-bound map
	// with FIFO eviction) rather than pull in an LRU dep — the access pattern
	// is "seen recently → drop", so FIFO is good enough and zero-dependency.
	dedup *dedupCache
}

type captureJob struct {
	source  string // claude | copilot | makro
	session string
	prompt  string
	cwd     string
}

// capture channel depth. Per BRAIN_DESIGN §3.6: >256 drops oldest. 256 is
// generous — at one user message per minute that's 4+ hours of headroom even
// if the memory daemon is down the whole time.
const captureChannelDepth = 256

// dedupCacheSize bounds the LRU. 512 distinct prefixes is more than a day's
// unique user messages for a single developer.
const dedupCacheSize = 512

// NewCaptureSink constructs the sink and starts its worker. If disabled, it
// returns a sink whose Capture() is a no-op (no worker started). The caller
// must call Stop on shutdown to flush.
func NewCaptureSink(cfg config.BrainConfig, client *Client) *CaptureSink {
	s := &CaptureSink{
		enabled: cfg.Enabled && cfg.CaptureEnabled,
		client:  client,
		ch:      make(chan captureJob, captureChannelDepth),
		stop:    make(chan struct{}),
	}
	if s.enabled {
		s.dedup = newDedupCache(dedupCacheSize)
		s.wg.Add(1)
		go s.worker()
		log.Printf("[brain] capture sink enabled (endpoint=%s)", cfg.MemoryEndpoint)
	} else {
		log.Printf("[brain] capture sink disabled (enabled=%v capture=%v)", cfg.Enabled, cfg.CaptureEnabled)
	}
	return s
}

// Capture enqueues one user message. Never blocks, never errors. Safe to call
// when disabled (no-op) or when the sink is stopped (drops).
//
// Control input is NOT captured: slash commands (/brain wake), @mentions
// (@session msg), and monitor commands (&session) are routing/control, not
// user ideas. Capturing them pollutes R1's idea mine with noise. Only genuine
// natural-language input (things the user actually said/thought) is captured.
func (s *CaptureSink) Capture(source, session, prompt, cwd string) {
	if !s.enabled || !isCaptureable(prompt) {
		return
	}

	cleaned := filterNoise(prompt)
	if cleaned == "" {
		return // entire message was noise
	}
	if isDuplicate(s.dedup, cleaned) {
		return
	}

	job := captureJob{source: source, session: session, prompt: cleaned, cwd: cwd}
	select {
	case s.ch <- job:
	default:
		// Channel full → drop oldest, then enqueue. Per §3.6: chat realtime
		// priority > capture completeness. We log the drop count.
		select {
		case dropped := <-s.ch:
			log.Printf("[brain] capture channel full, dropping oldest (session=%s)", dropped.session)
		default:
		}
		select {
		case s.ch <- job:
		default:
			// Still full after one drain — give up on this one.
			log.Printf("[brain] capture channel still full, dropping new message")
		}
	}
}

// worker drains the channel and writes to memory. On failure it retries with
// exponential backoff (1s, 4s, 16s) then lets writeWithFallback dead-letter.
func (s *CaptureSink) worker() {
	defer s.wg.Done()
	for {
		select {
		case <-s.stop:
			return
		case job := <-s.ch:
			s.writeWithRetry(job)
		}
	}
}

func (s *CaptureSink) writeWithRetry(job captureJob) {
	mem := buildCaptureMemory(job)
	backoffs := []time.Duration{1 * time.Second, 4 * time.Second, 16 * time.Second}
	ctx := context.Background()

	var lastErr error
	for attempt, wait := range append([]time.Duration{0}, backoffs...) {
		if wait > 0 {
			select {
			case <-s.stop:
				return
			case <-time.After(wait):
			}
		}
		if err := s.client.WriteCapture(ctx, mem); err != nil {
			lastErr = err
			log.Printf("[brain] capture write attempt %d failed: %v", attempt+1, err)
			continue
		}
		return // success
	}
	// All retries exhausted. WriteCapture itself never returns a fatal error
	// (it dead-letters internally), so reaching here means the dead-letter also
	// failed — data is lost. Log loudly; chat is unaffected.
	log.Printf("[brain] capture permanently failed after retries (session=%s): %v", job.session, lastErr)
}

// buildCaptureMemory assembles the Memory record per BRAIN_DESIGN §9.3(a):
// category=capture, tags=[chat-capture, <session>], source, role=user, project
// from cwd, metadata={session, prompt_hash}.
func buildCaptureMemory(job captureJob) Memory {
	project := projectFromCwd(job.cwd)
	tags := []string{"chat-capture"}
	if job.session != "" {
		tags = append(tags, sanitizeTag(job.session))
	}
	scope := "agent:" + job.source
	if job.source == "" {
		scope = "agent:makro"
	}
	return Memory{
		Content:  truncate(job.prompt, 500),
		Category: CategoryCapture,
		Scope:    scope,
		Tags:     tags,
		Source:   job.source,
		Project:  project,
		Role:     "user",
		Metadata: map[string]any{
			"session":     job.session,
			"prompt_hash": hashPrefix(job.prompt, 64),
		},
	}
}

// Stop signals the worker to exit and waits for it. Pending channel items are
// not flushed (acceptable: they're best-effort). Safe to call multiple times.
func (s *CaptureSink) Stop() {
	select {
	case <-s.stop:
		return
	default:
		close(s.stop)
	}
	s.wg.Wait()
}

// ── noise filter (ported verbatim from ~/.claude/hooks/memory-stop.sh) ──
//
// The shell hook skips any user message whose content contains one of these
// sentinel substrings — they're Claude Code's injected tool/command wrappers,
// not real user intent. This filter is the #1 determinant of fit quality
// (BRAIN_DESIGN §11 R3). Do not "simplify" it without re-reading stop-hook.sh.
var noiseSentinels = []string{
	"<local-command-caveat>",
	"<bash-input>",
	"<bash-stdout>",
	"<command-name>",
	"<local-command-stdout>",
	"<local-command-stderr>",
}

// isCaptureable reports whether the input is a genuine user idea worth
// capturing, as opposed to control input. Slash commands (/brain wake),
// @mentions (@session msg), and monitor commands (&session) are routing — they
// tell makro what to DO, not what the user THINKS. Capturing them would fill
// R1's idea mine with "inbox accept 1" and "switch" garbage.
func isCaptureable(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return false
	}
	// Control prefixes — these are commands, not ideas.
	switch {
	case strings.HasPrefix(t, "/"), // slash command (/brain, /inbox, /switch...)
		strings.HasPrefix(t, "@"), // @mention → route to a session
		strings.HasPrefix(t, "&"): // &session → background monitor
		return false
	}
	return true
}

func filterNoise(s string) string {
	// Drop the whole message if it's a wrapped tool/command payload.
	for _, sentinel := range noiseSentinels {
		if strings.Contains(s, sentinel) {
			return ""
		}
	}
	// Trim and drop very short prompts (<5 chars) — they're usually typos or
	// fragments. stop-hook.sh uses the same 5-char floor.
	cleaned := strings.TrimSpace(s)
	if len(cleaned) < 5 {
		return ""
	}
	return cleaned
}

// isDuplicate reports whether this prompt's 64-char-prefix hash was seen
// recently. nil cache (disabled sink) → never a dup.
func isDuplicate(cache *dedupCache, s string) bool {
	if cache == nil {
		return false
	}
	return cache.seen(hashPrefix(s, 64))
}

// dedupCache is a capacity-bound set of strings with FIFO eviction. It exists
// to avoid adding github.com/hashicorp/golang-lru as a dependency for a 512-key
// hot-path check. FIFO (not true LRU) is fine: a re-sent message arrives soon
// after its first send, so recency ≈ insertion order here.
type dedupCache struct {
	mu    sync.Mutex
	cap   int
	keys  map[string]struct{}
	order []string // FIFO queue for eviction
}

func newDedupCache(capacity int) *dedupCache {
	return &dedupCache{cap: capacity, keys: make(map[string]struct{}, capacity)}
}

// seen reports whether key was already present, recording it if not. Evicts the
// oldest entry ONLY when inserting a new key at capacity — a cache hit must not
// trigger eviction (else a stream of misses could evict entries still wanted).
func (d *dedupCache) seen(key string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.keys[key]; ok {
		return true
	}
	// New key: evict oldest if we're at capacity, then insert.
	if len(d.order) >= d.cap {
		oldest := d.order[0]
		d.order = d.order[1:]
		delete(d.keys, oldest)
	}
	d.keys[key] = struct{}{}
	d.order = append(d.order, key)
	return false
}

func hashPrefix(s string, n int) string {
	if len(s) > n {
		s = s[:n]
	}
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// projectFromCwd returns the cwd basename, or "" if cwd is empty. This populates
// the new project field (M1) so captures can be grouped by originating repo.
func projectFromCwd(cwd string) string {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return ""
	}
	return filepath.Base(cwd)
}

// sanitizeTag strips characters that would break comma-separated tag parsing
// (memory-cli splits tags on ","). Session names are usually clean but defend
// anyway.
func sanitizeTag(s string) string {
	s = strings.ReplaceAll(s, ",", "_")
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return s
}

// ensure unused-import safety for os (used via filepath in projectFromCwd path
// on some builds); keep the import list honest.
var _ = os.DevNull
