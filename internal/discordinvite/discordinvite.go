// Package discordinvite resolves a pasted Discord invite code to its Guild and
// that Guild's voice channels (#105, ADR-0047), backing the Configuration
// screen's invite picker. It makes two REST calls with the deployment Bot token:
// `GET /invites/{code}` for the guild, then `GET /guilds/{id}/channels` filtered
// to type-2 GUILD_VOICE.
//
// Like internal/discordtag, the calls are plain net/http GETs, deliberately NOT
// disgo's rest.NewClient: that client's rate limiter starts a cleanup goroutine
// (`for range ticker.C`) its Close never stops, leaking one goroutine + ticker
// per call.
//
// These are LIVE network calls. The RPC layer hides Resolve behind a seam so
// unit tests fake the resolver and never touch the network; offline tests drive
// the package-private base-URL seam ([ResolveAt]) at a fake HTTP server.
package discordinvite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"time"
)

// liveAPIBaseURL is Discord's REST API root, matching the version disgo pins.
const liveAPIBaseURL = "https://discord.com/api/v10"

// userAgent is the DiscordBot form Discord requires of bot REST callers, matching
// internal/discordtag.
const userAgent = "DiscordBot (https://github.com/MrWong99/Glyphoxa, v2)"

// guildVoiceType is Discord's channel type for a standard voice channel
// (GUILD_VOICE). Text (0), category (4), and stage (13) are excluded (ADR-0047).
const guildVoiceType = 2

// inviteClient is shared across calls (http.Client is safe for concurrent use);
// its Timeout is a belt on top of the caller's ctx deadline so a resolver invoked
// without one still cannot hang.
var inviteClient = &http.Client{Timeout: 15 * time.Second}

// ErrNotFound means the invite code is invalid/expired, or resolves to something
// without a guild (a group-DM invite) — either way there is no server to pick a
// voice channel from.
var ErrNotFound = errors.New("discordinvite: invite not found or expired")

// ErrNoAccess means the Bot is not a member of the resolved guild: the channels
// read came back 403 or 404 (Discord is inconsistent about which for a non-member).
var ErrNoAccess = errors.New("discordinvite: the Bot is not a member of that guild")

// Guild is the resolved server's identity.
type Guild struct {
	ID   string
	Name string
}

// VoiceChannel is one type-2 GUILD_VOICE channel: its snowflake and name.
type VoiceChannel struct {
	ID   string
	Name string
}

// Resolved is the invite resolution: the guild plus its voice channels, sorted
// by position then name.
type Resolved struct {
	Guild         Guild
	VoiceChannels []VoiceChannel
}

// Resolve resolves invite code to its guild and voice channels using token
// (a Discord Bot token). An empty token fails fast (no dial). A ctx deadline
// bounds each call. log may be nil.
func Resolve(ctx context.Context, token, code string, log *slog.Logger) (Resolved, error) {
	return resolve(ctx, token, code, "", log)
}

// resolve is Resolve with a base-URL seam: "" means the live Discord API, tests
// point it at a fake HTTP server.
func resolve(ctx context.Context, token, code, baseURL string, log *slog.Logger) (Resolved, error) {
	if token == "" {
		return Resolved{}, errors.New("discordinvite: empty bot token")
	}
	if log == nil {
		log = slog.Default()
	}
	if baseURL == "" {
		baseURL = liveAPIBaseURL
	}

	// GET /invites/{code} → the invite's guild (nil for a group-DM invite).
	var invite struct {
		Guild *struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"guild"`
	}
	status, err := getJSON(ctx, token, baseURL+"/invites/"+url.PathEscape(code), &invite)
	if err != nil {
		return Resolved{}, fmt.Errorf("discordinvite: GET /invites: %w", err)
	}
	if status == http.StatusNotFound {
		return Resolved{}, ErrNotFound
	}
	if status != http.StatusOK {
		return Resolved{}, fmt.Errorf("discordinvite: GET /invites: HTTP %d", status)
	}
	if invite.Guild == nil {
		return Resolved{}, ErrNotFound
	}
	guild := Guild{ID: invite.Guild.ID, Name: invite.Guild.Name}

	// GET /guilds/{id}/channels → the guild's channels; a non-member Bot gets
	// 403 or 404 (Discord is inconsistent), both → ErrNoAccess.
	var channels []struct {
		ID       string `json:"id"`
		Type     int    `json:"type"`
		Name     string `json:"name"`
		Position int    `json:"position"`
	}
	status, err = getJSON(ctx, token, baseURL+"/guilds/"+url.PathEscape(guild.ID)+"/channels", &channels)
	if err != nil {
		return Resolved{}, fmt.Errorf("discordinvite: GET /guilds/channels: %w", err)
	}
	if status == http.StatusForbidden || status == http.StatusNotFound {
		return Resolved{}, ErrNoAccess
	}
	if status != http.StatusOK {
		return Resolved{}, fmt.Errorf("discordinvite: GET /guilds/channels: HTTP %d", status)
	}

	// Keep only voice channels, remembering position for the sort.
	type positioned struct {
		vc  VoiceChannel
		pos int
	}
	kept := make([]positioned, 0, len(channels))
	for _, c := range channels {
		if c.Type != guildVoiceType {
			continue
		}
		kept = append(kept, positioned{vc: VoiceChannel{ID: c.ID, Name: c.Name}, pos: c.Position})
	}
	sort.SliceStable(kept, func(i, j int) bool {
		if kept[i].pos != kept[j].pos {
			return kept[i].pos < kept[j].pos
		}
		return kept[i].vc.Name < kept[j].vc.Name
	})
	voices := make([]VoiceChannel, len(kept))
	for i, k := range kept {
		voices[i] = k.vc
	}

	log.Debug("resolved Discord invite", "guild", guild.Name, "voiceChannels", len(voices))
	return Resolved{Guild: guild, VoiceChannels: voices}, nil
}

// getJSON issues an authenticated Bot GET and, on 200, decodes the body into out.
// It returns the HTTP status (so the caller maps 403/404 to sentinels) and an
// error only for transport or decode failures.
func getJSON(ctx context.Context, token, rawURL string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bot "+token)
	req.Header.Set("User-Agent", userAgent)

	resp, err := inviteClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return resp.StatusCode, fmt.Errorf("decode: %w", err)
	}
	return resp.StatusCode, nil
}
