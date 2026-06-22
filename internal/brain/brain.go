package brain

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/naglezhang/makro/internal/config"
)

// Pusher delivers a proposal to the user. The brain is UI-agnostic: it calls
// Push and whoever wired it (TUI main.go / GUI chat_service.go) decides how —
// typically a chat system message + a Bark push. This keeps the brain package
// free of UI/Bark imports.
type Pusher interface {
	Push(localID int64, prop InboxProposal)
}

// Brain is the proactive half of makro. It owns the wake loop: on a schedule
// (cron) or on-demand (/brain wake), it reads memory, asks the LLM for a
// proposal, gates it (confidence + daily cap + domain dedup), and if it passes,
// writes it to memory + inbox and pushes it to the user.
//
// P1 runs the brain as an in-process goroutine (started by main.go / the GUI).
// P2 will move it to a launchd-backed standalone process; this struct's shape
// is designed so that swap is a wiring change, not a rewrite.
type Brain struct {
	cfg      config.BrainConfig
	client   *Client
	reader   *Reader
	proposer *Proposer
	inbox    *InboxStore
	feedback *FeedbackHandler
	pusher   Pusher

	wakeMu    sync.Mutex // serialize wakes — never run two concurrently
	stop      chan struct{}
	wg        sync.WaitGroup  // tracks in-flight runWake goroutines
	runCtx    context.Context // cancelled by Stop; used by all runWake calls
	runCancel context.CancelFunc
}

// NewBrain assembles a Brain from its dependencies. All must be non-nil except
// pusher (a nil pusher = brain runs but never pushes; useful for tests/dry runs).
func NewBrain(cfg config.BrainConfig, client *Client, proposer *Proposer, inbox *InboxStore, pusher Pusher) *Brain {
	b := &Brain{
		cfg:      cfg,
		client:   client,
		reader:   NewReader(client),
		proposer: proposer,
		inbox:    inbox,
		feedback: NewFeedbackHandler(client, inbox),
		pusher:   pusher,
		stop:     make(chan struct{}),
	}
	// runCtx is cancelled by Stop so in-flight runWake goroutines exit promptly.
	// It deliberately does NOT inherit a caller ctx: a request-scoped ctx would
	// cancel every memory read inside runWake and yield empty snapshots.
	b.runCtx, b.runCancel = context.WithCancel(context.Background())
	return b
}

// goWake launches one runWake on the brain-scoped runCtx, tracked on wg so Stop
// can wait for it. runWake's internal TryLock drops concurrent wakes.
func (b *Brain) goWake(trigger string) {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		b.runWake(b.runCtx, trigger)
	}()
}

// Feedback exposes the feedback handler for /inbox commands to call.
func (b *Brain) Feedback() *FeedbackHandler { return b.feedback }

// Inbox exposes the inbox store for /inbox commands to list proposals.
func (b *Brain) Inbox() *InboxStore { return b.inbox }

// Run starts the cron-driven wake loop. Blocks until ctx is cancelled. Each
// iteration sleeps until the next cron tick (default 08:00 daily). The loop is
// purely cron in P1 — event-triggered candidate accumulation is P2 (it needs
// the standalone daemon + memory subscribe, neither of which exist yet).
//
// /brain wake bypasses this loop by calling WakeNow directly.
func (b *Brain) Run(ctx context.Context) {
	if !b.cfg.Enabled {
		log.Printf("[brain] disabled, not running wake loop")
		return
	}
	ticker := b.newCronTicker(b.cfg.CronTime)
	defer ticker.Stop()
	log.Printf("[brain] wake loop started (cron=%s)", b.cfg.CronTime)

	for {
		select {
		case <-ctx.Done():
			log.Printf("[brain] wake loop stopped")
			return
		case <-b.stop:
			log.Printf("[brain] wake loop stopped")
			return
		case <-ticker.C:
			b.goWake("cron")
		}
	}
}

// Stop signals the wake loop to exit and waits for any in-flight wake to
// finish. Safe to call multiple times.
func (b *Brain) Stop() {
	select {
	case <-b.stop:
	default:
		close(b.stop)
	}
	// Cancel in-flight wakes (their provider/memory calls honor ctx) and join
	// them so a process exit can't truncate a proposal write mid-flight.
	if b.runCancel != nil {
		b.runCancel()
	}
	b.wg.Wait()
}

