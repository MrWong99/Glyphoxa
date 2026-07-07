//go:build !opus

package codec

import (
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	gxvoice "github.com/MrWong99/Glyphoxa/pkg/voice"
	"github.com/MrWong99/Glyphoxa/pkg/voice/wire"
)

// TestStubAcceptsMetricsOption pins that the default (no -tags opus) build accepts
// the same New(WithMetrics(...)) construction the opus build does (#125): the
// option must exist and be ignored in the stub, or the tag-less CI that wires
// codec.New(codec.WithMetrics(...)) fails to compile. The stub still reports the
// codec unavailable.
func TestStubAcceptsMetricsOption(t *testing.T) {
	c := New(WithMetrics(observe.Discard{}))
	if _, err := c.DecodeInbound(gxvoice.Frame{UserID: 1, Opus: []byte{0x1}}); err != wire.ErrCodecUnavailable {
		t.Fatalf("stub DecodeInbound err = %v, want ErrCodecUnavailable", err)
	}
}
