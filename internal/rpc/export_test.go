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
