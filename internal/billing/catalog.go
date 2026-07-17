// Package billing is the SaaS subscription foundation (ADR-0054): the
// declarative Plan catalog the operator syncs into the plan table, and the
// Usage Ledger sink that persists per-Tenant metered usage for cost
// attribution. It deliberately contains NO payment-processor integration —
// collecting money is a later, separate layer; this package makes tiers
// configurable and cost + revenue measurable.
package billing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
)

// KeySource is where a Plan's provider keys come from (ADR-0004 / ADR-0054).
type KeySource string

const (
	// KeySourceBYOK: the Tenant supplies its own provider credentials — the v1.0
	// default and the self-host posture (ADR-0004).
	KeySourceBYOK KeySource = "byok"
	// KeySourcePlatform: the deployment's own provider keys (the env-fallback
	// path the hybrid policy already supports, ADR-0039) serve the Tenant's
	// usage; the subscription price covers it.
	KeySourcePlatform KeySource = "platform"
)

// slugPattern keeps slugs URL- and CLI-safe: lowercase alphanumerics and
// hyphens, starting alphanumeric.
var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Plan is one subscription tier as declared in the operator's catalog file.
// The slug is the stable handle: syncs upsert by it, subscriptions snapshot it.
type Plan struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	Description string `json:"description,omitempty"`
	// MonthlyPriceUSD is the tier's monthly list price (0 for a free tier).
	MonthlyPriceUSD float64 `json:"monthly_price_usd"`
	// KeySource defaults to "byok" when omitted.
	KeySource KeySource `json:"key_source,omitempty"`
	// IncludedUsageUSD is the monthly estimated-USD provider-usage allowance a
	// 'platform' plan includes; nil = no configured allowance. Meaningless (and
	// rejected) on a 'byok' plan.
	IncludedUsageUSD *float64 `json:"included_usage_usd,omitempty"`
	// Limits is the extensible per-tier knob bag (max campaigns, feature flags,
	// …): stored as-is, consumers read the keys they know (ADR-0054).
	Limits map[string]any `json:"limits,omitempty"`
}

// Catalog is the top-level shape of the plans file (`plans.json`).
type Catalog struct {
	Plans []Plan `json:"plans"`
}

// ParseCatalog strictly decodes and validates a catalog file. Unknown JSON
// fields are rejected (a typoed key must fail the sync, not silently vanish),
// as are duplicate or malformed slugs, negative prices/allowances, unknown key
// sources, and an allowance on a BYOK plan. An empty plan list is valid — it
// only does something when paired with -archive-missing.
func ParseCatalog(data []byte) (Catalog, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var c Catalog
	if err := dec.Decode(&c); err != nil {
		return Catalog{}, fmt.Errorf("billing: parse catalog: %w", err)
	}
	if dec.More() {
		return Catalog{}, fmt.Errorf("billing: parse catalog: trailing data after the catalog object")
	}

	seen := map[string]struct{}{}
	for i := range c.Plans {
		p := &c.Plans[i]
		if p.KeySource == "" {
			p.KeySource = KeySourceBYOK
		}
		if err := p.validate(); err != nil {
			return Catalog{}, fmt.Errorf("billing: plan %d (%q): %w", i, p.Slug, err)
		}
		if _, dup := seen[p.Slug]; dup {
			return Catalog{}, fmt.Errorf("billing: duplicate plan slug %q", p.Slug)
		}
		seen[p.Slug] = struct{}{}
	}
	return c, nil
}

// validate checks one plan's invariants (KeySource already defaulted).
func (p *Plan) validate() error {
	if !slugPattern.MatchString(p.Slug) {
		return fmt.Errorf("slug must match %s", slugPattern)
	}
	if p.DisplayName == "" {
		return fmt.Errorf("display_name is required")
	}
	if p.MonthlyPriceUSD < 0 {
		return fmt.Errorf("monthly_price_usd must be >= 0 (got %v)", p.MonthlyPriceUSD)
	}
	switch p.KeySource {
	case KeySourceBYOK:
		if p.IncludedUsageUSD != nil {
			return fmt.Errorf("included_usage_usd is only valid on a %q plan", KeySourcePlatform)
		}
	case KeySourcePlatform:
		if p.IncludedUsageUSD != nil && *p.IncludedUsageUSD < 0 {
			return fmt.Errorf("included_usage_usd must be >= 0 (got %v)", *p.IncludedUsageUSD)
		}
	default:
		return fmt.Errorf("key_source must be %q or %q (got %q)", KeySourceBYOK, KeySourcePlatform, p.KeySource)
	}
	return nil
}

// LimitsJSON marshals the plan's limits bag for storage ('{}' when empty).
func (p *Plan) LimitsJSON() (json.RawMessage, error) {
	if len(p.Limits) == 0 {
		return json.RawMessage(`{}`), nil
	}
	b, err := json.Marshal(p.Limits)
	if err != nil {
		return nil, fmt.Errorf("billing: marshal limits for plan %q: %w", p.Slug, err)
	}
	return b, nil
}
