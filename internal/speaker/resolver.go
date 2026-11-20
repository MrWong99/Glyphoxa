// Package speaker resolves a Speaker Lane's Discord snowflake to a display name
// and GM flag for the transcript relay + chunk writer (#281, E4). The two
// consumers are synchronous bus callbacks that must never block (ADR-0040), so
// resolution is split: Warm does the DB / Discord I/O asynchronously off the bus
// and fills a short-TTL cache; Lookup is cache-only and never blocks.
//
// The name is a persist-time snapshot (the recorded decision): who is written
// once at persist time and a later rebind only affects FUTURE lines — replay and
// search read the persisted who unchanged. Invalidation is an in-proc direct
// method (ADR-0039), called by the Character CRUD handlers, so a mid-campaign
// rebind re-resolves the next line without a bus event or polling.
package speaker

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

const (
	// ttlPositive caches a resolved name (Character or guild display) for 5 minutes:
	// long enough to cover a session's cadence, short enough that a rebind without
	// an explicit invalidation still self-heals.
	ttlPositive = 5 * time.Minute
	// ttlNegative caches an unresolved speaker (fetch error, nil namer, unknown
	// lookup failure) for 30s only, so a transient Discord blip retries soon.
	ttlNegative = 30 * time.Second
	// fillTimeout bounds one asynchronous fill's I/O so a stalled DB or Discord REST
	// call cannot leak goroutines.
	fillTimeout = 5 * time.Second
)

// CharacterLookup is the storage read surface the resolver needs: the speaker →
// Character resolution (#276) and the Campaign's owning Tenant, which scopes the
// GM verdict. *storage.Store satisfies it.
type CharacterLookup interface {
	GetCharacterByDiscordUser(ctx context.Context, campaignID uuid.UUID, discordUserID string) (storage.Character, error)
	GetCampaign(ctx context.Context, id uuid.UUID) (storage.Campaign, error)
}

// MemberNamer resolves a Discord snowflake to its guild display name — the
// unmapped-speaker fallback (recorded decision 2). The campaignID scopes the
// lookup to the Campaign's owning Tenant's Guild (#483), so a shared central
// token never bleeds another Tenant's nickname into this Campaign's labels.
// *presence.Clients satisfies it; a nil namer means web-only mode with no
// guild fallback.
type MemberNamer interface {
	MemberDisplayName(ctx context.Context, campaignID uuid.UUID, discordUserID string) (string, error)
}

// GMChecker reports whether a Discord snowflake is a GM OF ONE TENANT. It must
// never block — Lookup calls it inside the relay/chunker's synchronous bus
// callbacks (ADR-0040). *auth.GMIdentity satisfies it: THIS Tenant's operator
// binding union the env allowlist (ADR-0055, amending ADR-0050's
// allowlist-membership clause), served from a cached snapshot.
//
// The verdict is per Tenant on purpose. The deployment-wide "operator of SOME
// tenant" check labelled any Tenant's operator as GM in every other Tenant's
// transcript they merely joined as a player.
type GMChecker interface {
	IsGMInTenant(tenantID uuid.UUID, discordUserID string) bool
}

// Resolution is the resolved identity for a Speaker Lane snowflake. Name is the
// Character name when mapped, else the guild display name, else "" (unresolved or
// a failed fetch — the caller keeps its generic label). GM is the GMChecker's
// verdict for the Campaign's owning Tenant, computed on every Lookup against its
// own snapshot (never cached here), so a binding change takes effect on the
// checker's freshness terms without a name-cache flush. Until a Warm has learned
// the Campaign's Tenant, GM is false (the same cold-cache posture as Name).
type Resolution struct {
	Name string
	GM   bool
}

// key identifies a cached name by campaign + speaker snowflake. GM is not part of
// the key: it is never cached.
type key struct {
	campaign uuid.UUID
	speaker  string
}

// entry is a cached resolution: name ("" = negative) and its expiry.
type entry struct {
	name    string
	expires time.Time
}

// Resolver caches speaker → name resolutions. Warm fills asynchronously off the
// bus; Lookup reads the cache only. Its own mutex is never held across a callback,
// so a caller may Warm/Lookup from under its own lock without a deadlock.
type Resolver struct {
	chars   CharacterLookup
	members MemberNamer
	gm      GMChecker
	log     *slog.Logger
	now     func() time.Time

	mu       sync.Mutex
	cache    map[key]entry
	inflight map[key]struct{}        // keys with a fill in progress (dedup)
	gen      map[uuid.UUID]uint64    // per-campaign generation; bumped on InvalidateCampaign
	tenant   map[uuid.UUID]uuid.UUID // campaign → owning Tenant; filled by Warm, immutable once known
	wg       sync.WaitGroup          // tracks in-flight fills (test synchronization)
}

// NewResolver builds a Resolver. members may be nil (web-only mode: no guild
// fallback). gm must be non-blocking (see [GMChecker]); nil labels nobody GM.
// log nil defaults to a discarding logger.
func NewResolver(chars CharacterLookup, members MemberNamer, gm GMChecker, log *slog.Logger) *Resolver {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Resolver{
		chars:    chars,
		members:  members,
		gm:       gm,
		log:      log,
		now:      time.Now,
		cache:    map[key]entry{},
		inflight: map[key]struct{}{},
		gen:      map[uuid.UUID]uint64{},
		tenant:   map[uuid.UUID]uuid.UUID{},
	}
}

