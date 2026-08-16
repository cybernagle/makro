package llm

import (
	"regexp"
	"strings"
)

// thinkTagFilter strips inline <think>…</think> reasoning shells from model
// content streams. BigModel/DeepSeek-class models normally deliver reasoning
// via the structured reasoning_content delta (already surfaced as thinking
// events), but depending on model config the shell tags can leak into content
// — typically a stray trailing </think>, sometimes a full inline reasoning
// block. Tags leaking into content end up in chat bubbles AND get read aloud
// by TTS, so they are stripped here, at the provider boundary, for every
// consumer.
//
// The filter is a small state machine safe across chunk boundaries: a tag
// split mid-way ("<th" + "ink>") is buffered until it can be classified.
// Visible text is never held back longer than the longest tag minus one char.
type thinkTagFilter struct {
	insideThink bool   // between <think> and </think>: everything is reasoning
	pending     string // tail of the last push that may be a partial tag
}

const (
	thinkOpen  = "<think>"
	thinkClose = "</think>"
	// Any prefix of either tag could be completed by the next chunk.
	maxTagPartial = len(thinkClose) - 1
)

// Push consumes one content delta and returns the visible text it produced
// ("" if the delta was reasoning or an in-flight partial tag).
func (f *thinkTagFilter) Push(delta string) string {
	buf := f.pending + delta
	f.pending = ""
	var out strings.Builder
	for len(buf) > 0 {
		if f.insideThink {
			// Looking for the closer only.
			if i := strings.Index(buf, thinkClose); i >= 0 {
				buf = buf[i+len(thinkClose):]
				f.insideThink = false
				continue
			}
			// Keep a tail that might grow into "</think>".
			keep := holdPartialTag(buf, thinkClose)
			if keep > 0 {
				f.pending = buf[len(buf)-keep:]
			}
			return out.String() // everything else is reasoning: dropped
		}
		// Outside: emit text up to the next opener, dropping stray closers.
		nextOpen := strings.Index(buf, thinkOpen)
		nextClose := strings.Index(buf, thinkClose)
		switch {
		case nextOpen >= 0 && (nextClose < 0 || nextOpen < nextClose):
			out.WriteString(buf[:nextOpen])
			buf = buf[nextOpen+len(thinkOpen):]
			f.insideThink = true
		case nextClose >= 0:
			// Stray </think> with no open state: drop the tag, keep the text.
			out.WriteString(buf[:nextClose])
			buf = buf[nextClose+len(thinkClose):]
		default:
			// No complete tag. Hold back a tail that might be a partial one.
			keep := holdPartialTag(buf, thinkOpen)
			if keep > 0 {
				f.pending = buf[len(buf)-keep:]
				out.WriteString(buf[:len(buf)-keep])
			} else {
				out.WriteString(buf)
			}
			return out.String()
		}
	}
	return out.String()
}

// Flush returns any text still held as a possible partial tag once the stream
// ended — it can't be a tag after all, so it becomes visible.
func (f *thinkTagFilter) Flush() string {
	held := f.pending
	f.pending = ""
	return held
}

// holdPartialTag reports how many trailing bytes of buf could be the start of
// tag (i.e. a prefix of tag, shorter than the whole tag). 0 = no partial.
func holdPartialTag(buf, tag string) int {
	max := maxTagPartial
	if max >= len(buf) {
		max = len(buf) - 1
	}
	for n := max; n > 0; n-- {
		tail := buf[len(buf)-n:]
		if strings.HasPrefix(tag, tail) {
			return n
		}
	}
	return 0
}

// stripThinkTags is the non-streaming counterpart: remove inline reasoning
// blocks from a complete message. An unclosed <think> swallows the rest of the
// text (an unterminated reasoning shell has no visible content after it).
var thinkBlockRe = regexp.MustCompile(`(?s)<think>.*?</think>`)

func stripThinkTags(s string) string {
	s = thinkBlockRe.ReplaceAllString(s, "")
	if i := strings.Index(s, thinkOpen); i >= 0 {
		s = s[:i]
	}
	s = strings.ReplaceAll(s, thinkClose, "")
	return s
}
