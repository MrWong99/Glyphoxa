package main

import (
	"maps"
	"slices"
	"strings"
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
		// Session Highlight generated sound (#312) — same byte-mount posture as
		// the clip and image for the same #408 reason.
		"GET /api/v1/highlights/{id}/sound": auth.TenantRequired,
		// Campaign Map images (#538, ADR-0060) — the second blob owner's byte route.
		// TenantRequired like every other byte mount: the handler re-reads the
		// injected tenant to scope its row load, and a session-only gate would 401.
		"GET /api/v1/maps/{id}/image": auth.TenantRequired,
		// Node portraits (#590) — the third blob owner's byte route, same posture
		// for the same reason.
		"GET /api/v1/knowledge/nodes/{id}/portrait": auth.TenantRequired,
		"GET /api/v1/campaigns/{id}/export":         auth.TenantRequired,
		// TenantRequired since ADR-0065: the import lands in the Tenant the
		// policy authorized for the request, not in whatever Tenant the user
		// operates — which a revoked GM still does.
		"POST /api/v1/campaigns/import": auth.TenantRequired,
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

// TestPlainMountGuestPolicy pins the guest half of the plain-mount table
// (ADR-0065): which byte mounts a Campaign Guest may reach, and with which
// Guest Right. A mount absent from the table is GM-only, so opening one to
// guests — or loosening the right one needs — now means touching this literal
// too, i.e. doing it deliberately.
func TestPlainMountGuestPolicy(t *testing.T) {
	t.Parallel()
	want := map[string][]auth.GuestRight{
		"GET /api/v1/sessions/{id}":                 {auth.RightTranscriptsRead},
		"GET /api/v1/highlights/{id}/clip":          {auth.RightHighlightsRead},
		"GET /api/v1/highlights/{id}/image":         {auth.RightHighlightsRead},
		"GET /api/v1/highlights/{id}/sound":         {auth.RightHighlightsRead},
		"GET /api/v1/maps/{id}/image":               {auth.RightMapsRead},
		"GET /api/v1/knowledge/nodes/{id}/portrait": {auth.RightWikiRead},
	}
	if len(plainMountGuestPolicy) != len(want) {
		t.Errorf("plainMountGuestPolicy has %d rows, want %d", len(plainMountGuestPolicy), len(want))
	}
	for pattern, rights := range want {
		posture, ok := plainMountGuestPolicy[pattern]
		if !ok || !posture.Reachable() {
			t.Errorf("%s: want guest-reachable needing %v, but it is GM-only", pattern, rights)
			continue
		}
		// A posture with no rights would be "every guest": no byte mount is that.
		if !slices.Equal(posture.Rights(), rights) {
			t.Errorf("%s: requires %v, want %v", pattern, posture.Rights(), rights)
		}
	}
	for pattern, posture := range plainMountGuestPolicy {
		if _, ok := want[pattern]; !ok {
			t.Errorf("%s is guest-reachable in production but not in the pinned table", pattern)
		}
		// A guest posture on a pattern that is not a mount would silently do
		// nothing — almost certainly a typo for one that should have it.
		if _, mounted := plainMountPolicy[pattern]; !mounted {
			t.Errorf("guest posture declared for %q, which is not a plain mount", pattern)
		}
		if !strings.HasPrefix(pattern, "GET ") {
			t.Errorf("%s: a state-changing mount is open to guests (%v)", pattern, posture.Rights())
		}
	}
	// The three that must stay closed, named — so removing one from this list
	// is as deliberate as adding a row above.
	for _, pattern := range []string{
		"GET /api/v1/sessions/{id}/events",
		"GET /api/v1/campaigns/{id}/export",
		importMountPattern,
	} {
		if plainMountGuestPolicy[pattern].Reachable() {
			t.Errorf("%s is open to Campaign Guests; it is GM-only by decision", pattern)
		}
		if _, mounted := plainMountPolicy[pattern]; !mounted {
			t.Errorf("%s is no longer a plain mount; update this list", pattern)
		}
	}
}
