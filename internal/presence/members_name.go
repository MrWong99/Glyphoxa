package presence

import (
	"context"
	"errors"
	"fmt"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

// ErrNoGuild is returned by MemberDisplayName while the presence has no configured
// Guild (the wait-state) — there is no guild to fetch a member from, so the
// speaker resolver (#281) falls back to its generic label.
var ErrNoGuild = errors.New("presence: no configured guild for member lookup")

// MemberDisplayName resolves a Discord User snowflake to its display name in the
// configured Guild, using nick > global name > username precedence (disgo's
// Member.EffectiveName). It is the unmapped-speaker fallback the transcript
// resolver consults (#281, recorded decision 2): a speaker with no mapped
// Character renders as their guild display name. A wait-state (no Guild) or a REST
// failure returns an error, and the resolver negative-caches it as unresolved.
//
// This satisfies speaker.MemberNamer. The fetch is off-bus (the resolver calls it
// from a Warm goroutine, never a synchronous bus callback), so the REST round-trip
// here never stalls the voice pipeline.
func (p *Presence) MemberDisplayName(ctx context.Context, discordUserID string) (string, error) {
	gid := p.GuildID()
	if gid == "" {
		return "", ErrNoGuild
	}
	guildID, err := snowflake.Parse(gid)
	if err != nil {
		return "", fmt.Errorf("presence: parse guild id %q: %w", gid, err)
	}
	userID, err := snowflake.Parse(discordUserID)
	if err != nil {
		return "", fmt.Errorf("presence: parse user id %q: %w", discordUserID, err)
	}
	m, err := p.fetchMember(ctx, guildID, userID)
	if err != nil {
		return "", fmt.Errorf("presence: fetch guild member %s: %w", discordUserID, err)
	}
	if m == nil {
		return "", fmt.Errorf("presence: guild member %s: %w", discordUserID, ErrNoGuild)
	}
	return m.EffectiveName(), nil
}

// restGetMember is the production member fetch: it borrows the standing client and
// calls its REST GetMember. ErrNoClient in the wait-state propagates so the
// resolver negative-caches and retries.
func (p *Presence) restGetMember(ctx context.Context, guildID, userID snowflake.ID) (*discord.Member, error) {
	c, err := p.Client()
	if err != nil {
		return nil, err
	}
	return c.Rest.GetMember(guildID, userID, rest.WithCtx(ctx))
}
