package recall

import (
	"context"
	"strings"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// specCache holds the recaller's single speculated utterance (latest only): the
// normalized query text, the embedded vector, and the world-context chunks
// prefetched with that vector. A Recall whose normalized final matches norm reuses
// vec (skipping the inline embed) and, when worldOK, the prefetched world chunks —
// the "hit" path. worldOK is false when the vector embedded but the world prefetch
// failed, so a hit knows to fetch world inline rather than return empty. Guarded by
// cacheMu.
type specCache struct {
	norm    string
	vec     []float32
	world   []storage.ChunkMatch
	worldOK bool
	valid   bool
}

// onPartial is the bus callback for [voiceevent.STTPartial]. The bus delivers
// synchronously and callbacks MUST NOT block (relay/chunker precedent), so it only
// stows the latest text in a 1-slot latest-wins mailbox and pokes the speculator;
// all embedding/retrieval happens on the speculator goroutine. A stale-text
// partial (a previous utterance's interim arriving ~1 RTT after speech_start, per
// the #180 STTPartial doc) is harmless: it is just another candidate, and the
// final's normalized match self-heals a wrong speculation.
func (r *Recaller) onPartial(p voiceevent.STTPartial) {
	r.mailMu.Lock()
	r.pending = p.Text
	r.hasPending = true
	r.mailMu.Unlock()
	select {
	case r.signal <- struct{}{}:
	default: // a wake is already pending; the speculator will read the latest text
	}
}

// takePending drains the latest-wins mailbox.
func (r *Recaller) takePending() (string, bool) {
	r.mailMu.Lock()
	defer r.mailMu.Unlock()
	if !r.hasPending {
		return "", false
	}
	r.hasPending = false
	return r.pending, true
}

// speculateLoop is the single speculator goroutine: it wakes on the mailbox
// signal, speculates on the latest pending text, and exits when the recaller's
// context is cancelled ([Recaller.Close]). One goroutine gives single-flight for
// free — a slow embed simply defers the next candidate.
func (r *Recaller) speculateLoop() {
	defer close(r.specDone)
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-r.signal:
			r.drainAndSpeculate()
			// Notify a white-box test that one pass completed (non-blocking).
			select {
			case r.speculated <- struct{}{}:
			default:
			}
		}
	}
}

// drainAndSpeculate speculates on the latest pending text, honoring the embed rate
// limit WITHOUT dropping the candidate: when [maybeSpeculate] reports the interval
// has not elapsed, it waits out the remainder and retries on the latest text (a
// newer partial supersedes, else the same one). Without this the last pre-final
// partial inside the interval window would never be embedded — a systematic
// speculation miss on exactly the utterance the final matches.
func (r *Recaller) drainAndSpeculate() {
	text, ok := r.takePending()
	if !ok {
		return
	}
	for {
		wait := r.maybeSpeculate(text)
		if wait <= 0 {
			return
		}
		if err := r.sleep(r.ctx, wait); err != nil {
			return // Close cancelled the recaller
		}
		if newer, ok := r.takePending(); ok {
			text = newer
		}
	}
}

// maybeSpeculate embeds text and prefetches its world-context chunks when the text
// is worth it: at least [minSpeculateWords] words and changed since the last embed.
// It returns 0 when the pass is done (embedded, or permanently skipped as too-short
// / unchanged); it returns a positive duration when the embed rate limit
// ([minEmbedInterval]) has not yet elapsed — the remaining wait the caller sleeps
// before retrying, so the candidate is deferred, never dropped. World context is
// prefetched here (the vector is in hand); NPC-knowledge is deferred to Recall (the
// target agent is unknown during speech). On success it replaces the single-slot
// cache.
func (r *Recaller) maybeSpeculate(text string) time.Duration {
	norm := normalize(text)
	if wordCount(norm) < minSpeculateWords {
		return 0 // no retrieval signal in a one/two-word interim
	}
	if norm == r.lastEmbedNorm {
		return 0 // unchanged since the last embed
	}
	now := r.now()
	if !r.lastEmbedAt.IsZero() {
		if since := now.Sub(r.lastEmbedAt); since < minEmbedInterval {
			return minEmbedInterval - since // defer, do not drop (finding 2)
		}
	}
	campaignID, ok := r.campaign()
	if !ok {
		return 0 // no active session to scope the prefetch
	}
	// Commit to this attempt BEFORE the call so a failing/hung provider is not
	// hammered and the same text is not re-embedded on the next tick.
	r.lastEmbedNorm = norm
	r.lastEmbedAt = now

	ctx, cancel := context.WithTimeout(r.ctx, speculateTimeout)
	defer cancel()

	vecs, err := r.embedder.Embed(ctx, []string{text})
	if err != nil || len(vecs) != 1 {
		r.log.Warn("memory speculation: embed failed; will retry on the next partial", "err", err)
		return 0
	}
	// Prefetch world context with the vector. A failure still caches the vector
	// (worldOK false) so a later hit skips the (expensive) embed and fetches world
	// inline, rather than silently returning empty world.
	world, err := r.retriever.SearchChunksByCampaign(ctx, campaignID, vecs[0], r.k)
	worldOK := err == nil
	if err != nil {
		r.log.Warn("memory speculation: world prefetch failed; caching vector only", "err", err)
		world = nil
	}
	r.storeCache(norm, vecs[0], world, worldOK)
	return 0
}

// storeCache replaces the single speculated entry (latest utterance only).
func (r *Recaller) storeCache(norm string, vec []float32, world []storage.ChunkMatch, worldOK bool) {
	r.cacheMu.Lock()
	r.cache = specCache{norm: norm, vec: vec, world: world, worldOK: worldOK, valid: true}
	r.cacheMu.Unlock()
}

// cacheLookup returns the cached entry when norm matches the speculated query, and
// whether it was a hit.
func (r *Recaller) cacheLookup(norm string) (specCache, bool) {
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	if r.cache.valid && r.cache.norm == norm {
		return r.cache, true
	}
	return specCache{}, false
}

// wordCount counts whitespace-separated tokens in an already-normalized string.
func wordCount(s string) int { return len(strings.Fields(s)) }
