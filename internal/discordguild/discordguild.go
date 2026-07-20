// Package discordguild proves a Discord User administers a Guild (#504,
// ADR-0058): the guild-ownership proof SaveDiscordSettings runs before binding a
// guild_id to a Tenant, so a squat-first attacker cannot bind a guild they do
// not administer. It makes at most two REST calls with the deployment Bot token:
// `GET /guilds/{id}` for the owner + role table, and — when the user is not the
// owner — `GET /guilds/{id}/members/{userID}` for the user's roles. "Administers"
// means: guild owner, or any held role (the @everyone role included) carrying
// ADMINISTRATOR (0x8) or MANAGE_GUILD (0x20).
//
// Like internal/discordinvite and internal/discordtag, the calls are plain
// net/http GETs, deliberately NOT disgo's rest.NewClient: that client's rate
// limiter starts a cleanup goroutine its Close never stops, leaking one
// goroutine + ticker per call (ADR-0047).
//
// These are LIVE network calls. The RPC layer hides CheckAdmin behind a seam so
// unit tests fake the checker and never touch the network; offline tests drive
// the package-private base-URL seam ([CheckAdminAt]) at a fake HTTP server.
package discordguild

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// liveAPIBaseURL is Discord's REST API root, matching internal/discordinvite.
const liveAPIBaseURL = "https://discord.com/api/v10"

// userAgent is the DiscordBot form Discord requires of bot REST callers.
const userAgent = "DiscordBot (https://github.com/MrWong99/Glyphoxa, v2)"

// Discord permission bits that count as "administers the guild": either grants
// full guild-settings control, which is the bar for binding the guild to a
// Tenant (#504).
const (
	permAdministrator uint64 = 0x8
	permManageGuild   uint64 = 0x20
)

// guildClient is shared across calls (http.Client is safe for concurrent use);
// its Timeout is a belt on top of the caller's ctx deadline (ADR-0047).
var guildClient = &http.Client{Timeout: 15 * time.Second}

// ErrBotNotInGuild means the Bot itself cannot read the guild: the guild read
// (or the member read) came back 403 or 404 — Discord is inconsistent about
// which for a non-member Bot, so both map here (ADR-0047 precedent).
var ErrBotNotInGuild = errors.New("discordguild: the Bot is not a member of that guild")

// ErrUserNotInGuild means the guild resolved but the user is not a member of it
// (member read 404 while the guild read succeeded).
var ErrUserNotInGuild = errors.New("discordguild: user is not a member of that guild")

// ErrNoPermission means the user is a member but none of their roles carries
// ADMINISTRATOR or MANAGE_GUILD.
var ErrNoPermission = errors.New("discordguild: user lacks Manage Guild")

// CheckAdmin proves userID administers guildID using token (a Discord Bot
// token): nil when the user is the guild owner or holds a role with
// ADMINISTRATOR/MANAGE_GUILD, else one of the sentinel errors above. An empty
// token fails fast (no dial). A ctx deadline bounds each call. log may be nil.
func CheckAdmin(ctx context.Context, token, guildID, userID string, log *slog.Logger) error {
	return checkAdmin(ctx, token, guildID, userID, "", log)
}

// checkAdmin is CheckAdmin with a base-URL seam: "" means the live Discord API,
// tests point it at a fake HTTP server.
func checkAdmin(ctx context.Context, token, guildID, userID, baseURL string, log *slog.Logger) error {
	if token == "" {
		return errors.New("discordguild: empty bot token")
	}
	if log == nil {
		log = slog.Default()
	}
	if baseURL == "" {
		baseURL = liveAPIBaseURL
	}

	// GET /guilds/{id} → owner_id + the guild's role table. The Bot must be a
	// member to read this; 403/404 both mean it is not (Discord is inconsistent).
	var guild struct {
		OwnerID string `json:"owner_id"`
		Roles   []struct {
			ID          string `json:"id"`
			Permissions string `json:"permissions"` // decimal STRING per Discord API
		} `json:"roles"`
	}
	status, err := getJSON(ctx, token, baseURL+"/guilds/"+url.PathEscape(guildID), &guild)
	if err != nil {
		return fmt.Errorf("discordguild: GET /guilds: %w", err)
	}
	if status == http.StatusForbidden || status == http.StatusNotFound {
		return ErrBotNotInGuild
	}
	if status != http.StatusOK {
		return fmt.Errorf("discordguild: GET /guilds: HTTP %d", status)
	}
	if guild.OwnerID == userID {
		log.Debug("guild-admin proof: user is the guild owner", "guild", guildID)
		return nil
	}

	// GET /guilds/{id}/members/{userID} → the user's role ids. 404 here means
	// the user is not a member (the guild itself resolved above); 403 means the
	// Bot lost read access between the two calls — collapse to not-in-guild.
	var member struct {
		Roles []string `json:"roles"`
	}
	status, err = getJSON(ctx, token,
		baseURL+"/guilds/"+url.PathEscape(guildID)+"/members/"+url.PathEscape(userID), &member)
	if err != nil {
		return fmt.Errorf("discordguild: GET /guilds/members: %w", err)
	}
	if status == http.StatusNotFound {
		return ErrUserNotInGuild
	}
	if status == http.StatusForbidden {
		return ErrBotNotInGuild
	}
	if status != http.StatusOK {
		return fmt.Errorf("discordguild: GET /guilds/members: HTTP %d", status)
	}

	// Union of the member's roles plus @everyone (role id == guild id, held by
	// every member implicitly). Any role carrying ADMINISTRATOR or MANAGE_GUILD
	// proves administration. Permissions is a decimal string.
	held := map[string]bool{guildID: true}
	for _, id := range member.Roles {
		held[id] = true
	}
	for _, role := range guild.Roles {
		if !held[role.ID] {
			continue
		}
		perms, err := strconv.ParseUint(role.Permissions, 10, 64)
		if err != nil {
			return fmt.Errorf("discordguild: parse role %s permissions %q: %w", role.ID, role.Permissions, err)
		}
		if perms&(permAdministrator|permManageGuild) != 0 {
			log.Debug("guild-admin proof: role grants Manage Guild", "guild", guildID, "role", role.ID)
			return nil
		}
	}
	return ErrNoPermission
}

// getJSON issues an authenticated Bot GET and, on 200, decodes the body into
// out. It returns the HTTP status (so the caller maps 403/404 to sentinels) and
// an error only for transport or decode failures.
func getJSON(ctx context.Context, token, rawURL string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bot "+token)
	req.Header.Set("User-Agent", userAgent)

	resp, err := guildClient.Do(req)
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