// WakeNow triggers an immediate wake in a goroutine and returns instantly. Used
// by the /brain wake command so the chat reply doesn't block on the LLM call
// (which can take 10-60s). The proposal, if any, arrives as a push later.
func (b *Brain) WakeNow(_ context.Context) {
	// Runs on b.runCtx (cancelled by Stop) rather than a per-call background
	// ctx + watcher goroutine. goWake tracks it on wg so Stop can join it.
	b.goWake("manual")
}

// runWake is one full brain cycle. Serialized by wakeMu so cron + manual can't
// double-fire. Steps per BRAIN_DESIGN §2.4 wake():
//  1. Replay any dead-lettered writes from last time.
//  2. Read R1-R5.
//  3. Abort if no signal (cold start — don't push garbage).
//  4. Rate-limit: daily cap.
//  5. Propose (LLM).
//  6. Gate: confidence threshold + domain dedup.
//  7. Write proposal to memory (truth) + inbox (cache).
//  8. Push.
func (b *Brain) runWake(ctx context.Context, trigger string) {
	// Serialize. Non-blocking acquire: if a wake is already running, skip this
	// one (cron firing during a slow manual wake is fine to drop).
	if !b.wakeMu.TryLock() {
		log.Printf("[brain] wake (%s) skipped — another wake in progress", trigger)
		return
	}
	defer b.wakeMu.Unlock()

	log.Printf("[brain] wake (%s) start", trigger)
	start := time.Now()

	// 1. Replay dead letters from prior failed writes.
	if n, err := b.client.ReplayDeadLetter(ctx); err != nil {
		log.Printf("[brain] dead-letter replay error: %v", err)
	} else if n > 0 {
		log.Printf("[brain] replayed %d dead-lettered writes", n)
	}

	// 2. Read memory.
	snap := b.reader.ReadAll(ctx)
	if len(snap.ReadErrors) > 0 {
		log.Printf("[brain] wake read errors (continuing with partial): %v", snap.ReadErrors)
	}

	// 3. Cold-start guard.
	if !snap.HasSignal() {
		log.Printf("[brain] wake (%s) — no captures (cold start), skipping", trigger)
		b.notifySkip(trigger, "memory 里还没有足够的 capture 信号，brain 跳过本轮（先用一段时间让 capture 攒起来）")
		return
	}

	// 4. Daily cap.
	count, err := b.inbox.CountTodayProposals(ctx)
	if err != nil {
		log.Printf("[brain] daily-cap check failed: %v (proceeding)", err)
	} else if count >= b.cfg.DailyProposalCap {
		log.Printf("[brain] wake (%s) — daily cap reached (%d/%d), skipping", trigger, count, b.cfg.DailyProposalCap)
		b.notifySkip(trigger, fmt.Sprintf("今天已经推过 %d 条 proposal（上限 %d），brain 跳过本轮", count, b.cfg.DailyProposalCap))
		return
	}

	// 5. Propose.
	prop, err := b.proposer.Propose(ctx, snap)
	if err != nil {
		log.Printf("[brain] propose failed: %v", err)
		b.notifySkip(trigger, "brain 生成 proposal 失败："+err.Error())
		return
	}

	// 6. Gate.
	if prop.Confidence < b.cfg.ConfidenceThreshold {
		log.Printf("[brain] proposal gated: confidence %.2f < threshold %.2f (title=%q)",
			prop.Confidence, b.cfg.ConfidenceThreshold, prop.Title)
		b.notifySkip(trigger, fmt.Sprintf("brain 生成了一个 proposal「%s」但 confidence %.0f%% 低于阈值 %.0f%%，没推。", prop.Title, prop.Confidence*100, b.cfg.ConfidenceThreshold*100))
		return
	}
	// Domain dedup: skip if this domain already has a proposal in last 7 days.
	if b.domainRecentlyPushed(ctx, prop.Domain) {
		log.Printf("[brain] proposal gated: domain %q recently pushed", prop.Domain)
		b.notifySkip(trigger, fmt.Sprintf("brain 生成了「%s」但领域 %s 最近 7 天已推过，跳过避免重复。", prop.Title, prop.Domain))
		return
	}

	// 7. Write to memory (truth) + inbox (cache).
	memID, err := b.writeProposalToMemory(ctx, prop, trigger)
	if err != nil {
		log.Printf("[brain] write proposal to memory failed: %v (not pushing)", err)
		b.notifySkip(trigger, "brain 写 proposal 到 memory 失败："+err.Error())
		return
	}
	localID, err := b.inbox.Add(ctx, memID, prop.Title, prop.Body, prop.Domain, prop.Confidence)
	if err != nil {
		log.Printf("[brain] inbox add failed (memory has it, push will use memory_id): %v", err)
	}

	// 8. Push.
	if b.pusher != nil {
		b.pusher.Push(localID, InboxProposal{
			ID: localID, MemoryID: memID, Title: prop.Title, Body: prop.Body,
			Confidence: prop.Confidence, Domain: prop.Domain, Status: "open", CreatedAt: time.Now(),
		})
	}

	log.Printf("[brain] wake (%s) done in %s — pushed [#%d] %q (conf %.2f, domain %s)",
		trigger, time.Since(start).Round(time.Millisecond), localID, prop.Title, prop.Confidence, prop.Domain)
}

