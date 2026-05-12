// Package audio defines the unit of audio that crosses voice pipeline stages.
//
// A [Frame] is a fixed-duration window of single-channel signed-16-bit PCM
// samples whose sample count is structurally tied to its sample rate and
// duration: constructing a Frame validates that len(samples) ==
// SampleRate × FrameMs / 1000. Stages downstream (VAD, STT, …) accept Frame
// rather than raw []byte so the format contract lives in the type system, not
// in prose.
package audio

import (
	"encoding/binary"
	"fmt"
)

// Frame is a single window of single-channel signed-16-bit PCM samples.
//
// Frames are immutable from the caller's perspective: the slice returned by
// [Frame.Samples] must not be mutated. Construct frames with [NewFrame] or
// [FromPCM16LE]; the zero value is not a valid frame.
type Frame struct {
	samples    []int16
	sampleRate int
	frameMs    int
}

// NewFrame wraps an existing []int16 slice as a Frame, validating that the
// sample count matches sampleRate × frameMs / 1000. The Frame retains the
// slice; callers must not mutate it afterwards.
func NewFrame(samples []int16, sampleRate, frameMs int) (Frame, error) {
	if sampleRate <= 0 {
		return Frame{}, fmt.Errorf("audio: SampleRate must be > 0, got %d", sampleRate)
	}
	if frameMs <= 0 {
		return Frame{}, fmt.Errorf("audio: FrameMs must be > 0, got %d", frameMs)
	}
	want := sampleRate * frameMs / 1000
	if len(samples) != want {
		return Frame{}, fmt.Errorf(
			"audio: %d samples for SampleRate=%d FrameMs=%d (expected %d)",
			len(samples), sampleRate, frameMs, want,
		)
	}
	return Frame{samples: samples, sampleRate: sampleRate, frameMs: frameMs}, nil
}

// FromPCM16LE decodes little-endian signed-16-bit PCM bytes into a Frame.
// Returns an error if len(pcm) is odd or if the decoded sample count does
// not match sampleRate × frameMs / 1000.
func FromPCM16LE(pcm []byte, sampleRate, frameMs int) (Frame, error) {
	if len(pcm)%2 != 0 {
		return Frame{}, fmt.Errorf("audio: PCM byte length %d is not a multiple of 2", len(pcm))
	}
	samples := make([]int16, len(pcm)/2)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(pcm[i*2:]))
	}
	return NewFrame(samples, sampleRate, frameMs)
}

// Samples returns the underlying PCM samples. The returned slice aliases the
// Frame's storage; callers must not mutate it.
func (f Frame) Samples() []int16 { return f.samples }

// SampleRate returns the audio sample rate in Hz.
func (f Frame) SampleRate() int { return f.sampleRate }

// FrameMs returns the frame duration in milliseconds.
func (f Frame) FrameMs() int { return f.frameMs }
