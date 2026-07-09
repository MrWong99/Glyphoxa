package presence

import (
	"context"
	"log/slog"

	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
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

// VoiceChannelMembers lists the Discord Users currently in channelID, read from
// the standing gateway's voice-state cache — populated by the GuildVoiceStates
// intent the Bot already carries (see defaultClientBuilder), NOT the privileged
// GuildMembers intent. Each occupant's display name and avatar come from a REST
// GetMember, bounded by the channel's occupants; the Bot's own user is skipped.
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
func (p *Presence) VoiceChannelMembers(ctx context.Context, channelID snowflake.ID) ([]Member, error) {
	client, err := p.Client()
	if err != nil {
		return nil, err
	}

	// Filter the Bot itself out only when we actually know its id; if SelfUser is
	// not cached yet, an unknown-and-zero selfID must not masquerade as a real user.
	self, selfKnown := client.Caches.SelfUser()

	// Snapshot occupants under the cache lock, then release before any REST call.
	var occupants []voiceOccupant
	for guildID, vs := range client.Caches.VoiceStateCache().All() {
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
		m, err := client.Rest.GetMember(o.guildID, o.userID, rest.WithCtx(ctx))
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
