package presence

import (
	"context"
	"errors"
	"fmt"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"github.com/google/uuid"
)

// ErrNoGuild is returned by MemberDisplayName when the Campaign's owning Tenant
// has no known Guild or standing client to fetch a member from (the wait-state) —
// so the speaker resolver (#281) falls back to its generic label.
var ErrNoGuild = errors.New("presence: no configured guild for member lookup")

// MemberDisplayName resolves a Discord User snowflake to its display name, using
// nick > global name > username precedence (disgo's Member.EffectiveName). It is
// the unmapped-speaker fallback the transcript resolver consults (#281, recorded
// decision 2): a speaker with no mapped Character renders as their guild display
// name. The lookup is narrowed to the Campaign's OWNING Tenant's configured
// Guild and standing client (#483 — GuildForTenant + clientFor): on a shared
// central token the same Discord user may carry different nicknames in other
// Tenants' Guilds, and those must never bleed into this Campaign's labels. A
// wait-state (no Guild / no client yet), an unresolvable Campaign, or a REST
// failure returns an error, and the resolver negative-caches it as unresolved.
// This satisfies speaker.MemberNamer; the fetch is off-bus (a Warm goroutine,
// never a synchronous bus callback), so the REST round-trip never stalls the
// voice pipeline.
func (c *Clients) MemberDisplayName(ctx context.Context, campaignID uuid.UUID, discordUserID string) (string, error) {
	userID, err := snowflake.Parse(discordUserID)
	if err != nil {
		return "", fmt.Errorf("presence: parse user id %q: %w", discordUserID, err)
	}

	camp, err := c.store.GetCampaign(ctx, campaignID)
	if err != nil {
		return "", fmt.Errorf("presence: resolve campaign %s owning tenant: %w", campaignID, err)
	}

	guild := c.GuildForTenant(camp.TenantID)
	if guild == "" {
		return "", ErrNoGuild
	}
	gid, err := snowflake.Parse(guild)
	if err != nil {
		return "", fmt.Errorf("presence: parse guild id %q: %w", guild, err)
	}
	client := c.clientFor(camp.TenantID)
	if client == nil {
		// The Tenant's standing client is down/rebuilding: fail soft, the resolver
		// negative-caches for 30s and retries.
		return "", fmt.Errorf("presence: member lookup for tenant %s: %w", camp.TenantID, ErrNoClient)
	}

	m, err := c.fetchMember(ctx, client.Rest, gid, userID)
	if err != nil {
		return "", fmt.Errorf("presence: fetch guild member %s: %w", discordUserID, err)
	}
	if m == nil {
		return "", fmt.Errorf("presence: guild member %s: %w", discordUserID, ErrNoGuild)
	}
	return m.EffectiveName(), nil
}

// restGetMember is the production member fetch: it calls GetMember on a REST
// client the caller borrowed from the registry.
func (c *Clients) restGetMember(ctx context.Context, r rest.Rest, guildID, userID snowflake.ID) (*discord.Member, error) {
	return r.GetMember(guildID, userID, rest.WithCtx(ctx))
}
