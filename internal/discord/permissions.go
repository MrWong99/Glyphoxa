package discord

import (
	"log/slog"
	"slices"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

// interactionMember provides access to the interaction's member and user.
// All disgo interaction event types satisfy this interface.
type interactionMember interface {
	Member() *discord.ResolvedMember
	User() discord.User
}

// PermissionChecker validates that a Discord user has the DM role
// before executing privileged slash commands.
type PermissionChecker struct {
	dmRoleID snowflake.ID
	hasRole  bool // true if a DM role ID was configured
}

// NewPermissionChecker creates a PermissionChecker with the given DM role ID.
// If dmRoleID is empty, all users are treated as DMs (useful for development).
func NewPermissionChecker(dmRoleID string) *PermissionChecker {
	p := &PermissionChecker{}
	if dmRoleID != "" {
		id, err := snowflake.Parse(dmRoleID)
		if err != nil {
			slog.Warn("discord: invalid DM role ID, treating all users as DMs", "dm_role_id", dmRoleID, "err", err)
			return p
		}
		p.dmRoleID = id
		p.hasRole = true
	}
	return p
}

// IsDM checks whether the interaction author has the configured DM role.
// If dmRoleID is empty, all users are treated as DMs (useful for development).
// Returns false if the interaction has no Member (e.g., DM channel interactions).
func (p *PermissionChecker) IsDM(e interactionMember) bool {
	if !p.hasRole {
		return true
	}
	member := e.Member()
	if member == nil {
		return false
	}
	slog.Debug("checking permissions for user", "username", e.User().EffectiveName(), "roles", member.RoleIDs)
	return slices.Contains(member.RoleIDs, p.dmRoleID)
}
