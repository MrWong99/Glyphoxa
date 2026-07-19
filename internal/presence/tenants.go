package presence

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// tenantResolveTTL bounds how long a Guild→Tenant HIT is cached before a fresh
// interaction re-reads it. Short so a Configuration change (a Tenant saving its
// guild_id, which flips the newest-wins owner of a duplicated Guild) propagates
// within seconds, while keeping the interaction dispatch off a per-call DB read.
//
// Accepted tradeoff (#490 review): after a Guild is remapped to a new owner, this
// cached router and the UNCACHED member picker (presence.Clients.VoiceChannelMembers,
// which reads GetTenantIDByGuildID directly) can disagree for up to one TTL — a
// ≤30s window in which the router may still route to the previous owner while the
// picker already reflects the new one. Bounded and self-healing on the next refresh.
const tenantResolveTTL = 30 * time.Second

// tenantResolveNegTTL caches an UNKNOWN-Guild miss briefly so a burst of
// interactions from an unconfigured Guild (a DM-adjacent probe, a not-yet-saved
// Guild) does not hit the DB on every event. Shorter than the hit TTL so a Tenant
// that configures the Guild starts resolving within seconds.
const tenantResolveNegTTL = 5 * time.Second

// GuildTenantReader is the storage read the prod TenantResolver wraps —
// *storage.Store satisfies it via GetTenantIDByGuildID (newest-wins on a duplicated
// guild_id).
type GuildTenantReader interface {
	GetTenantIDByGuildID(ctx context.Context, guildID string) (uuid.UUID, error)
}

// storageTenantResolver is the production TenantResolver: a thin adapter over the
// storage GetTenantIDByGuildID read with a small TTL cache. The cache holds only
// successful hits (an unknown Guild is not cached, so a Tenant configuring that
// Guild resolves on its very next interaction). It shares GetTenantIDByGuildID's
// newest-wins determinism with the member-picker path, so a stale losing row can
// never route an interaction to the wrong Tenant.
type storageTenantResolver struct {
	reader GuildTenantReader
	ttl    time.Duration
	negTTL time.Duration
	now    func() time.Time

	mu    sync.Mutex
	cache map[string]tenantCacheEntry
}

type tenantCacheEntry struct {
	tenantID uuid.UUID
	notFound bool // a cached UNKNOWN-Guild miss (short-lived)
	expires  time.Time
}

// NewStorageTenantResolver builds the production Guild→Tenant resolver over the
// storage read, with the default hit + negative TTL caches.
func NewStorageTenantResolver(reader GuildTenantReader) TenantResolver {
	return &storageTenantResolver{
		reader: reader,
		ttl:    tenantResolveTTL,
		negTTL: tenantResolveNegTTL,
		now:    time.Now,
		cache:  map[string]tenantCacheEntry{},
	}
}

// TenantForGuild resolves guildID to its owning Tenant, serving a cached hit (TTL)
// or a cached UNKNOWN-Guild miss (shorter negTTL). Only storage.ErrNotFound is
// cached as a miss; a transient error is surfaced uncached so it retries next event.
func (r *storageTenantResolver) TenantForGuild(ctx context.Context, guildID string) (uuid.UUID, error) {
	now := r.now()
	r.mu.Lock()
	if e, ok := r.cache[guildID]; ok && now.Before(e.expires) {
		r.mu.Unlock()
		if e.notFound {
			return uuid.Nil, storage.ErrNotFound
		}
		return e.tenantID, nil
	}
	r.mu.Unlock()

	tenantID, err := r.reader.GetTenantIDByGuildID(ctx, guildID)
	if errors.Is(err, storage.ErrNotFound) {
		r.mu.Lock()
		r.cache[guildID] = tenantCacheEntry{notFound: true, expires: now.Add(r.negTTL)}
		r.mu.Unlock()
		return uuid.Nil, err
	}
	if err != nil {
		return uuid.Nil, err
	}
	r.mu.Lock()
	r.cache[guildID] = tenantCacheEntry{tenantID: tenantID, expires: now.Add(r.ttl)}
	r.mu.Unlock()
	return tenantID, nil
}
