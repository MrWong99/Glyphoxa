//go:build opus

package mixdown

import (
	"fmt"

	"github.com/hraban/opus"
)

// This file is built only under `-tags opus` and links the system libopus (via
// github.com/hraban/opus). Always pair it with `-tags nolibopusfile` so
// hraban/opus does not also require libopusfile. It provides the real
// [DecoderFactory]: one fresh libopus decoder per lane, decoding to mono 48 kHz
// PCM — the same rate/layout the internal mix runs at, and independent of the
// live codec's per-speaker decoders.
func init() {
	defaultDecoderFactory = newOpusDecoder
}

// maxFrameSamples bounds the decode scratch: the largest Opus frame is 120 ms,
// which at 48 kHz mono is 5760 samples.
const maxFrameSamples = decodeRate * 120 / 1000 // 5760

type opusLaneDecoder struct {
	dec *opus.Decoder
	buf []int16
}

func newOpusDecoder() (Decoder, error) {
	dec, err := opus.NewDecoder(decodeRate, 1)
	if err != nil {
		return nil, fmt.Errorf("mixdown: new Opus decoder: %w", err)
	}
	return &opusLaneDecoder{dec: dec, buf: make([]int16, maxFrameSamples)}, nil
}

func (o *opusLaneDecoder) Decode(payload []byte) ([]int16, error) {
	n, err := o.dec.Decode(payload, o.buf)
	if err != nil {
		return nil, fmt.Errorf("mixdown: decode Opus frame: %w", err)
	}
	out := make([]int16, n)
	copy(out, o.buf[:n])
	return out, nil
}
