// Package discordguild proves a Discord User administers a Guild (#504,
// ADR-0058): the guild-ownership proof SaveDiscordSettings runs before binding a
// guild_id to a Tenant, so a squat-first attacker cannot bind a guild they do
// not administer. It makes at most two REST calls with the deployment Bot token:
// `GET /guilds/{id}` for the owner + role table, and — when the user is not the
// owner — `GET /guilds/{id}/members/{userID}` for the user's roles. "Administers"
// means: guild owner, or any held role (the @everyone role included) carrying
// ADMINISTRATOR (0x8) or MANAGE_GUILD (0x20).
//
// The calls go through internal/discordrest (plain net/http GETs, never
// disgo's leaking rest client, ADR-0047).
//
// These are LIVE network calls. The RPC layer hides CheckAdmin behind a seam so
// unit tests fake the checker and never touch the network; offline tests drive
// the package-private base-URL seam ([CheckAdminAt]) at a fake HTTP server.
package discordguild

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	"github.com/MrWong99/Glyphoxa/internal/discordrest"
)

// Discord permission bits that count as "administers the guild": either grants
// full guild-settings control, which is the bar for binding the guild to a
// Tenant (#504).
const (
	permAdministrator uint64 = 0x8
	permManageGuild   uint64 = 0x20
)

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
	_, err := checkAdmin(ctx, token, guildID, userID, "", log)
	return err
}

// CheckAdminGuild is [CheckAdmin] that also returns the guild's display name,
// read by the same GET /guilds/{id} the proof makes, so a caller linking the
// guild can store the name without a second call. The name is "" whenever the
// error is not nil.
func CheckAdminGuild(ctx context.Context, token, guildID, userID string, log *slog.Logger) (string, error) {
	return checkAdmin(ctx, token, guildID, userID, "", log)
}

// checkAdmin is CheckAdminGuild with a base-URL seam: "" means the live
// Discord API, tests point it at a fake HTTP server.
func checkAdmin(ctx context.Context, token, guildID, userID, baseURL string, log *slog.Logger) (string, error) {
	if token == "" {
		return "", errors.New("discordguild: empty bot token")
	}
	if log == nil {
		log = slog.Default()
	}
	baseURL = discordrest.BaseURL(baseURL)

	// GET /guilds/{id} → owner_id + the guild's role table. The Bot must be a
	// member to read this; 403/404 both mean it is not (Discord is inconsistent).
	var guild struct {
		Name    string `json:"name"`
		OwnerID string `json:"owner_id"`
		Roles   []struct {
			ID          string `json:"id"`
			Permissions string `json:"permissions"` // decimal STRING per Discord API
		} `json:"roles"`
	}
	status, err := discordrest.GetJSON(ctx, token, baseURL+"/guilds/"+url.PathEscape(guildID), &guild)
	if err != nil {
		return "", fmt.Errorf("discordguild: GET /guilds: %w", err)
	}
	if status == http.StatusForbidden || status == http.StatusNotFound {
		return "", ErrBotNotInGuild
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("discordguild: GET /guilds: HTTP %d", status)
	}
	if guild.OwnerID == userID {
		log.Debug("guild-admin proof: user is the guild owner", "guild", guildID)
		return guild.Name, nil
	}

	// GET /guilds/{id}/members/{userID} → the user's role ids. 404 here means
	// the user is not a member (the guild itself resolved above); 403 means the
	// Bot lost read access between the two calls — collapse to not-in-guild.
	var member struct {
		Roles []string `json:"roles"`
	}
	status, err = discordrest.GetJSON(ctx, token,
		baseURL+"/guilds/"+url.PathEscape(guildID)+"/members/"+url.PathEscape(userID), &member)
	if err != nil {
		return "", fmt.Errorf("discordguild: GET /guilds/members: %w", err)
	}
	if status == http.StatusNotFound {
		return "", ErrUserNotInGuild
	}
	if status == http.StatusForbidden {
		return "", ErrBotNotInGuild
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("discordguild: GET /guilds/members: HTTP %d", status)
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
			return "", fmt.Errorf("discordguild: parse role %s permissions %q: %w", role.ID, role.Permissions, err)
		}
		if perms&(permAdministrator|permManageGuild) != 0 {
			log.Debug("guild-admin proof: role grants Manage Guild", "guild", guildID, "role", role.ID)
			return guild.Name, nil
		}
	}
	return "", ErrNoPermission
}