// writeProposalToMemory POSTs a category=proposals record and returns the new
// memory ID. Shape per BRAIN_DESIGN §9.3(b): status/confidence/domain in
// metadata, brain-proposal + domain in tags.
func (b *Brain) writeProposalToMemory(ctx context.Context, p *Proposal, trigger string) (string, error) {
	mem := Memory{
		Category: CategoryProposals,
		Scope:    "global",
		Tags:     []string{"brain-proposal", p.Domain},
		Source:   SourceMakroBrain,
		Metadata: map[string]any{
			"status":     "open",
			"confidence": p.Confidence,
			"domain":     p.Domain,
			"trigger":    trigger,
			"reason":     p.Reason,
		},
		Content: fmt.Sprintf("%s\n\n%s\n\n（reason: %s）", p.Title, p.Body, p.Reason),
	}
	// WriteProposal returns nil on success but the memory ID comes back in the
	// response body, which our client doesn't currently capture. To get the ID,
	// do the POST directly via a helper that reads the body.
	return b.client.writeAndReturnID(ctx, mem)
}

// domainRecentlyPushed checks if `domain` has any proposal in the last 7 days
// (inbox cache). Empty domain → not blocked (don't gate on missing metadata).
func (b *Brain) domainRecentlyPushed(ctx context.Context, domain string) bool {
	if domain == "" {
		return false
	}
	recent, err := b.inbox.RecentDomains(ctx, 7)
	if err != nil {
		log.Printf("[brain] domain-dedup check failed: %v (not blocking)", err)
		return false
	}
	return recent[domain] > 0
}

// notifySkip sends a system message when a wake produces no push, so the user
// (who triggered /brain wake) isn't left wondering. Silent for cron wakes in
// steady state — only manual wakes get these "skip" explanations to confirm
// the command ran. For cron, we log only.
func (b *Brain) notifySkip(trigger, reason string) {
	if trigger != "manual" {
		return
	}
	if b.pusher == nil {
		return
	}
	b.pusher.Push(0, InboxProposal{
		Title: "（本轮 brain 没有推 proposal）", Body: reason, Status: "skip", CreatedAt: time.Now(),
	})
}

// newCronTicker returns a ticker that fires once daily at the configured HH:MM.
// P1 implementation: sleep until next HH:MM, then tick every 24h. The brain
// runs in-process so this is fine; P2's standalone daemon will reuse this.
func (b *Brain) newCronTicker(hhmm string) *time.Ticker {
	dur := durationUntilNext(hhmm)
	t := time.NewTicker(dur)
	log.Printf("[brain] first cron wake in %s (at %s)", dur.Round(time.Second), hhmm)
	// After the first fire, reset to 24h. time.Ticker fires at dur then every
	// dur; we want first-fire-at-HH:MM then every-24h, so we rebuild after the
	// first tick. Simplest: just use 24h and accept up to 24h drift is wrong —
	// instead we fire at the computed delay then swap to 24h via a reset goroutine.
	go func() {
		select {
		case <-t.C:
			t.Reset(24 * time.Hour)
		case <-b.stop:
			// Run is exiting (its defer will ticker.Stop) — exit so this
			// goroutine doesn't block forever on a never-firing channel.
		}
	}()
	return t
}

// durationUntilNext returns the duration from now until the next HH:MM (local).
// If HH:MM is malformed, defaults to 8 hours from now (so we still wake).
func durationUntilNext(hhmm string) time.Duration {
	now := time.Now()
	var hh, mm int
	if _, err := fmt.Sscanf(hhmm, "%d:%d", &hh, &mm); err != nil || hh > 23 || mm > 59 {
		return 8 * time.Hour
	}
	next := time.Date(now.Year(), now.Month(), now.Day(), hh, mm, 0, 0, now.Location())
	if !next.After(now) {
		next = next.Add(24 * time.Hour)
	}
	return time.Until(next)
}
