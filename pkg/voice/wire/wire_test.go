package wire_test

import (
	"errors"
	"testing"

	gxvoice "github.com/MrWong99/Glyphoxa/pkg/voice"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/wire"
)

// Compile-time assertion: the stub satisfies the Codec contract the real
// transcoder will implement.
var _ wire.Codec = wire.UnavailableCodec()

// TestUnavailableCodec_ReportsUnavailable pins the codec-boundary stub: until
// the Opus↔PCM transcoder is built, both directions report
// [wire.ErrCodecUnavailable] rather than silently dropping audio. This is the
// explicit boundary the wiring leaves for the codec to fill; the full
// Pipeline.Run loop around it is exercised by the manual live-NPC run, which
// needs a real Discord Session and provider keys (out of scope for unit tests).
func TestUnavailableCodec_ReportsUnavailable(t *testing.T) {
	c := wire.UnavailableCodec()

	if _, err := c.DecodeInbound(gxvoice.Frame{}); !errors.Is(err, wire.ErrCodecUnavailable) {
		t.Errorf("DecodeInbound err = %v, want ErrCodecUnavailable", err)
	}
	if _, err := c.PlaybackSource(make(chan tts.AudioChunk)); !errors.Is(err, wire.ErrCodecUnavailable) {
		t.Errorf("PlaybackSource err = %v, want ErrCodecUnavailable", err)
	}
}
