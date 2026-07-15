//go:build opus

package mixdown

import (
	"fmt"

	"github.com/pion/opus"
)

// This file is built only under `-tags opus`, matching the live codec's opt-in
// split (the tag no longer implies CGO — the codec is the pure-Go
// github.com/pion/opus). It provides the real [DecoderFactory]: one fresh Opus
// decoder per lane, decoding to mono 48 kHz PCM — the same rate/layout the
// internal mix runs at, and independent of the live codec's per-speaker
// decoders.
func init() {
	defaultDecoderFactory = newOpusDecoder
}

// maxFrameSamples bounds the decode scratch: the largest Opus frame is 120 ms,
// which at 48 kHz mono is 5760 samples.
const maxFrameSamples = decodeRate * 120 / 1000 // 5760

type opusLaneDecoder struct {
	dec opus.Decoder
	buf []int16
}

func newOpusDecoder() (Decoder, error) {
	dec, err := opus.NewDecoderWithOutput(decodeRate, 1)
	if err != nil {
		return nil, fmt.Errorf("mixdown: new Opus decoder: %w", err)
	}
	return &opusLaneDecoder{dec: dec, buf: make([]int16, maxFrameSamples)}, nil
}

func (o *opusLaneDecoder) Decode(payload []byte) ([]int16, error) {
	n, err := o.dec.DecodeToInt16(payload, o.buf)
	if err != nil {
		return nil, fmt.Errorf("mixdown: decode Opus frame: %w", err)
	}
	out := make([]int16, n)
	copy(out, o.buf[:n])
	return out, nil
}
