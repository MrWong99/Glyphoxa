package presence

import (
	"context"
	"log/slog"

	"github.com/disgoorg/snowflake/v2"
	"github.com/google/uuid"
)

// Member is one Discord User currently in a voice channel (#279): the snowflake
// id to bind a Character to, plus a display name and avatar URL for the Players
// panel's member picker row.
type Member struct {
	ID          snowflake.ID
	DisplayName string
	AvatarURL   string
}

// voiceOccupant is a (guild, user) pair snapshotted out of the voice-state cache
// before any REST call — see VoiceChannelMembers for why the snapshot matters.
type voiceOccupant struct {
	guildID snowflake.ID
	userID  snowflake.ID
}

// parseGuild parses a Guild snowflake string; ok is false for "" (wait-state) or
// an unparseable id, so the caller scopes the member read to nothing.
func parseGuild(guild string) (id snowflake.ID, ok bool) {
	if guild == "" {
		return 0, false
	}
	id, err := snowflake.Parse(guild)
	if err != nil {
		return 0, false
	}
	return id, true
}

// VoiceChannelMembers lists the Discord Users currently in channelID, read from
// the standing gateway's voice-state cache — populated by the GuildVoiceStates
// intent the Bot already carries (see defaultClientBuilder), NOT the privileged
// GuildMembers intent. Each occupant's display name and avatar come from
// p.fetchMember — the SAME injected REST-GetMember seam MemberDisplayName
// (members_name.go) uses, rather than a second one, so this is unit-tested
// (members_test.go, newMembersTestPresence) without a live gateway by injecting
// fetchMember and populating a real in-memory disgo cache directly. The Bot's own
// user is skipped.
//
// The standing REST client is borrowed ONCE up front (p.Client()) and that same
// rest.Rest is handed to every fetchMember call, so a mid-loop Close/token-rebuild
// can't flip later members into ErrNoClient and silently blank the picker — the
// fan-out runs against the snapshot the caller started with.
//
// The cache iteration (VoiceStateCache().All()) holds the grouped cache's RLock
// for the whole loop, so we must NOT do REST inside it: a slow/rate-limited
// GetMember would hold the read lock while the gateway's VoiceStateUpdate write
// parks behind it, stalling gateway event processing mid-session (the STT-wedge
// failure family). So snapshot the (guild, user) pairs UNDER the lock, exit the
// iteration, THEN fan out the REST calls. A single GetMember failure skips that
// one member (logged) rather than dropping the whole list. A wait-state presence
// (no Bot token yet) returns ErrNoClient, which the RPC handler maps to an empty
// list so the picker falls back to free-text entry (ADR-0003).
//
// The Tenant's standing client is resolved from the registry (#489), so a member
// picker read touches only that Tenant's client.
func (c *Clients) VoiceChannelMembers(ctx context.Context, tenantID uuid.UUID, channelID snowflake.ID) ([]Member, error) {
	client, err := c.ClientForTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	// A central-token client's voice-state cache holds occupants of EVERY Tenant's
	// Guild that shares the token. channelID alone does not scope to this Tenant, so
	// a Tenant configuring another Tenant's channel snowflake would otherwise list
	// that Guild's occupants (finding 2). Scope to this Tenant's own Guild; an
	// unconfigured/unparseable Guild (wait-state) scopes to nothing.
	wantGuild, guildKnown := parseGuild(c.GuildForTenant(tenantID))
	if !guildKnown {
		return []Member{}, nil
	}

	// Filter the Bot itself out only when we actually know its id; if SelfUser is
	// not cached yet, an unknown-and-zero selfID must not masquerade as a real user.
	self, selfKnown := client.Caches.SelfUser()

	// Snapshot occupants under the cache lock, then release before any REST call.
	var occupants []voiceOccupant
	for guildID, vs := range client.Caches.VoiceStateCache().All() {
		if guildID != wantGuild {
			continue
		}
		if vs.ChannelID == nil || *vs.ChannelID != channelID {
			continue
		}
		if selfKnown && vs.UserID == self.ID {
			continue
		}
		occupants = append(occupants, voiceOccupant{guildID: guildID, userID: vs.UserID})
	}

	members := make([]Member, 0, len(occupants))
	for _, o := range occupants {
		m, err := c.fetchMember(ctx, client.Rest, o.guildID, o.userID)
		if err != nil {
			// One member failing (rate limit, gone) must not blank the whole picker;
			// skip it and keep the rest.
			slog.Default().Warn("presence: get voice-channel member failed; skipping",
				"guild_id", o.guildID, "user_id", o.userID, "err", err)
			continue
		}
		members = append(members, Member{
			ID:          o.userID,
			DisplayName: m.EffectiveName(),
			AvatarURL:   m.EffectiveAvatarURL(),
		})
	}
	return members, nil
}
