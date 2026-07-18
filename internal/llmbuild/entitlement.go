package llmbuild

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
)

// This file is the ADR-0054 entitlement seam (a), decided in ADR-0055: the env
// fallback ("" key → the adapter reads the deployment's *_API_KEY) silently
// spends the deployment's Platform Keys, which is fine for a self-host but a
// cross-tenant hole the moment strangers can sign up. [ResolveKeyGated] refuses
// the fallback — INCLUDING the no-config-row path, which resolves to "" exactly
// like the seeded "env" placeholder — for a tenant the entitlement does not
// grant. In `allowlist` Admission Mode the hybrid policy (ADR-0039) is
// untouched: the composition root wires [EnvFallbackAllowed] (or nil); in
// `open` Admission Mode it swaps in [SubscriptionKeyGate] (cmd/glyphoxa runWeb,
// the one construction point).
//
// Gated everywhere on the web/all path (the phase-B inventory is closed): the
// live voice session (wirenpc resolveSessionKeys), the Recap engine, the
// Highlight image factory (cmd/glyphoxa), and the RPC tier's provider-key
// resolution (VoiceServer.resolveComponentKey — provider health pings,
// model/voice catalogs, TTS preview). ProviderServer.openKey is closed by
// construction: its only caller resolves the Discord Bot token, which is
// deployment infrastructure and deliberately outside the entitlement (as are
// VoiceServer.resolveDiscordToken and the presence/highlight token paths).
// The STANDALONE voice node (-mode voice) wires no entitlement by design: it
// is the single-operator self-host posture (deployment-scoped keys, tenant-
// free reads) and is not part of an open-admission SaaS deployment.

// ErrNoPlatformKeyEntitlement marks an env-fallback key resolution refused
// because the tenant has no platform-key entitlement (ADR-0054 gate (a)): a
// BYOK-plan tenant with no saved key must save one, never silently spend the
// deployment's env keys.
var ErrNoPlatformKeyEntitlement = errors.New("llmbuild: tenant has no platform-key entitlement (BYOK plan) — save a provider API key in Configuration (ADR-0054)")

// PlatformKeyEntitlement decides whether a tenant may resolve to the env
// fallback and spend the deployment's Platform Keys (ADR-0054/0055).
// Implementations must be safe for concurrent use.
type PlatformKeyEntitlement interface {
	PlatformKeyAllowed(ctx context.Context, tenantID uuid.UUID) (bool, error)
}

// EnvFallbackAllowed grants every tenant the env fallback — the `allowlist`
// Admission Mode posture (ADR-0055), where the hybrid BYOK policy (ADR-0039)
// stands unchanged and self-hosts run with zero subscription rows.
type EnvFallbackAllowed struct{}

// PlatformKeyAllowed always grants.
func (EnvFallbackAllowed) PlatformKeyAllowed(context.Context, uuid.UUID) (bool, error) {
	return true, nil
}

// PlatformSubscriptionChecker reports whether a tenant holds an active
// platform-key-source subscription. *storage.Store satisfies it via
// TenantHasPlatformKeySource.
type PlatformSubscriptionChecker interface {
	TenantHasPlatformKeySource(ctx context.Context, tenantID uuid.UUID) (bool, error)
}

// SubscriptionKeyGate is the `open`-mode entitlement (ADR-0055): a tenant may
// ride the env fallback only while an active subscription on a
// key_source='platform' Plan backs it.
type SubscriptionKeyGate struct {
	Subs PlatformSubscriptionChecker
}

// PlatformKeyAllowed defers to the subscription read.
func (g SubscriptionKeyGate) PlatformKeyAllowed(ctx context.Context, tenantID uuid.UUID) (bool, error) {
	return g.Subs.TenantHasPlatformKeySource(ctx, tenantID)
}

// ResolveKeyGated is [ResolveKey] behind the entitlement seam: a resolution
// that lands on the env fallback ("" — a nil cfg or the seeded "env"
// placeholder) consults ent for tenantID and refuses with
// [ErrNoPlatformKeyEntitlement] when not granted; an entitlement read error
// fails CLOSED. A real decrypted BYOK key passes without consulting ent — the
// gate protects the deployment's Platform Keys, not the tenant's own — and a
// key-resolution error surfaces as-is. A nil ent grants everything (the
// self-host / `allowlist` posture, identical to [ResolveKey]).
func ResolveKeyGated(ctx context.Context, ent PlatformKeyEntitlement, tenantID uuid.UUID, cipher *crypto.Cipher, cfg *storage.ProviderConfig, component storage.Component) (string, error) {
	key, err := ResolveKey(cipher, cfg, component)
	if err != nil || key != "" || ent == nil {
		return key, err
	}
	allowed, err := ent.PlatformKeyAllowed(ctx, tenantID)
	if err != nil {
		return "", fmt.Errorf("llmbuild: %s key entitlement check for tenant %s: %w", component, tenantID, err)
	}
	if !allowed {
		return "", fmt.Errorf("llmbuild: resolve %s key for tenant %s: %w", component, tenantID, ErrNoPlatformKeyEntitlement)
	}
	return "", nil
}