// Warm asynchronously fills the cache for a speaker so a later Lookup hits. It
// never blocks: a fresh cache entry or an in-flight fill for the same key is a
// no-op (dedup), otherwise it dispatches one background fill. An empty speakerID
// (unattributed lane) is ignored.
func (r *Resolver) Warm(campaignID uuid.UUID, speakerID string) {
	if speakerID == "" {
		return
	}
	k := key{campaign: campaignID, speaker: speakerID}

	r.mu.Lock()
	_, tenantKnown := r.tenant[campaignID]
	if e, ok := r.cache[k]; ok && r.now().Before(e.expires) && tenantKnown {
		r.mu.Unlock()
		return // still fresh
	}
	if _, busy := r.inflight[k]; busy {
		r.mu.Unlock()
		return // a fill is already running for this key
	}
	r.inflight[k] = struct{}{}
	gen := r.gen[campaignID]
	r.wg.Add(1)
	r.mu.Unlock()

	go r.fill(k, gen)
}

// fill runs one resolution's I/O off the bus and stores the result under the
// mutex. A result whose campaign generation changed while the fill ran (an
// InvalidateCampaign landed mid-flight) is dropped so a stale name is never cached.
func (r *Resolver) fill(k key, gen uint64) {
	defer r.wg.Done()
	tenantID, tenantOK := r.resolveTenant(k.campaign)
	name, ttl := r.resolve(k)
	r.mu.Lock()
	defer r.mu.Unlock()
	if tenantOK {
		// Recorded regardless of the generation check below: a Campaign never
		// changes Tenant, so this is never stale.
		r.tenant[k.campaign] = tenantID
	}
	if r.gen[k.campaign] != gen {
		// Invalidated mid-fill: the generation was bumped (and this key's marker
		// cleared) after this fill started. A later Warm may have installed a fresh
		// marker for the new generation — do NOT delete it, or dedup breaks and a
		// duplicate fill spawns. Discard this now-stale result and leave the map.
		return
	}
	delete(r.inflight, k)
	r.cache[k] = entry{name: name, expires: r.now().Add(ttl)}
}

// resolveTenant returns the Campaign's owning Tenant, from the cache or (once per
// Campaign) the store, with no lock held across the read. ok=false leaves the
// Tenant unknown, so Lookup reports GM=false until a later Warm succeeds.
func (r *Resolver) resolveTenant(campaignID uuid.UUID) (uuid.UUID, bool) {
	r.mu.Lock()
	id, known := r.tenant[campaignID]
	r.mu.Unlock()
	if known {
		return id, true
	}
	ctx, cancel := context.WithTimeout(context.Background(), fillTimeout)
	defer cancel()
	c, err := r.chars.GetCampaign(ctx, campaignID)
	if err != nil {
		r.log.Warn("speaker: campaign lookup failed; GM label off until it resolves", "err", err, "campaign", campaignID)
		return uuid.Nil, false
	}
	return c.TenantID, true
}

// resolve performs the actual lookup with no lock held: Character name (positive)
// → guild display name (positive) → "" (negative). An unknown Character-lookup
// error (not a genuine miss) short-caches negative and does NOT fall through to
// the guild name — "unknown" is not "unmapped".
func (r *Resolver) resolve(k key) (string, time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), fillTimeout)
	defer cancel()

	ch, err := r.chars.GetCharacterByDiscordUser(ctx, k.campaign, k.speaker)
	switch {
	case err == nil:
		return ch.Name, ttlPositive
	case !errors.Is(err, storage.ErrNotFound):
		r.log.Warn("speaker: character lookup failed", "err", err, "speaker", k.speaker)
		return "", ttlNegative
	}

	// Genuine miss (unmapped speaker): fall back to the Discord guild display name.
	if r.members == nil {
		return "", ttlNegative
	}
	name, err := r.members.MemberDisplayName(ctx, k.campaign, k.speaker)
	if err != nil {
		r.log.Warn("speaker: guild member fetch failed", "err", err, "speaker", k.speaker)
		return "", ttlNegative
	}
	if name == "" {
		return "", ttlNegative
	}
	return name, ttlPositive
}

// Lookup returns the cached Resolution for a speaker. It NEVER blocks or does I/O:
// GM is the non-blocking GMChecker's verdict for the Campaign's owning Tenant
// (false until a Warm has learned which Tenant that is), and Name is served only
// from a fresh cache entry (a cold or expired entry yields Name ""). The caller
// keeps its generic label when Name is "".
func (r *Resolver) Lookup(campaignID uuid.UUID, speakerID string) Resolution {
	if speakerID == "" {
		return Resolution{}
	}
	var res Resolution
	r.mu.Lock()
	tenantID, tenantKnown := r.tenant[campaignID]
	if e, ok := r.cache[key{campaign: campaignID, speaker: speakerID}]; ok && r.now().Before(e.expires) {
		res.Name = e.name
	}
	r.mu.Unlock()
	// The checker is called outside r.mu (it takes its own lock).
	res.GM = tenantKnown && r.gm != nil && r.gm.IsGMInTenant(tenantID, speakerID)
	return res
}

// InvalidateCampaign drops every cached name for a campaign and bumps its
// generation, so any fill still in flight discards its result and the next Warm
// re-queries with the new mapping (ADR-0039 in-proc direct-method invalidation).
// Called by the Character CRUD handlers after a create/update/delete.
func (r *Resolver) InvalidateCampaign(campaignID uuid.UUID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gen[campaignID]++
	for k := range r.cache {
		if k.campaign == campaignID {
			delete(r.cache, k)
		}
	}
	// Clear in-flight markers too so a fresh Warm re-queries immediately rather than
	// waiting on the now-stale fill (whose result the generation check discards).
	for k := range r.inflight {
		if k.campaign == campaignID {
			delete(r.inflight, k)
		}
	}
}
