package presence

import (
	"errors"

	"github.com/MrWong99/Glyphoxa/internal/auth"
)

// Discord's own command-permission settings are a UX hint a Guild admin can edit
// (ADR-0010), so every interaction is authorized here, server-side. Two sentinel
// denials let the Registry map each to its own ephemeral text.
var (
	// ErrWrongGuild denies an interaction that did not happen in the configured
	// Guild (including a DM, which carries no Guild).
	ErrWrongGuild = errors.New("presence: interaction is not in the configured Guild")
	// ErrNotOperator denies a GM-only command invoked by a Discord User whose
	// snowflake is not on the operator allowlist (ADR-0041 v1.0 "GM only").
	ErrNotOperator = errors.New("presence: user is not on the operator allowlist")
)

// Gate is the server-side authorization check for slash-command interactions
// (ADR-0010). It resolves the configured Guild lazily via guildID (the presence
// only learns its Guild once the deployment config is loaded), so a wait-state
// presence ("" Guild) denies every command until a Guild is configured.
type Gate struct {
	allow   auth.OperatorAllowlist
	guildID func() string
}

// NewGate builds a Gate over the operator allowlist and a resolver for the
// configured Guild (wired to Presence.GuildID). guildID must be non-nil.
func NewGate(allow auth.OperatorAllowlist, guildID func() string) *Gate {
	return &Gate{allow: allow, guildID: guildID}
}

// CheckGuild passes when the interaction happened in the configured Guild. This
// is the /roll rule (ADR-0010): anyone in the configured Guild, no user check.
// A DM (empty interactionGuildID) is denied, as is any interaction while the
// presence has no configured Guild yet.
func (g *Gate) CheckGuild(interactionGuildID string) error {
	want := g.guildID()
	if want == "" || interactionGuildID != want {
		return ErrWrongGuild
	}
	return nil
}

// CheckGM is CheckGuild plus operator-allowlist membership: the v1.0 "GM only"
// rule (ADR-0010 amendment / ADR-0041) until a real tenant_members.role exists.
func (g *Gate) CheckGM(interactionGuildID, userID string) error {
	if err := g.CheckGuild(interactionGuildID); err != nil {
		return err
	}
	if !g.allow.Contains(userID) {
		return ErrNotOperator
	}
	return nil
}
