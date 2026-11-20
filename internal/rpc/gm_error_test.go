package rpc_test

import (
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
)

// TestGMText pins the GM-facing rewrite of lower-package sentinel text: the
// package qualifier and a trailing ADR reference go, the meaning stays.
func TestGMText(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		`storage: invalid tag: "x" is too long (max 40 characters)`: `invalid tag: "x" is too long (max 40 characters)`,
		llmbuild.ErrNoPlatformKeyEntitlement.Error():                "tenant has no platform-key entitlement (BYOK plan) — save a provider API key in Configuration",
		"entry not found":    "entry not found",
		"Groq: key rejected": "Groq: key rejected", // not a package qualifier
	} {
		if got := rpc.GMText(in); got != want {
			t.Errorf("GMText(%q) = %q, want %q", in, got, want)
		}
	}
}
