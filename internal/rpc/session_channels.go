package rpc

import (
	"context"
	"errors"
	"regexp"

	"connectrpc.com/connect"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/discordshare"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// Session voice-channel selection: the Session screen picks which voice channel
// a start joins, so it needs the linked guild's voice channels plus the stored
// Default Voice Channel to pre-select. The lister rides the same
// DeploymentSharer that backs the Highlight share dialog's text-channel list —
// one tenant-scoped token + guild resolve, one Discord REST shape — but through
// its OWN seam, so a deployment without the Highlight sharing wired still lists
// channels.

// VoiceChannelLister lists the linked guild's voice channels the Session screen
// may join. *DeploymentSharer satisfies it; tests fake it so the handler never
// touches the network. A missing saved token is [ErrNoDiscordToken].
type VoiceChannelLister interface {
	ListVoiceChannels(ctx context.Context) ([]discordshare.Channel, error)
}

// snowflakePattern is the shape of a Discord snowflake id (an unsigned 64-bit
// decimal). StartSession validates its optional voice_channel_id against it so
// an accidental non-id string fails fast instead of riding into the voice loop.
var snowflakePattern = regexp.MustCompile(`^[0-9]{1,20}$`)

// SetVoiceChannels wires the Session screen's voice-channel picker seam onto
// the SessionServer: the guild channel lister plus the deployment-config read
// that carries the linked guild + Default Voice Channel. Called once at boot;
// unwired (web-standalone tests), ListSessionVoiceChannels reports
// CodeUnimplemented rather than panic — the sharingEnabled posture.
func (s *SessionServer) SetVoiceChannels(lister VoiceChannelLister, deps deploymentReader) {
	s.voiceChannels = lister
	s.deployments = deps
}

// ListSessionVoiceChannels lists the linked guild's voice channels for the
// Session screen's channel picker plus the stored Default Voice Channel id.
// A read. An unlinked guild and a missing saved Bot token are both
// CodeFailedPrecondition with an actionable message, mirroring
// ListShareChannels.
func (s *SessionServer) ListSessionVoiceChannels(
	ctx context.Context,
	_ *connect.Request[managementv1.ListSessionVoiceChannelsRequest],
) (*connect.Response[managementv1.ListSessionVoiceChannelsResponse], error) {
	if s.voiceChannels == nil || s.deployments == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("voice channel listing is not enabled on this server"))
	}
	tenantID, err := s.tenant(ctx)
	if err != nil {
		return nil, err
	}

	dep, err := s.deployments.GetDeploymentConfig(ctx, tenantID)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		s.log.Error("ListSessionVoiceChannels: load deployment config failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	if dep.GuildID == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("link a Discord server first"))
	}

	channels, err := s.voiceChannels.ListVoiceChannels(ctx)
	if errors.Is(err, ErrNoDiscordToken) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("save a Discord Bot token first"))
	}
	if err != nil {
		return nil, s.discordError("ListSessionVoiceChannels", err)
	}

	out := make([]*managementv1.VoiceChannel, 0, len(channels))
	for _, c := range channels {
		out = append(out, &managementv1.VoiceChannel{Id: c.ID, Name: c.Name})
	}
	return connect.NewResponse(&managementv1.ListSessionVoiceChannelsResponse{
		Channels:         out,
		DefaultChannelId: dep.VoiceChannelID,
	}), nil
}
