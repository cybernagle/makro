package brain

import (
	"context"
	"fmt"
	"log"
	"time"
)

// Verdict is the user's reaction to a proposal. These map to:
//   - feedback memory tags (brain-feedback,<verdict>,<domain>) for R4 aggregation
//   - proposal metadata.status via PATCH (open→accepted/rejected/expired)
type Verdict string

const (
	VerdictAccept Verdict = "accept"
	VerdictReject Verdict = "reject"
	// VerdictIgnore is assigned by the brain's expiry sweep (not a user action).
	// User-initiated feedback is only accept/reject in P1.
	VerdictIgnore Verdict = "ignore"
)

// FeedbackHandler closes the loop on a proposal: writes the user's reaction
// back to BOTH memory (truth source — PATCH proposal status + a new feedback
// memory for R4) and the local inbox cache (so /inbox reflects it immediately).
//
// This is the closed loop's lifeblood (BRAIN_DESIGN §1.7): the feedback memory
// is what the next wake's R4 reads, so its quality directly determines how well
// the brain fits. Each feedback record carries verdict + reason + proposal_id,
// letting future wakes compute per-domain accept rates.
type FeedbackHandler struct {
	client *Client
	inbox  *InboxStore
}

// NewFeedbackHandler builds a handler over the given client + inbox.
func NewFeedbackHandler(c *Client, inbox *InboxStore) *FeedbackHandler {
	return &FeedbackHandler{client: c, inbox: inbox}
}

// Apply records the user's verdict on a proposal. localID is the inbox's short
// numeric ID (from /inbox). It:
//  1. Looks up the proposal (inbox.Get) to get the memory_id + domain + title.
//  2. PATCHes the proposal's metadata.status in memory (truth source).
//  3. Writes a category=feedback memory (the R4 signal for next wake).
//  4. Updates the inbox cache status (so /inbox shows it gone).
//
// reason is optional (user may give one on reject). Empty reason is fine.
// Returns a human-readable confirmation string for the chat reply.
func (h *FeedbackHandler) Apply(ctx context.Context, localID int64, verdict Verdict, reason string) (string, error) {
	// 1. Look up the proposal.
	prop, err := h.inbox.Get(ctx, localID)
	if err != nil {
		return "", fmt.Errorf("feedback: proposal #%d not found: %w", localID, err)
	}
	if prop.Status != "open" {
		return fmt.Sprintf("proposal #%d 已经是 %s 状态，不能再改", localID, prop.Status), nil
	}

	// 2. PATCH the proposal's status in memory (truth source).
	patchErr := h.client.PatchMetadata(ctx, prop.MemoryID, map[string]any{
		"status":        string(verdict) + "ed", // accepted / rejected
		"verdict_at":    time.Now().UTC().Format(time.RFC3339),
		"reject_reason": reason,
	})
	if patchErr != nil {
		// Non-fatal: the feedback memory (step 3) is the more important write
		// for the fit loop. Log and continue — don't lose the feedback signal.
		log.Printf("[brain] feedback PATCH proposal %s failed (continuing): %v", prop.MemoryID, patchErr)
	}

	// 3. Write a category=feedback memory — this is the R4 signal. Shape per
	// BRAIN_DESIGN §9.3(c): verdict in BOTH tag (for accept-rate aggregation)
	// and metadata (carries proposal_id, reason).
	fbMem := Memory{
		Category: CategoryFeedback,
		Scope:    "global",
		Tags:     []string{"brain-feedback", string(verdict), prop.Domain},
		Source:   SourceHuman, // the user took this action; makro is the writer
		Metadata: map[string]any{
			"proposal_id":       prop.MemoryID,
			"verdict":           string(verdict),
			"reason":            reason,
			"proposal_title":    prop.Title,
			"acted_within_days": nil, // P3: completion-rate rescan backfills this
			"outcome":           nil, // P3: stalled|in-progress|done
		},
		Content: formatFeedbackContent(prop, verdict, reason),
	}
	if err := h.client.WriteFeedback(ctx, fbMem); err != nil {
		log.Printf("[brain] feedback write to memory failed (signal lost): %v", err)
	}

	// 4. Update the inbox cache so /inbox reflects it immediately.
	status := string(verdict) + "ed" // accepted / rejected
	if err := h.inbox.MarkStatus(ctx, localID, status); err != nil {
		log.Printf("[brain] feedback inbox mark failed: %v", err)
	}

	// Confirmation for the chat reply.
	emoji := "✅"
	if verdict == VerdictReject {
		emoji = "❌"
	}
	reply := fmt.Sprintf("%s proposal #%d 「%s」已 %s", emoji, localID, prop.Title, status)
	if reason != "" {
		reply += "（原因：" + reason + "）"
	}
	reply += "。反馈已写回 memory，下一轮 wake 会读到。"
	return reply, nil
}

// formatFeedbackContent builds the human-readable content of the feedback
// memory. This is what R4 search/recall surfaces, so keep it scannable.
func formatFeedbackContent(prop *InboxProposal, verdict Verdict, reason string) string {
	out := fmt.Sprintf("verdict: %s\nproposal: %s\n", verdict, prop.Title)
	if reason != "" {
		out += "reason: " + reason + "\n"
	}
	out += fmt.Sprintf("domain: %s\nconfidence_was: %.2f", prop.Domain, prop.Confidence)
	return out
}
