package rpc

import (
	"context"
	"log/slog"

	"connectrpc.com/connect"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/presence"
)

// voiceMemberLister lists the Discord Users currently in the operator's voice
// channel — the Players panel picker source (#279). It is the seam main.go binds
// to the standing presence (channel resolved from the deployment config); a test
// injects a fake. Any failure (bot offline, unresolved channel, REST error) is a
// soft degrade, not an RPC error: the handler logs it and serves an empty list.
type voiceMemberLister func(ctx context.Context) ([]presence.Member, error)

// SetMemberLister wires the voice-channel member source the Players panel picker
// reads (#279). Called once at boot, before the server serves, so no lock is
// needed. Left nil on a keyless / bot-offline deployment, in which case
// ListDiscordVoiceMembers returns an empty list.
func (s *CampaignServer) SetMemberLister(l voiceMemberLister) {
	s.characterRoster.memberLister = l
}

// ListDiscordVoiceMembers returns the Discord Users currently in the operator's
// configured voice channel for the Players panel's member picker (#279). It is
// bot-offline-safe by design: a nil lister (no standing presence) or a lister
// failure (offline Bot, unresolved channel, REST error) returns an empty list
// and never an error, so the picker degrades to free-text snowflake entry
// (ADR-0003). No privileged intent is used — the members come from the
// voice-state cache the Bot already receives.
func (s *characterRoster) ListDiscordVoiceMembers(
	ctx context.Context,
	_ *connect.Request[managementv1.ListDiscordVoiceMembersRequest],
) (*connect.Response[managementv1.ListDiscordVoiceMembersResponse], error) {
	if s.memberLister == nil {
		return connect.NewResponse(&managementv1.ListDiscordVoiceMembersResponse{}), nil
	}

	members, err := s.memberLister(ctx)
	if err != nil {
		// Soft degrade: the picker is an optional convenience over free-text entry,
		// so a bot-offline / REST failure must not fail the call.
		slog.Default().Warn("ListDiscordVoiceMembers: lister failed; serving empty", "err", err)
		return connect.NewResponse(&managementv1.ListDiscordVoiceMembersResponse{}), nil
	}

	out := make([]*managementv1.DiscordVoiceMember, 0, len(members))
	for _, m := range members {
		out = append(out, &managementv1.DiscordVoiceMember{
			DiscordUserId: m.ID.String(),
			DisplayName:   m.DisplayName,
			AvatarUrl:     m.AvatarURL,
		})
	}
	return connect.NewResponse(&managementv1.ListDiscordVoiceMembersResponse{Members: out}), nil
}
