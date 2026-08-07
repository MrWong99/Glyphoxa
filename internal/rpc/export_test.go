package rpc

import (
	"context"
	"log/slog"

	"github.com/MrWong99/Glyphoxa/internal/discordshare"
)

// SetShareSeamsForTest overrides the Discord REST seams so a test drives the
// DeploymentSharer against fakes instead of the live Discord API (#310).
func (d *DeploymentSharer) SetShareSeamsForTest(
	list func(ctx context.Context, token, guildID string, log *slog.Logger) ([]discordshare.Channel, error),
	post func(ctx context.Context, token, channelID, caption, filename, contentType string, data []byte, log *slog.Logger) error,
) {
	d.listFn = list
	d.postFn = post
}

// SetVoiceListSeamForTest overrides the voice-channel REST seam so a test
// drives ListVoiceChannels against a fake instead of the live Discord API.
func (d *DeploymentSharer) SetVoiceListSeamForTest(
	list func(ctx context.Context, token, guildID string, log *slog.Logger) ([]discordshare.Channel, error),
) {
	d.listVoiceFn = list
}

// SetGuildProofForTest overrides the #504 guild-admin proof seam so unit tests
// drive SaveDiscordSettings without dialing Discord, mirroring the resolveInvite
// seam pattern.
func (s *ProviderServer) SetGuildProofForTest(fn func(ctx context.Context, token, guildID, userID string) error) {
	s.checkGuildAdmin = fn
}
