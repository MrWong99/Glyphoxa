package main

import (
	"maps"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/auth"
)

// TestPlainMountPolicy pins the PRODUCTION plain-mount auth table (#446).
// auth.MustGuardMounts already makes an UNDECLARED row fail the boot, but a
// MIS-declared one — a byte mount downgraded from TenantRequired, the exact
// #408 regression shape — would otherwise ship with every test green (the
// review's mutation check proved it: flipping the clip row to TenantNone
// changed no test outcome). This literal restatement turns that one-line
// downgrade into a loud failure: changing a mount's posture now requires
// touching this test too, i.e. doing it deliberately.
func TestPlainMountPolicy(t *testing.T) {
	t.Parallel()
	want := map[string]auth.TenantMode{
		"GET /api/v1/sessions/{id}/events":  auth.TenantRequired,
		"GET /api/v1/sessions/{id}":         auth.TenantRequired,
		"GET /api/v1/highlights/{id}/clip":  auth.TenantRequired,
		"GET /api/v1/highlights/{id}/image": auth.TenantRequired,
		"GET /api/v1/campaigns/{id}/export": auth.TenantRequired,
		"POST /api/v1/campaigns/import":     auth.TenantNone,
	}
	if !maps.Equal(plainMountPolicy, want) {
		t.Fatalf("plainMountPolicy drifted from the pinned #446 table:\n got  %v\n want %v\n"+
			"If this change is intentional, update BOTH maps — a silent posture downgrade is the #408 class.",
			plainMountPolicy, want)
	}
	// Belt and braces: every GET byte/SSE mount must stay tenant-scoped (#439).
	for pattern, mode := range plainMountPolicy {
		if len(pattern) > 4 && pattern[:4] == "GET " && mode != auth.TenantRequired {
			t.Errorf("GET mount %q is not TenantRequired — the #408 subset", pattern)
		}
	}
}
