package discordtag_test

import (
	"context"
	"strings"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/discordtag"
)

// TestResolve_EmptyToken_FailsFastNoNetwork pins the only offline-deterministic
// path: an empty token is rejected before any gateway dial, so the default
// `go test` makes no live Discord call (ADR-0021). The live login itself is
// exercised by an operator run / a live build, behind the RPC layer's seam.
func TestResolve_EmptyToken_FailsFastNoNetwork(t *testing.T) {
	t.Parallel()
	_, err := discordtag.Resolve(context.Background(), "", nil)
	if err == nil {
		t.Fatal("Resolve with empty token returned nil error")
	}
	if !strings.Contains(err.Error(), "empty bot token") {
		t.Errorf("error %q does not mention the empty token", err)
	}
}
