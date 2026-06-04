// Package dsp holds the pure-Go signal pieces of the voice codec — a streaming
// linear resampler and a fixed-size reframer — with no cgo dependency. The
// Opus-linking codec (../, //go:build opus) composes these; keeping them here
// means they always build and are always unit-tested in the default suite,
// independent of whether libopus is linked.
package dsp

// Resampler converts a mono int16 PCM stream from one sample rate to another by
// linear interpolation, preserving fractional read position and the last input
// sample across calls. It is stateful by design: the outbound codec streams TTS
// chunks through it one at a time, and resetting per chunk would put an audible
// click at every chunk boundary.
//
// Linear interpolation is adequate here because the live NPC path is configured
// for an integer ratio (TTS at 48 kHz → encode-only, or 24 kHz → clean 2×) and
// the output is encoded through lossy Opus VOIP regardless; the Resampler exists
// for generality (arbitrary chunk rates and tests). It is not safe for
// concurrent use — one Resampler belongs to one stream, driven by one goroutine.
type Resampler struct {
	inRate  int
	outRate int

	prev   int16   // last input sample consumed; interpolation anchor for the next chunk
	pos    float64 // fractional read position into the current chunk, carried across calls
	primed bool    // true once the first sample has been seen
}

// NewResampler builds a Resampler from inRate to outRate (both Hz, > 0).
func NewResampler(inRate, outRate int) *Resampler {
	return &Resampler{inRate: inRate, outRate: outRate}
}

// Process resamples in (mono int16) and returns the output. When the rates are
// equal it is a pass-through copy. State carries across calls so a stream split
// into arbitrary chunks resamples identically to processing the whole at once.
func (r *Resampler) Process(in []int16) []int16 {
	if len(in) == 0 {
		return nil
	}
	if r.inRate == r.outRate {
		out := make([]int16, len(in))
		copy(out, in)
		r.prev = in[len(in)-1]
		r.primed = true
		return out
	}

	// On the very first sample we have no previous to interpolate against, so
	// seed prev with the first input sample (equivalent to holding it).
	if !r.primed {
		r.prev = in[0]
		r.primed = true
	}

	ratio := float64(r.inRate) / float64(r.outRate) // input samples per output sample
	out := make([]int16, 0, int(float64(len(in))/ratio)+2)

	// pos is relative to the start of this chunk: index -1 is r.prev (the
	// previous chunk's last sample), 0..len-1 are in[].
	for r.pos < float64(len(in)) {
		idx := int(r.pos) // floor (pos >= 0)
		frac := r.pos - float64(idx)

		var a int16
		if idx == 0 {
			a = r.prev
		} else {
			a = in[idx-1]
		}
		b := in[idx]

		out = append(out, lerp(a, b, frac))
		r.pos += ratio
	}
	// Carry the fractional remainder into the next chunk; stash this chunk's
	// last sample as the new interpolation anchor.
	r.pos -= float64(len(in))
	r.prev = in[len(in)-1]
	return out
}

// lerp linearly interpolates between a and b at t∈[0,1), rounding half away from
// zero and clamping to the int16 range.
func lerp(a, b int16, t float64) int16 {
	v := float64(a) + (float64(b)-float64(a))*t
	if v >= 0 {
		v += 0.5
	} else {
		v -= 0.5
	}
	switch {
	case v > 32767:
		return 32767
	case v < -32768:
		return -32768
	default:
		return int16(v)
	}
}
