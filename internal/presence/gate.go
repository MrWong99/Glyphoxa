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
// (ADR-0010). Under the per-Tenant client registry (#489) the presence no longer
// has one configured Guild, so the Gate accepts any KNOWN Guild — the configured
// Guild of some resolved Tenant (wired to Clients.KnownGuild) — until #490's
// TenantResolver narrows an interaction to its owning Tenant. A wait-state (no
// Tenant resolved yet) knows no Guild and denies every command.
type Gate struct {
	gm    GMChecker
	known func(guildID string) bool
}

// NewGate builds a Gate over the GM identity checker and a predicate reporting
// whether a Guild is a known (resolved-Tenant) Guild (wired to
// Clients.KnownGuild). known must be non-nil.
func NewGate(gm GMChecker, known func(guildID string) bool) *Gate {
	return &Gate{gm: gm, known: known}
}

// CheckGuild passes when the interaction happened in a known Guild. This is the
// /roll rule (ADR-0010): anyone in a configured Guild, no user check. A DM (empty
// interactionGuildID) is denied, as is any interaction in an unknown Guild
// (including while no Tenant is resolved yet).
func (g *Gate) CheckGuild(interactionGuildID string) error {
	if interactionGuildID == "" || !g.known(interactionGuildID) {
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
