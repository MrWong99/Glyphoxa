//go:build !opus

package codec

import (
	gxvoice "github.com/MrWong99/Glyphoxa/pkg/voice"
	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/wire"
)

// Codec is the default stub: the real transcoder links libopus and is built only
// under `-tags opus`, so without that tag every operation reports
// [wire.ErrCodecUnavailable]. This keeps the tree green under the team's plain
// `CGO_ENABLED=1 go test ./...` (no libopus needed) without silently
// substituting a no-op codec — the same opt-in pattern as the DAVE `-tags dave`
// build. The pure-Go DSP in ./dsp is always built and tested regardless.
type Codec struct{}

// New returns the stub Codec. It implements [wire.Codec].
func New() *Codec { return &Codec{} }

var _ wire.Codec = (*Codec)(nil)

// DecodeInbound reports the codec is unavailable in this build.
func (*Codec) DecodeInbound(gxvoice.Frame) ([]audio.Frame, error) {
	return nil, wire.ErrCodecUnavailable
}

// PlaybackSource reports the codec is unavailable in this build.
func (*Codec) PlaybackSource(<-chan tts.AudioChunk) (gxvoice.Source, error) {
	return nil, wire.ErrCodecUnavailable
}
