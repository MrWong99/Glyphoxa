package presence

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

// tenantResolveTTL bounds how long a Guild→Tenant mapping is cached before a fresh
// interaction re-reads it. Short so a Configuration change (a Tenant saving its
// guild_id, which flips the newest-wins owner of a duplicated Guild) propagates
// within seconds, while keeping the interaction dispatch off a per-call DB read.
const tenantResolveTTL = 30 * time.Second

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
	now    func() time.Time

	mu    sync.Mutex
	cache map[string]tenantCacheEntry
}

type tenantCacheEntry struct {
	tenantID uuid.UUID
	expires  time.Time
}

// NewStorageTenantResolver builds the production Guild→Tenant resolver over the
// storage read, with the default 30s TTL cache.
func NewStorageTenantResolver(reader GuildTenantReader) TenantResolver {
	return &storageTenantResolver{
		reader: reader,
		ttl:    tenantResolveTTL,
		now:    time.Now,
		cache:  map[string]tenantCacheEntry{},
	}
}

// TenantForGuild resolves guildID to its owning Tenant, serving a cached hit within
// the TTL. A miss (unknown Guild) surfaces the storage error and is not cached.
func (r *storageTenantResolver) TenantForGuild(ctx context.Context, guildID string) (uuid.UUID, error) {
	now := r.now()
	r.mu.Lock()
	if e, ok := r.cache[guildID]; ok && now.Before(e.expires) {
		r.mu.Unlock()
		return e.tenantID, nil
	}
	r.mu.Unlock()

	tenantID, err := r.reader.GetTenantIDByGuildID(ctx, guildID)
	if err != nil {
		return uuid.Nil, err
	}
	r.mu.Lock()
	r.cache[guildID] = tenantCacheEntry{tenantID: tenantID, expires: now.Add(r.ttl)}
	r.mu.Unlock()
	return tenantID, nil
}
