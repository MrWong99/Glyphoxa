package rpc

import (
	"context"
	"errors"
	"slices"

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

// SetVoiceChannels wires the Session screen's voice-channel picker seam onto
// the SessionServer: the guild channel lister plus the deployment-config read
// that carries the linked guild + Default Voice Channel. Called once at boot;
// unwired (web-standalone tests), ListSessionVoiceChannels reports
// CodeUnimplemented rather than panic — the sharingEnabled posture.
func (s *SessionServer) SetVoiceChannels(lister VoiceChannelLister, deps deploymentReader) {
	s.voiceChannels = lister
	s.deployments = deps
}

// errVoiceChannelNotLinked is the GM-readable refusal for a voice channel id
// that is not one of the linked guild's voice channels (R9). Shared with
// ProviderServer's Default Voice Channel save so both surfaces say the same.
var errVoiceChannelNotLinked = errors.New("that voice channel is not in your linked Discord server — pick one from the list")

// checkVoiceChannelLinked verifies channelID is a voice channel of the
// request Tenant's linked guild (R9), via the same tenant-scoped lister the
// picker reads. A server without the lister wired skips the check (unit-test
// posture). Not linked is CodeInvalidArgument with a GM-readable message; a
// missing token / unlinked guild are the picker's own preconditions.
func (s *SessionServer) checkVoiceChannelLinked(ctx context.Context, channelID string) error {
	if s.voiceChannels == nil {
		return nil
	}
	channels, err := s.voiceChannels.ListVoiceChannels(ctx)
	switch {
	case errors.Is(err, ErrNoDiscordToken):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("save a Discord Bot token first"))
	case errors.Is(err, ErrNoGuildLinked):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("link a Discord server first"))
	case err != nil:
		return s.discordError("StartSession", err)
	}
	if !slices.ContainsFunc(channels, func(c discordshare.Channel) bool { return c.ID == channelID }) {
		return connect.NewError(connect.CodeInvalidArgument, errVoiceChannelNotLinked)
	}
	return nil
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
	if errors.Is(err, ErrNoGuildLinked) {
		// Belt and braces: the deployment read above already answered this state;
		// the lister's own guild fence covers a release racing between the two.
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("link a Discord server first"))
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
