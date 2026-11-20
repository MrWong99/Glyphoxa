package recall

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// specCache holds one campaign's speculated utterance (latest only): the
// normalized query text, the embedded vector, and the world-context chunks
// prefetched with that vector. A Recall whose normalized final matches norm reuses
// vec (skipping the inline embed) and, when worldOK, the prefetched world chunks —
// the "hit" path. worldOK is false when the vector embedded but the world prefetch
// failed, so a hit knows to fetch world inline rather than return empty. Guarded by
// cacheMu.
type specCache struct {
	norm string
	// campaignID scopes the entry to the session the partial came from (#487): a
	// Recall in a DIFFERENT campaign that normalizes to the same text must miss, so
	// it never serves another concurrent session's prefetched world chunks.
	campaignID uuid.UUID
	vec        []float32
	world      []storage.ChunkMatch
	worldOK    bool
	valid      bool
	// at is when the entry was stored; an entry older than [cacheTTL] is not
	// served (the same words said again minutes later get a fresh retrieval).
	at time.Time
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
	r.pendingSID = p.SessionID // #487: scope the prefetch to the partial's session
	r.hasPending = true
	r.mailMu.Unlock()
	select {
	case r.signal <- struct{}{}:
	default: // a wake is already pending; the speculator will read the latest text
	}
}

// flight is one embed-and-prefetch of a FINAL utterance, started the moment the
// final is published. Its point is not a head start — Address Detection is a
// synchronous text match, so the turn's [Recaller.Recall] follows within
// microseconds — but that the embed runs under the recaller's own context: a
// turn that gives up at its budget no longer cancels the request, which for a
// cold local model aborted the load every time. Recall joins the flight rather
// than embedding the same text a second time behind it; done is closed when the
// result is in the cache, or will never be.
type flight struct {
	norm string
	done chan struct{}
	// result is the flight's own outcome, valid once done is closed (result.valid
	// false = the embed failed). Recall reads it from here, not from the cache: a
	// later final of the same campaign may have replaced the cache slot meanwhile.
	result specCache
}

// onFinal is the bus callback for [voiceevent.STTFinal]. Finals do not go through
// the serial speculator: each gets its own goroutine (bounded by finalSlots), so
// one table's chatter cannot queue ahead of another turn's recall. Like onPartial
// it must not block.
func (r *Recaller) onFinal(f voiceevent.STTFinal) {
	norm := normalize(f.Text)
	if norm == "" {
		return
	}
	sid, err := uuid.Parse(f.SessionID)
	if err != nil {
		return // nothing to scope a prefetch to; Recall embeds inline
	}
	vs, ok := r.sessions.Resolve(sid)
	if !ok {
		return
	}
	campaignID := vs.CampaignID
	if _, hit := r.cacheLookup(norm, campaignID); hit {
		return // the partials already speculated this exact text
	}
	select {
	case r.finalSlots <- struct{}{}:
	default:
		return // saturated: Recall embeds inline, as it did before finals speculated
	}
	fl := &flight{norm: norm, done: make(chan struct{})}
	r.cacheMu.Lock()
	r.flights[campaignID] = fl
	r.cacheMu.Unlock()

	r.finals.Add(1)
	go func() {
		defer r.finals.Done()
		defer func() { <-r.finalSlots }()
		defer close(fl.done)
		fl.result = r.embedAndCache(f.Text, norm, campaignID)
	}()
}

// flightFor returns the campaign's in-flight final speculation when it is for
// exactly this query.
func (r *Recaller) flightFor(norm string, campaignID uuid.UUID) *flight {
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	if fl := r.flights[campaignID]; fl != nil && fl.norm == norm {
		return fl
	}
	return nil
}

// takePending drains the latest-wins mailbox (text + originating session id).
func (r *Recaller) takePending() (text, sessionID string, ok bool) {
	r.mailMu.Lock()
	defer r.mailMu.Unlock()
	if !r.hasPending {
		return "", "", false
	}
	r.hasPending = false
	return r.pending, r.pendingSID, true
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
	text, sid, ok := r.takePending()
	if !ok {
		return
	}
	for {
		wait := r.maybeSpeculate(text, sid)
		if wait <= 0 {
			return
		}
		if err := r.sleep(r.ctx, wait); err != nil {
			return // Close cancelled the recaller
		}
		if newer, newSID, ok := r.takePending(); ok {
			text, sid = newer, newSID
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
func (r *Recaller) maybeSpeculate(text, sessionID string) time.Duration {
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
	// Resolve the Campaign from the partial's OWN session (#487): an empty or
	// unparsable SessionID (a pre-stamp / session-local straggler), or a session the
	// registry no longer resolves, has nothing to scope the prefetch to.
	sid, err := uuid.Parse(sessionID)
	if err != nil {
		return 0
	}
	vs, ok := r.sessions.Resolve(sid)
	if !ok {
		return 0
	}
	campaignID := vs.CampaignID
	// Commit to this attempt BEFORE the call so a failing/hung provider is not
	// hammered and the same text is not re-embedded on the next tick.
	r.lastEmbedNorm = norm
	r.lastEmbedAt = now

	r.embedAndCache(text, norm, campaignID)
	return 0
}

// embedAndCache embeds text and prefetches its world-context chunks off the turn
// path, replacing the campaign's cache slot on success. It returns the stored
// entry (valid false when the embed failed).
func (r *Recaller) embedAndCache(text, norm string, campaignID uuid.UUID) specCache {
	ctx, cancel := context.WithTimeout(r.ctx, speculateTimeout)
	defer cancel()

	vecs, err := r.embedder.Embed(ctx, []string{text})
	if err != nil || len(vecs) != 1 {
		r.log.Warn("memory speculation: embed failed; will retry on the next utterance", "err", err)
		return specCache{}
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
	return r.storeCache(norm, campaignID, vecs[0], world, worldOK)
}

// storeCache replaces the campaign's speculated entry (latest utterance only).
// One slot per campaign (#487): concurrent sessions neither serve nor evict each
// other's prefetch. The map is bounded by the campaigns live in this process;
// entries past their TTL are dropped here so an ended session's slot goes too.
func (r *Recaller) storeCache(norm string, campaignID uuid.UUID, vec []float32, world []storage.ChunkMatch, worldOK bool) specCache {
	now := time.Now()
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	for id, c := range r.cache {
		if now.Sub(c.at) > cacheTTL {
			delete(r.cache, id)
		}
	}
	c := specCache{norm: norm, campaignID: campaignID, vec: vec, world: world, worldOK: worldOK, valid: true, at: now}
	r.cache[campaignID] = c
	return c
}

// cacheLookup returns the campaign's cached entry when it is fresh and norm
// matches the speculated query, and whether it was a hit.
func (r *Recaller) cacheLookup(norm string, campaignID uuid.UUID) (specCache, bool) {
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	if c := r.cache[campaignID]; c.valid && c.norm == norm && time.Since(c.at) <= cacheTTL {
		return c, true
	}
	return specCache{}, false
}

// wordCount counts whitespace-separated tokens in an already-normalized string.
func wordCount(s string) int { return len(strings.Fields(s)) }
