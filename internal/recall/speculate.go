package recall

import (
	"context"
	"strings"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// specCache holds the recaller's single speculated utterance (latest only): the
// normalized query text, the embedded vector, and the world-context chunks
// prefetched with that vector. A Recall whose normalized final matches norm reuses
// vec + world and skips the inline embed (the "hit" path). Guarded by cacheMu.
type specCache struct {
	norm  string
	vec   []float32
	world []storage.ChunkMatch
	valid bool
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
			if text, ok := r.takePending(); ok {
				r.maybeSpeculate(text)
			}
			// Notify a white-box test that one pass completed (non-blocking).
			select {
			case r.speculated <- struct{}{}:
			default:
			}
		}
	}
}

// maybeSpeculate embeds text and prefetches its world-context chunks when the text
// is worth it: at least [minSpeculateWords] words, changed since the last embed,
// and no more than one embed per [minEmbedInterval]. World context is prefetched
// here (the vector is in hand); NPC-knowledge is deferred to Recall (the target
// agent is unknown during speech). On success it replaces the single-slot cache.
func (r *Recaller) maybeSpeculate(text string) {
	norm := normalize(text)
	if wordCount(norm) < minSpeculateWords {
		return
	}
	if norm == r.lastEmbedNorm {
		return // unchanged since the last embed
	}
	now := r.now()
	if !r.lastEmbedAt.IsZero() && now.Sub(r.lastEmbedAt) < minEmbedInterval {
		return // rate-limited; the next partial after the interval is embedded
	}
	campaignID, ok := r.campaign()
	if !ok {
		return // no active session to scope the prefetch
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
		return
	}
	// Prefetch world context with the vector. A failure still caches the vector so a
	// later hit skips the (expensive) embed and only owes the NPC-knowledge query.
	world, err := r.retriever.SearchChunksByCampaign(ctx, campaignID, vecs[0], r.k)
	if err != nil {
		r.log.Warn("memory speculation: world prefetch failed; caching vector only", "err", err)
		world = nil
	}
	r.storeCache(norm, vecs[0], world)
}

// storeCache replaces the single speculated entry (latest utterance only).
func (r *Recaller) storeCache(norm string, vec []float32, world []storage.ChunkMatch) {
	r.cacheMu.Lock()
	r.cache = specCache{norm: norm, vec: vec, world: world, valid: true}
	r.cacheMu.Unlock()
}

// cacheLookup returns the cached vector and world chunks when norm matches the
// speculated query, and whether it was a hit.
func (r *Recaller) cacheLookup(norm string) (vec []float32, world []storage.ChunkMatch, hit bool) {
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	if r.cache.valid && r.cache.norm == norm {
		return r.cache.vec, r.cache.world, true
	}
	return nil, nil, false
}

// wordCount counts whitespace-separated tokens in an already-normalized string.
func wordCount(s string) int { return len(strings.Fields(s)) }
