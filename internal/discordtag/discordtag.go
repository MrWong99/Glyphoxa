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
// The call is a plain net/http GET, deliberately NOT disgo's rest.NewClient:
// that client's rate limiter starts a cleanup goroutine (`for range ticker.C`)
// its Close never stops, leaking one goroutine + ticker per probe.
//
// This is a LIVE network call. The RPC layer hides it behind a seam so unit
// tests fake the resolver and never touch the network; [Resolve] against the
// real API is exercised only by an operator run. Offline tests drive the
// package-private base-URL seam at a fake HTTP server.
package discordtag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/disgoorg/disgo/discord"
)

// liveAPIBaseURL is Discord's REST API root, matching the version disgo pins.
const liveAPIBaseURL = "https://discord.com/api/v10"

// selfUserClient is shared across probes (http.Client is safe for concurrent
// use); its Timeout is a belt on top of the caller's ctx deadline so a resolver
// invoked without one still cannot hang.
var selfUserClient = &http.Client{Timeout: 15 * time.Second}

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
	if baseURL == "" {
		baseURL = liveAPIBaseURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/users/@me", nil)
	if err != nil {
		return "", fmt.Errorf("discordtag: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bot "+token)
	// Discord requires bots to identify with the DiscordBot User-Agent form.
	req.Header.Set("User-Agent", "DiscordBot (https://github.com/MrWong99/Glyphoxa, v2)")

	resp, err := selfUserClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("discordtag: GET /users/@me: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("discordtag: GET /users/@me: HTTP %d", resp.StatusCode)
	}

	var user discord.OAuth2User
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return "", fmt.Errorf("discordtag: decode self user: %w", err)
	}
	tag := user.Tag()
	log.Debug("resolved Discord bot tag via REST", "tag", tag)
	return tag, nil
}
