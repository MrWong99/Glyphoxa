//go:build opus

package mixdown

import (
	"math"
	"testing"
	"time"

	"github.com/hraban/opus"
)

// encodeOpusFrames encodes a continuous PCM stream into 20ms (960-sample) Opus
// frames at 48 kHz mono, the shape the rollover tape captures off the wire.
func encodeOpusFrames(t *testing.T, samples []int16) [][]byte {
	t.Helper()
	enc, err := opus.NewEncoder(decodeRate, 1, opus.AppVoIP)
	if err != nil {
		t.Fatalf("new encoder: %v", err)
	}
	var frames [][]byte
	for i := 0; i+frameSamples <= len(samples); i += frameSamples {
		buf := make([]byte, 4000)
		n, err := enc.Encode(samples[i:i+frameSamples], buf)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		frames = append(frames, buf[:n])
	}
	return frames
}

// Under -tags opus the built-in libopus decoder round-trips real Opus: a tone
// encoded to Opus, dropped onto a lane, and mixed with the DEFAULT (nil)
// decoder factory comes back as recognizable audio (non-trivial energy),
// exercising the fresh-decoder-per-lane path against the real codec.
func TestWAVClip_RealOpusRoundTrip(t *testing.T) {
	const n = frameSamples * 20 // 400ms
	tone := sine(n, 440, 12000)
	opusFrames := encodeOpusFrames(t, tone)

	base := time.Unix(9000, 0)
	start := base.Add(100 * time.Millisecond)
	var frames []Frame
	for i, of := range opusFrames {
		frames = append(frames, Frame{Opus: of, At: start.Add(time.Duration(i) * 20 * time.Millisecond)})
	}
	snap := Snapshot{From: base, To: base.Add(time.Second),
		Lanes: []LaneSnapshot{{LaneID: "spk", Frames: frames}}}

	clip, err := WAVClip(snap, Options{}) // nil Decoder → real libopus default
	if err != nil {
		t.Fatalf("WAVClip: %v", err)
	}
	got := samplesOf(t, clip)

	// The decoded tone should carry real energy in the run region [100ms, 500ms).
	var sumSq float64
	region := got[4800 : 4800+n]
	for _, s := range region {
		sumSq += float64(s) * float64(s)
	}
	rms := math.Sqrt(sumSq / float64(len(region)))
	if rms < 1000 {
		t.Fatalf("decoded RMS = %.0f, want a substantial tone (>1000)", rms)
	}
	// Silence outside the run.
	if !allZero(got[:4700]) {
		t.Fatal("audio leaked before the run start")
	}
}
