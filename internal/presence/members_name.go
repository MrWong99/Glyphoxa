package presence

import (
	"context"
	"errors"
	"fmt"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"github.com/google/uuid"
)

// ErrNoGuild is returned by MemberDisplayName when no standing client has a known
// Guild to fetch a member from (the wait-state) — so the speaker resolver (#281)
// falls back to its generic label.
var ErrNoGuild = errors.New("presence: no configured guild for member lookup")

// MemberDisplayNameInTenant resolves a Discord User snowflake to its display name
// scoped to ONE Tenant's own Guild (#490): the tenant-narrowed form of
// MemberDisplayName for label paths that DO carry a Tenant (a session/campaign whose
// Tenant is known). It resolves ONLY that Tenant's configured Guild on that Tenant's
// standing client, so a member who also sits in another Tenant's Guild can never be
// labelled from the foreign Guild's nickname. A wait-state (the Tenant has no Guild
// or no live client) or a REST failure returns an error; the caller negative-caches
// it. Callers WITHOUT a Tenant keep the interim all-entries scan (MemberDisplayName).
func (c *Clients) MemberDisplayNameInTenant(ctx context.Context, tenantID uuid.UUID, discordUserID string) (string, error) {
	userID, err := snowflake.Parse(discordUserID)
	if err != nil {
		return "", fmt.Errorf("presence: parse user id %q: %w", discordUserID, err)
	}
	guildStr := c.GuildForTenant(tenantID)
	guild, err := snowflake.Parse(guildStr)
	if err != nil {
		return "", ErrNoGuild // wait-state: no configured Guild for this Tenant
	}
	client := c.clientFor(tenantID)
	if client == nil {
		return "", ErrNoGuild
	}
	m, err := c.fetchMember(ctx, client.Rest, guild, userID)
	if err != nil {
		return "", fmt.Errorf("presence: fetch guild member %s in tenant %s: %w", discordUserID, tenantID, err)
	}
	if m == nil {
		return "", fmt.Errorf("presence: guild member %s: %w", discordUserID, ErrNoGuild)
	}
	return m.EffectiveName(), nil
}

// MemberDisplayName resolves a Discord User snowflake to its display name, using
// nick > global name > username precedence (disgo's Member.EffectiveName). It is
// the unmapped-speaker fallback the transcript resolver consults (#281, recorded
// decision 2): a speaker with no mapped Character renders as their guild display
// name. A wait-state (no known Guild) or a REST failure returns an error, and the
// resolver negative-caches it as unresolved.
//
// Interim, label-only (#489/#490): the transcript resolver (speaker.Resolver) is
// campaign-keyed and carries no Tenant, so this stays the all-entries scan across
// the standing clients and their registered Guilds, returning the first that
// resolves the member. A label path that DOES carry a Tenant should use
// MemberDisplayNameInTenant instead (narrowed to that Tenant's Guild). This
// satisfies speaker.MemberNamer; the fetch is off-bus (a Warm goroutine, never a
// synchronous bus callback), so the REST round-trip never stalls the voice pipeline.
func (c *Clients) MemberDisplayName(ctx context.Context, discordUserID string) (string, error) {
	userID, err := snowflake.Parse(discordUserID)
	if err != nil {
		return "", fmt.Errorf("presence: parse user id %q: %w", discordUserID, err)
	}

	type attempt struct {
		client *bot.Client
		guild  snowflake.ID
	}
	var attempts []attempt
	c.mu.Lock()
	for _, e := range c.entries {
		client := e.client.Load()
		if client == nil {
			continue
		}
		for g := range e.registeredGuilds {
			gid, perr := snowflake.Parse(g)
			if perr != nil {
				continue
			}
			attempts = append(attempts, attempt{client: client, guild: gid})
		}
	}
	c.mu.Unlock()
	if len(attempts) == 0 {
		return "", ErrNoGuild
	}

	var lastErr error
	for _, a := range attempts {
		m, err := c.fetchMember(ctx, a.client.Rest, a.guild, userID)
		if err != nil {
			lastErr = err
			continue
		}
		if m != nil {
			return m.EffectiveName(), nil
		}
	}
	if lastErr != nil {
		return "", fmt.Errorf("presence: fetch guild member %s: %w", discordUserID, lastErr)
	}
	return "", fmt.Errorf("presence: guild member %s: %w", discordUserID, ErrNoGuild)
}

// restGetMember is the production member fetch: it calls GetMember on a REST
// client the caller borrowed from the registry.
func (c *Clients) restGetMember(ctx context.Context, r rest.Rest, guildID, userID snowflake.ID) (*discord.Member, error) {
	return r.GetMember(guildID, userID, rest.WithCtx(ctx))
}
