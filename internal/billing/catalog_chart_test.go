package billing_test

import (
	"encoding/json"
	"os"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/MrWong99/Glyphoxa/internal/billing"
)

// chartValuesPath is the shipped chart's values file, relative to this package.
const chartValuesPath = "../../deploy/charts/glyphoxa/values.yaml"

// TestChartDefaultCatalogParses is the #521 launch-catalog gate: the plan
// catalog the chart ships as its DEFAULT must survive the same strict parser
// `glyphoxa billing plans-sync` runs (ADR-0054) — unknown keys, a bad key
// source, or an allowance on a BYOK tier all fail the sync at deploy time, and
// finding that out during a launch install is far too late.
//
// It also pins the launch posture itself: a platform-keyed trial with a
// configured monthly allowance, and a free BYOK tier for sustained use, both
// free of charge — the public-beta promise the landing page makes.
func TestChartDefaultCatalogParses(t *testing.T) {
	raw, err := os.ReadFile(chartValuesPath)
	if err != nil {
		t.Fatalf("read chart values: %v", err)
	}

	// The chart authors the catalog as YAML under plans.catalog and renders it
	// to JSON in the ConfigMap; do the same round-trip here so this test parses
	// exactly what the cluster would.
	var values struct {
		Plans struct {
			Catalog map[string]any `yaml:"catalog"`
		} `yaml:"plans"`
	}
	if err := yaml.Unmarshal(raw, &values); err != nil {
		t.Fatalf("parse chart values: %v", err)
	}
	catalogJSON, err := json.Marshal(values.Plans.Catalog)
	if err != nil {
		t.Fatalf("marshal catalog to JSON: %v", err)
	}

	catalog, err := billing.ParseCatalog(catalogJSON)
	if err != nil {
		t.Fatalf("the chart's default plan catalog does not parse: %v\ncatalog: %s", err, catalogJSON)
	}

	bySlug := map[string]billing.Plan{}
	for _, p := range catalog.Plans {
		bySlug[p.Slug] = p
	}

	trial, ok := bySlug["beta-trial"]
	if !ok {
		t.Fatalf("no beta-trial plan in the shipped catalog; got %v", catalog.Plans)
	}
	if trial.KeySource != billing.KeySourcePlatform {
		t.Errorf("beta-trial key_source = %q, want %q — the trial spends the deployment's keys",
			trial.KeySource, billing.KeySourcePlatform)
	}
	if trial.IncludedUsageUSD == nil || *trial.IncludedUsageUSD <= 0 {
		t.Errorf("beta-trial included_usage_usd = %v, want a positive allowance", trial.IncludedUsageUSD)
	}

	byok, ok := bySlug["byok-free"]
	if !ok {
		t.Fatalf("no byok-free plan in the shipped catalog; got %v", catalog.Plans)
	}
	if byok.KeySource != billing.KeySourceBYOK {
		t.Errorf("byok-free key_source = %q, want %q", byok.KeySource, billing.KeySourceBYOK)
	}

	// Nothing in the beta charges money: there is no payment flow to charge it
	// with, so a non-zero price would be a promise signup cannot keep.
	for _, p := range catalog.Plans {
		if p.MonthlyPriceUSD != 0 {
			t.Errorf("plan %q costs %v/month, but the beta bills nobody", p.Slug, p.MonthlyPriceUSD)
		}
	}
}
