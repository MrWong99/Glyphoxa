package presence

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// Discord's own command-permission settings are a UX hint a Guild admin can edit
// (ADR-0010), so every interaction is authorized here, server-side. Two sentinel
// denials let the Registry map each to its own ephemeral text.
var (
	// ErrWrongGuild denies an interaction that did not resolve to a Tenant: a DM
	// (which carries no Guild) or a Guild no Tenant has configured.
	ErrWrongGuild = errors.New("presence: interaction is not in a known Tenant Guild")
	// ErrNotOperator denies a GM-only command invoked by a Discord User who is not a
	// GM IN THE RESOLVED TENANT (ADR-0010 "GM only"; per-Tenant GM identity per
	// ADR-0055/#490 — a Tenant A operator is not GM in Tenant B).
	ErrNotOperator = errors.New("presence: user is not a GM in this Tenant")
)

// GMChecker reports whether a Discord snowflake is a GM WITHIN a given Tenant. It
// must never block — IsGMInTenant runs inside the interaction dispatch against
// Discord's 3s response deadline. *auth.GMIdentity satisfies it: the Tenant's
// operator binding union the env allowlist (a deployment-wide override), served
// from a cached snapshot (ADR-0055/#490).
type GMChecker interface {
	IsGMInTenant(tenantID uuid.UUID, discordUserID string) bool
}

// TenantResolver maps an interaction's Guild to its owning Tenant — the first thing
// dispatch does, before any storage read touches campaigns (#490). An unknown Guild
// returns a non-nil error, which the Gate turns into a clean ephemeral rejection.
// The prod impl is a thin storage adapter over GetTenantIDByGuildID (newest-wins on
// a duplicate guild_id); see [NewStorageTenantResolver].
type TenantResolver interface {
	TenantForGuild(ctx context.Context, guildID string) (uuid.UUID, error)
}

// Gate is the server-side authorization check for slash-command interactions
// (ADR-0010). It resolves the interaction's owning Tenant from its Guild and then
// applies the permission rule per-Tenant — replacing #489's interim "any known
// Guild" seam with a real Guild→Tenant routing that restores per-Tenant GM scoping
// (#490).
type Gate struct {
	gm      GMChecker
	tenants TenantResolver
}

// NewGate builds a Gate over the per-Tenant GM checker and the Guild→Tenant
// resolver. tenants must be non-nil (a nil resolver would deny every command).
func NewGate(gm GMChecker, tenants TenantResolver) *Gate {
	return &Gate{gm: gm, tenants: tenants}
}

// Authorize resolves the interaction's Tenant from its Guild and applies the
// command's permission rule, returning the resolved Tenant id for the dispatch to
// thread into the handler. A DM (empty guildID) or a Guild no Tenant configured is
// denied ErrWrongGuild — the /roll rule (ADR-0010): anyone in a KNOWN Tenant Guild,
// no user check. A gmOnly command additionally requires the invoker be a GM IN THE
// RESOLVED TENANT (the "GM only" rule, ADR-0055/#490); a nil checker fails closed.
// Any resolver failure (unknown Guild or a transient DB error) fails CLOSED as
// ErrWrongGuild — an interaction is never authorized against an unresolved Tenant.
func (g *Gate) Authorize(ctx context.Context, guildID, userID string, gmOnly bool) (uuid.UUID, error) {
	if guildID == "" {
		return uuid.Nil, ErrWrongGuild
	}
	tenantID, err := g.tenants.TenantForGuild(ctx, guildID)
	if err != nil {
		return uuid.Nil, ErrWrongGuild
	}
	if gmOnly {
		if g.gm == nil || !g.gm.IsGMInTenant(tenantID, userID) {
			return uuid.Nil, ErrNotOperator
		}
	}
	return tenantID, nil
}
