//go:build !opus

package codec

import (
	"github.com/MrWong99/Glyphoxa/internal/observe"
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

// Option configures a [Codec]. The stub accepts the same construction shape as the
// opus build (so wirenpc's codec.New(codec.WithMetrics(...)) compiles tag-lessly)
// but has nothing to configure.
type Option func(*Codec)

// WithMetrics is the stub's no-op counterpart to the opus build's per-frame codec
// instrumentation: it accepts the recorder and ignores it (the stub decodes and
// encodes nothing).
func WithMetrics(observe.StageRecorder) Option { return func(*Codec) {} }

// New returns the stub Codec. It implements [wire.Codec]. Options are accepted for
// signature parity with the opus build and ignored.
func New(opts ...Option) *Codec {
	c := &Codec{}
	for _, o := range opts {
		o(c)
	}
	return c
}

var _ wire.Codec = (*Codec)(nil)

// DecodeInbound reports the codec is unavailable in this build.
func (*Codec) DecodeInbound(gxvoice.Frame) ([]audio.Frame, error) {
	return nil, wire.ErrCodecUnavailable
}

// PlaybackSource reports the codec is unavailable in this build.
func (*Codec) PlaybackSource(<-chan tts.AudioChunk) (gxvoice.Source, error) {
	return nil, wire.ErrCodecUnavailable
}
