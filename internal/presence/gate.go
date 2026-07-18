package presence

import (
	"errors"
)

// Discord's own command-permission settings are a UX hint a Guild admin can edit
// (ADR-0010), so every interaction is authorized here, server-side. Two sentinel
// denials let the Registry map each to its own ephemeral text.
var (
	// ErrWrongGuild denies an interaction that did not happen in the configured
	// Guild (including a DM, which carries no Guild).
	ErrWrongGuild = errors.New("presence: interaction is not in the configured Guild")
	// ErrNotOperator denies a GM-only command invoked by a Discord User the
	// GMChecker does not recognize as a GM (ADR-0010 "GM only"; GM identity per
	// ADR-0055 — tenant-bound operator or allowlisted).
	ErrNotOperator = errors.New("presence: user is not a GM")
)

// GMChecker reports whether a Discord snowflake is a GM. It must never block —
// CheckGM runs inside the interaction dispatch against Discord's 3s response
// deadline. *auth.GMIdentity satisfies it: the tenant-operator binding union
// the env allowlist (ADR-0055, amending ADR-0050's GM-identity clause), served
// from a cached snapshot.
type GMChecker interface {
	IsGM(discordUserID string) bool
}

// Gate is the server-side authorization check for slash-command interactions
// (ADR-0010). It resolves the configured Guild lazily via guildID (the presence
// only learns its Guild once the deployment config is loaded), so a wait-state
// presence ("" Guild) denies every command until a Guild is configured.
type Gate struct {
	gm      GMChecker
	guildID func() string
}

// NewGate builds a Gate over the GM identity checker and a resolver for the
// configured Guild (wired to Presence.GuildID). guildID must be non-nil.
func NewGate(gm GMChecker, guildID func() string) *Gate {
	return &Gate{gm: gm, guildID: guildID}
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

// CheckGM is CheckGuild plus GM identity: the "GM only" rule (ADR-0010
// amendment). GM identity is the GMChecker's verdict — tenant-bound operator or
// env-allowlisted (ADR-0055) — until a real tenant_members.role exists. A nil
// checker fails closed (nobody is GM).
func (g *Gate) CheckGM(interactionGuildID, userID string) error {
	if err := g.CheckGuild(interactionGuildID); err != nil {
		return err
	}
	if g.gm == nil || !g.gm.IsGM(userID) {
		return ErrNotOperator
	}
	return nil
}
