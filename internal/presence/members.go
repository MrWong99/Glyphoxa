package presence

import (
	"context"
	"fmt"

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

// VoiceChannelMembers lists the Discord Users currently in channelID, read from
// the standing gateway's voice-state cache — populated by the GuildVoiceStates
// intent the Bot already carries (see defaultClientBuilder), NOT the privileged
// GuildMembers intent. Each occupant's display name and avatar come from a REST
// GetMember, so the fan-out is bounded by the channel's occupants; the Bot's own
// user is skipped. A wait-state presence (no Bot token yet) returns ErrNoClient,
// which the RPC handler maps to an empty list so the picker falls back to
// free-text snowflake entry (ADR-0003: the Discord User ID is the identity).
func (p *Presence) VoiceChannelMembers(ctx context.Context, channelID snowflake.ID) ([]Member, error) {
	client, err := p.Client()
	if err != nil {
		return nil, err
	}

	var selfID snowflake.ID
	if self, ok := client.Caches.SelfUser(); ok {
		selfID = self.ID
	}

	var members []Member
	for guildID, vs := range client.Caches.VoiceStateCache().All() {
		if vs.ChannelID == nil || *vs.ChannelID != channelID || vs.UserID == selfID {
			continue
		}
		m, err := client.Rest.GetMember(guildID, vs.UserID, rest.WithCtx(ctx))
		if err != nil {
			return nil, fmt.Errorf("presence: get member %d: %w", vs.UserID, err)
		}
		members = append(members, Member{
			ID:          vs.UserID,
			DisplayName: m.EffectiveName(),
			AvatarURL:   m.EffectiveAvatarURL(),
		})
	}
	return members, nil
}
