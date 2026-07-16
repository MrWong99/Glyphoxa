package silero_test

// Golden equivalence gate for the pure-Go Silero forward pass (#468).
//
// The goldens in testdata/golden/ hold per-frame speech probabilities computed
// with ONNX Runtime (see scripts/gen-silero-golden.py): the 16 kHz files
// against the pre-#468 production model (opset-16 silero_vad.onnx), the 8 kHz
// files against the embedded ifless model's 8k branch. Replaying the same
// clips through the pure-Go engine and asserting |Δ| < 1e-4 per frame
// therefore gates BOTH the model swap and the engine swap at once.
//
// This test is the acceptance gate for any future inference-engine or model
// change: regenerate the goldens only when the model itself is intentionally
// updated, never to paper over an engine regression.

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/vad"
	"github.com/MrWong99/Glyphoxa/pkg/voice/vad/silero"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicetest"
)

// goldenTolerance is the maximum per-frame probability delta accepted against
// the ONNX Runtime reference (issue #468 acceptance criterion). Observed
// deltas are ~1e-6; the two orders of magnitude of headroom absorb benign
// float32 ordering differences without letting real regressions through.
const goldenTolerance = 1e-4

// goldenFile mirrors the JSON schema written by scripts/gen-silero-golden.py.
type goldenFile struct {
	Clip         string    `json:"clip"`
	SampleRate   int       `json:"sample_rate"`
	ChunkSamples int       `json:"chunk_samples"`
	Reference    string    `json:"reference"`
	Probs        []float64 `json:"probs"`
}

func TestGoldenEquivalence(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob(filepath.Join("testdata", "golden", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no golden files found (err=%v); regenerate with scripts/gen-silero-golden.py", err)
	}

	eng, err := silero.New()
	if err != nil {
		t.Fatalf("silero.New: %v", err)
	}

	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			t.Parallel()

			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatalf("read golden: %v", err)
			}
			var g goldenFile
			if err := json.Unmarshal(raw, &g); err != nil {
				t.Fatalf("parse golden: %v", err)
			}

			probs := runClip(t, eng, g.Clip, g.SampleRate, g.ChunkSamples)
			if len(probs) != len(g.Probs) {
				t.Fatalf("frame count mismatch: engine produced %d, golden has %d", len(probs), len(g.Probs))
			}

			var worst float64
			var worstFrame int
			for i := range probs {
				if d := math.Abs(probs[i] - g.Probs[i]); d > worst {
					worst, worstFrame = d, i
				}
			}
			t.Logf("%s: %d frames, max |Δ| = %.2e vs %s", g.Clip, len(probs), worst, g.Reference)
			if worst > goldenTolerance {
				t.Errorf("frame %d: |Δ| = %.3e exceeds tolerance %.0e (got %.6f, golden %.6f)",
					worstFrame, worst, goldenTolerance, probs[worstFrame], g.Probs[worstFrame])
			}
		})
	}
}

// TestPerFrameLatencyBudget asserts the pure-Go forward pass stays within the
// real-time budget from #468: < 5 ms p95 per 32 ms frame on CI runners. The
// engine typically needs well under 1 ms, so a pass leaves a wide margin for
// noisy shared runners while still catching order-of-magnitude regressions.
// Under the race detector the timing is instrumentation-dominated (~10× slow),
// so the budget scales ×10 there — still tight enough to catch a real
// order-of-magnitude regression while keeping the gate active in the -race CI
// suite.
func TestPerFrameLatencyBudget(t *testing.T) {
	eng, err := silero.New()
	if err != nil {
		t.Fatalf("silero.New: %v", err)
	}
	sess, err := eng.NewSession(vad.Config{
		SampleRate: 16000, FrameSizeMs: 32, SpeechThreshold: 0.5, SilenceThreshold: 0.35,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	clip := voicetest.LoadClip(t, "two-utterance-test")
	frames, _ := clip.FramesOf(t, 512)

	durations := make([]time.Duration, 0, len(frames))
	for _, f := range frames {
		start := time.Now()
		if _, err := sess.ProcessFrame(f); err != nil {
			t.Fatalf("ProcessFrame: %v", err)
		}
		durations = append(durations, time.Since(start))
	}

	budget := 5 * time.Millisecond
	if raceEnabled {
		budget *= 10
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p95 := durations[len(durations)*95/100]
	t.Logf("per-frame latency over %d frames: p50=%v p95=%v max=%v (budget %v, race=%v)",
		len(durations), durations[len(durations)/2], p95, durations[len(durations)-1], budget, raceEnabled)
	if p95 > budget {
		t.Errorf("p95 per-frame latency %v exceeds the %v budget", p95, budget)
	}
}

// runClip replays a corpus clip through a fresh engine session and returns
// the per-frame speech probabilities. For 8 kHz goldens the 16 kHz corpus
// clip is decimated 2:1, matching scripts/gen-silero-golden.py.
func runClip(t *testing.T, eng *silero.Engine, clipName string, sampleRate, chunkSamples int) []float64 {
	t.Helper()

	clip := voicetest.LoadClip(t, clipName)
	pcm := clip.PCM
	if sampleRate == 8000 {
		pcm = decimatePCM16(pcm)
	} else if sampleRate != clip.SampleRate {
		t.Fatalf("clip %q is %d Hz, golden wants %d Hz", clipName, clip.SampleRate, sampleRate)
	}

	sess, err := eng.NewSession(vad.Config{
		SampleRate:       sampleRate,
		FrameSizeMs:      chunkSamples * 1000 / sampleRate,
		SpeechThreshold:  0.5,
		SilenceThreshold: 0.35,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	frameMs := chunkSamples * 1000 / sampleRate
	bytesPerFrame := chunkSamples * 2
	n := len(pcm) / bytesPerFrame
	probs := make([]float64, 0, n)
	for i := range n {
		frame, err := audio.FromPCM16LE(pcm[i*bytesPerFrame:(i+1)*bytesPerFrame], sampleRate, frameMs)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		evt, err := sess.ProcessFrame(frame)
		if err != nil {
			t.Fatalf("frame %d: ProcessFrame: %v", i, err)
		}
		probs = append(probs, evt.Probability)
	}
	return probs
}

// decimatePCM16 naively downsamples 16 kHz s16le PCM to 8 kHz by keeping
// every other sample (sileroGoldenPCM8k contract with the generator script;
// aliasing is irrelevant for a numerical equivalence gate).
func decimatePCM16(pcm []byte) []byte {
	samples := len(pcm) / 2
	out := make([]byte, 0, samples)
	for i := 0; i < samples; i += 2 {
		var b [2]byte
		binary.LittleEndian.PutUint16(b[:], binary.LittleEndian.Uint16(pcm[i*2:]))
		out = append(out, b[:]...)
	}
	return out
}
