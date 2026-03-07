package config

import (
	"context"
	"fmt"
	"regexp"
)

// LicenseTier represents the isolation and resource tier for a tenant.
type LicenseTier int

const (
	// TierShared uses shared infrastructure with per-tenant schema isolation.
	TierShared LicenseTier = iota

	// TierDedicated uses dedicated infrastructure (gateway, DB, workers).
	TierDedicated
)

// String returns the string representation of the license tier, matching
// the convention used by resilience.State.
func (t LicenseTier) String() string {
	switch t {
	case TierShared:
		return "shared"
	case TierDedicated:
		return "dedicated"
	default:
		return fmt.Sprintf("LicenseTier(%d)", int(t))
	}
}

// ParseLicenseTier converts a string to a LicenseTier.
// Returns an error for unrecognised values.
func ParseLicenseTier(s string) (LicenseTier, error) {
	switch s {
	case "shared":
		return TierShared, nil
	case "dedicated":
		return TierDedicated, nil
	default:
		return 0, fmt.Errorf("config: unknown license tier %q", s)
	}
}

// validTenantID enforces safe tenant IDs that can be used as PostgreSQL
// schema names (tenant_<id>). Starts with a letter, alphanumeric + underscore,
// max 63 chars (PostgreSQL identifier limit).
var validTenantID = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// TenantContext carries tenant identity through the request/session lifecycle.
// In full mode, TenantID is "local" and LicenseTier is TierShared.
type TenantContext struct {
	TenantID    string
	LicenseTier LicenseTier
	CampaignID  string
	GuildID     string
}

// Validate checks that the TenantContext has a valid TenantID.
func (tc TenantContext) Validate() error {
	if !validTenantID.MatchString(tc.TenantID) {
		return fmt.Errorf("config: invalid tenant ID %q (must match %s)", tc.TenantID, validTenantID.String())
	}
	return nil
}

// tenantCtxKey is the unexported context key type for TenantContext.
// Using a struct type prevents cross-package collisions.
type tenantCtxKey struct{}

// WithTenant returns a new context carrying the given TenantContext.
func WithTenant(ctx context.Context, tc TenantContext) context.Context {
	return context.WithValue(ctx, tenantCtxKey{}, tc)
}

// TenantFromContext extracts the TenantContext from ctx.
// Returns the zero value and false if no tenant is set.
func TenantFromContext(ctx context.Context) (TenantContext, bool) {
	tc, ok := ctx.Value(tenantCtxKey{}).(TenantContext)
	return tc, ok
}

// LocalTenant returns a TenantContext for single-process full mode.
// CampaignID is derived from the campaign name in config.
func LocalTenant(campaignName string) TenantContext {
	cid := campaignName
	if cid == "" {
		cid = "default"
	}
	return TenantContext{
		TenantID:    "local",
		LicenseTier: TierShared,
		CampaignID:  cid,
	}
}
