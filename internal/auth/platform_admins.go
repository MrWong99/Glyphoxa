package auth

import "github.com/MrWong99/Glyphoxa/internal/storage"

// PlatformAdmins answers "is this person a Platform Admin" (ADR-0065): the
// people who administer the instance itself — admit and remove others,
// suspend, decide who spends Platform Keys. They are configured in the
// environment ONLY (GLYPHOXA_OPERATOR_IDS): the one credential that can admit
// everyone else must not be editable by a compromised browser session, so
// nothing in the database can make someone a Platform Admin.
//
// The composition root builds ONE value and hands it to everything that asks
// the question (the OAuth callback, GetCurrentUser, the AdminService), so they
// cannot disagree. The zero value recognises nobody.
type PlatformAdmins struct {
	// Allowlist is the parsed GLYPHOXA_OPERATOR_IDS.
	Allowlist OperatorAllowlist
	// Dev makes the synthetic dev operator a Platform Admin, so the Admin
	// screen is reachable on a GLYPHOXA_DEV_MODE instance. Never set outside
	// dev mode: [storage.DevOperatorDiscordID] is not a real snowflake, but a
	// leftover dev users row may exist in a production database.
	Dev bool
}

// Is reports whether the user is a Platform Admin.
func (p PlatformAdmins) Is(u storage.User) bool {
	return p.IsDiscordUser(u.DiscordUserID)
}

// IsDiscordUser is [PlatformAdmins.Is] by Discord User ID, for callers that
// hold only the snowflake — an AdminService target who may never have signed
// in.
func (p PlatformAdmins) IsDiscordUser(discordUserID string) bool {
	if p.Allowlist.Contains(discordUserID) {
		return true
	}
	return p.Dev && discordUserID == storage.DevOperatorDiscordID
}
