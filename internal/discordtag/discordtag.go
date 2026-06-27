// Package discordtag resolves a Discord bot's live tag (e.g. "Glyphoxa#4823")
// by performing a SHORT-LIVED gateway login with a bot token and reading the
// bot user off the gateway Ready event (#70). It is the real signal behind the
// Configuration screen's Discord health badge: unlike a token-presence check, a
// successful gateway login proves the token can actually identify with Discord.
//
// This is a LIVE network call. The RPC layer hides it behind a seam so unit
// tests fake the resolver and never touch the network; [Resolve] itself is
// exercised only under an integration/live build or a real operator run. The
// only offline-safe path is the empty-token guard, which fails fast before any
// dial.
package discordtag

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
)

// Resolve opens a short-lived Discord gateway session with token, waits for the
// Ready event, and returns the bot user's tag ("Username#Discriminator", or the
// bare username under Discord's new handle system). The session is always closed
// before return.
//
// An empty token fails fast (no dial). A ctx deadline bounds the login: a hung
// or unreachable gateway returns ctx.Err() rather than blocking. log may be nil.
func Resolve(ctx context.Context, token string, log *slog.Logger) (string, error) {
	if token == "" {
		return "", errors.New("discordtag: empty bot token")
	}
	if log == nil {
		log = slog.Default()
	}

	// A buffered channel so the Ready listener never blocks even if Resolve has
	// already returned on a ctx deadline.
	tagCh := make(chan string, 1)

	client, err := disgo.New(token,
		bot.WithLogger(log),
		bot.WithDefaultGateway(),
		// Guilds is the minimal intent; Ready (and its bot user) is delivered on
		// identify regardless, so this keeps the login cheap.
		bot.WithGatewayConfigOpts(gateway.WithIntents(gateway.IntentGuilds)),
		bot.WithEventListenerFunc(func(e *events.Ready) {
			select {
			case tagCh <- e.User.Tag():
			default:
			}
		}),
	)
	if err != nil {
		return "", fmt.Errorf("discordtag: build client: %w", err)
	}
	// Close the gateway on every exit path; a fresh ctx so teardown still runs
	// when the caller's ctx has already expired.
	defer client.Close(context.Background())

	if err := client.OpenGateway(ctx); err != nil {
		return "", fmt.Errorf("discordtag: open gateway: %w", err)
	}

	select {
	case tag := <-tagCh:
		return tag, nil
	case <-ctx.Done():
		return "", fmt.Errorf("discordtag: timed out before Ready: %w", ctx.Err())
	}
}
