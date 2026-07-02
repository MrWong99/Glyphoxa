// Package discordtag resolves a Discord bot's live tag (e.g. "Glyphoxa#4823")
// by calling the REST `GET /users/@me` endpoint with the bot token (#70, #150).
// It is the real signal behind the Configuration screen's Discord health badge:
// unlike a token-presence check, a successful self-user read proves the token
// authenticates with Discord — WITHOUT a gateway IDENTIFY. A gateway login
// (the pre-#150 implementation) is serialized per token (~1 per 5s) and counts
// toward Discord's daily session-start cap, so a health probe sharing the live
// session's token could delay its reconnect and burn its budget; the REST read
// costs neither.
//
// This is a LIVE network call. The RPC layer hides it behind a seam so unit
// tests fake the resolver and never touch the network; [Resolve] against the
// real API is exercised only by an operator run. Offline tests drive the
// package-private base-URL seam at a fake HTTP server.
package discordtag

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
)

// Resolve reads the bot user via REST `GET /users/@me` with token and returns
// its tag ("Username#Discriminator", or the bare username under Discord's new
// handle system). No gateway connection is opened and no IDENTIFY happens.
//
// An empty token fails fast (no dial). A ctx deadline bounds the call: a hung
// or unreachable endpoint returns ctx.Err() rather than blocking. log may be
// nil.
func Resolve(ctx context.Context, token string, log *slog.Logger) (string, error) {
	return resolve(ctx, token, "", log)
}

// resolve is Resolve with a base-URL seam: "" means the live Discord API,
// tests point it at a fake HTTP server.
func resolve(ctx context.Context, token, baseURL string, log *slog.Logger) (string, error) {
	if token == "" {
		return "", errors.New("discordtag: empty bot token")
	}
	if log == nil {
		log = slog.Default()
	}

	opts := []rest.ClientConfigOpt{rest.WithLogger(log)}
	if baseURL != "" {
		opts = append(opts, rest.WithURL(baseURL))
	}
	client := rest.NewClient(token, opts...)
	// Close on a fresh ctx so teardown still runs when the caller's ctx has
	// already expired.
	defer client.Close(context.WithoutCancel(ctx))

	var user discord.OAuth2User
	if err := client.Do(rest.GetCurrentUser.Compile(nil), nil, &user, rest.WithCtx(ctx)); err != nil {
		return "", fmt.Errorf("discordtag: GET /users/@me: %w", err)
	}
	return user.Tag(), nil
}
