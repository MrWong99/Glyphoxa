package dsp

// Reframer accumulates a mono int16 PCM stream and emits fixed-size frames,
// carrying any leftover samples to the next call. Both directions of the codec
// need it: inbound, one 48→16 kHz-decoded Opus packet is 320 samples but the
// orchestrator wants 512-sample (32 ms) frames, so packets must be regrouped;
// outbound, resampled TTS chunks of arbitrary length must be cut into 960-sample
// (20 ms @ 48 kHz) Opus encoder frames.
//
// It is stateful and single-goroutine, like [Resampler]: leftover samples from
// one push belong to the same logical stream as the next.
type Reframer struct {
	size int     // samples per emitted frame
	buf  []int16 // accumulated, not-yet-emitted samples
}

// NewReframer builds a Reframer emitting frames of size samples (> 0).
func NewReframer(size int) *Reframer {
	return &Reframer{size: size}
}

// Push appends in and returns every full frame now available, each exactly
// [Reframer] size samples. Leftover (< size) stays buffered for the next push.
// Returned frames have their own backing storage; callers may retain them.
func (r *Reframer) Push(in []int16) [][]int16 {
	r.buf = append(r.buf, in...)
	var frames [][]int16
	for len(r.buf) >= r.size {
		frame := make([]int16, r.size)
		copy(frame, r.buf[:r.size])
		frames = append(frames, frame)
		r.buf = r.buf[r.size:]
	}
	// Compact so the backing array does not grow unbounded as we reslice.
	if len(r.buf) == 0 {
		r.buf = r.buf[:0:0]
	}
	return frames
}

// Flush returns the final partial frame zero-padded to the frame size, or nil if
// nothing is buffered. Used at end-of-stream so a trailing TTS fragment is still
// spoken rather than dropped; inbound (VAD/STT) discards its tail instead.
func (r *Reframer) Flush() []int16 {
	if len(r.buf) == 0 {
		return nil
	}
	frame := make([]int16, r.size)
	copy(frame, r.buf)
	r.buf = r.buf[:0:0]
	return frame
}

// Buffered reports how many samples are held but not yet emitted.
func (r *Reframer) Buffered() int { return len(r.buf) }
