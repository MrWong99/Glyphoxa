package energy

import (
	"encoding/binary"
	"sync"
	"testing"

	"github.com/MrWong99/glyphoxa/pkg/provider/vad"
)

// generatePCMFrame creates a frame of int16 PCM samples with the given amplitude.
func generatePCMFrame(sampleRate, frameSizeMs int, amplitude int16) []byte {
	numSamples := sampleRate * frameSizeMs / 1000
	buf := make([]byte, numSamples*2)
	for i := range numSamples {
		binary.LittleEndian.PutUint16(buf[i*2:], uint16(amplitude))
	}
	return buf
}

// validCfg returns a Config that passes NewSession validation.
func validCfg() vad.Config {
	return vad.Config{
		SampleRate:       16000,
		FrameSizeMs:      20,
		SpeechThreshold:  0.5,
		SilenceThreshold: 0.3,
	}
}

func TestNew_Defaults(t *testing.T) {
	t.Parallel()
	eng := New()
	if eng.minSpeechFrames != defaultMinSpeechFrames {
		t.Errorf("minSpeechFrames = %d, want %d", eng.minSpeechFrames, defaultMinSpeechFrames)
	}
	if eng.minSilenceFrames != defaultMinSilenceFrames {
		t.Errorf("minSilenceFrames = %d, want %d", eng.minSilenceFrames, defaultMinSilenceFrames)
	}
	if eng.minSpeechDurationFrames != defaultMinSpeechDurationFrames {
		t.Errorf("minSpeechDurationFrames = %d, want %d", eng.minSpeechDurationFrames, defaultMinSpeechDurationFrames)
	}
	if eng.smoothingFactor != defaultSmoothingFactor {
		t.Errorf("smoothingFactor = %g, want %g", eng.smoothingFactor, defaultSmoothingFactor)
	}
}

func TestNewSession_ValidConfig(t *testing.T) {
	t.Parallel()
	eng := New()
	sess, err := eng.NewSession(validCfg())
	if err != nil {
		t.Fatalf("NewSession returned unexpected error: %v", err)
	}
	if sess == nil {
		t.Fatal("NewSession returned nil session")
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestNewSession_InvalidConfig(t *testing.T) {
	t.Parallel()
	eng := New()

	cases := []struct {
		name string
		cfg  vad.Config
	}{
		{
			name: "zero sample rate",
			cfg:  vad.Config{SampleRate: 0, FrameSizeMs: 20, SpeechThreshold: 0.5, SilenceThreshold: 0.3},
		},
		{
			name: "negative sample rate",
			cfg:  vad.Config{SampleRate: -1, FrameSizeMs: 20, SpeechThreshold: 0.5, SilenceThreshold: 0.3},
		},
		{
			name: "zero frame size",
			cfg:  vad.Config{SampleRate: 16000, FrameSizeMs: 0, SpeechThreshold: 0.5, SilenceThreshold: 0.3},
		},
		{
			name: "negative frame size",
			cfg:  vad.Config{SampleRate: 16000, FrameSizeMs: -10, SpeechThreshold: 0.5, SilenceThreshold: 0.3},
		},
		{
			name: "speech threshold zero",
			cfg:  vad.Config{SampleRate: 16000, FrameSizeMs: 20, SpeechThreshold: 0.0, SilenceThreshold: 0.0},
		},
		{
			name: "speech threshold above 1",
			cfg:  vad.Config{SampleRate: 16000, FrameSizeMs: 20, SpeechThreshold: 1.1, SilenceThreshold: 0.5},
		},
		{
			name: "silence threshold negative",
			cfg:  vad.Config{SampleRate: 16000, FrameSizeMs: 20, SpeechThreshold: 0.5, SilenceThreshold: -0.1},
		},
		{
			name: "silence threshold equals speech threshold",
			cfg:  vad.Config{SampleRate: 16000, FrameSizeMs: 20, SpeechThreshold: 0.5, SilenceThreshold: 0.5},
		},
		{
			name: "silence threshold above speech threshold",
			cfg:  vad.Config{SampleRate: 16000, FrameSizeMs: 20, SpeechThreshold: 0.5, SilenceThreshold: 0.6},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := eng.NewSession(tc.cfg)
			if err == nil {
				t.Errorf("expected error for case %q, got nil", tc.name)
			}
		})
	}
}

func TestProcessFrame_WrongSize(t *testing.T) {
	t.Parallel()
	sess, err := New().NewSession(validCfg())
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	_, err = sess.ProcessFrame([]byte{0x00, 0x01, 0x02}) // 3 bytes — not a valid frame
	if err == nil {
		t.Error("expected error for wrong frame size, got nil")
	}
}

func TestProcessFrame_Silence(t *testing.T) {
	t.Parallel()
	sess, err := New().NewSession(validCfg())
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	silentFrame := generatePCMFrame(16000, 20, 0)

	for i := range 20 {
		ev, err := sess.ProcessFrame(silentFrame)
		if err != nil {
			t.Fatalf("ProcessFrame[%d]: %v", i, err)
		}
		if ev.Type != vad.VADSilence {
			t.Errorf("frame %d: got event type %v, want VADSilence", i, ev.Type)
		}
	}
}

func TestProcessFrame_SpeechDetection(t *testing.T) {
	t.Parallel()
	const (
		sampleRate  = 16000
		frameSizeMs = 20
		minSpeech   = 3
	)
	// WithSmoothingFactor(0) disables history so energy responds instantly,
	// making frame counts deterministic.
	eng := New(
		WithMinSpeechFrames(minSpeech),
		WithSmoothingFactor(0.0),
	)
	sess, err := eng.NewSession(vad.Config{
		SampleRate:       sampleRate,
		FrameSizeMs:      frameSizeMs,
		SpeechThreshold:  0.5,
		SilenceThreshold: 0.3,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	loudFrame := generatePCMFrame(sampleRate, frameSizeMs, 32767)

	// The first (minSpeech-1) loud frames increment the counter but have not
	// yet reached minSpeechFrames, so they must still emit VADSilence.
	for i := range minSpeech - 1 {
		ev, err := sess.ProcessFrame(loudFrame)
		if err != nil {
			t.Fatalf("ProcessFrame[%d]: %v", i, err)
		}
		if ev.Type != vad.VADSilence {
			t.Errorf("frame %d: got %v, want VADSilence", i, ev.Type)
		}
	}

	// The minSpeech-th loud frame crosses the threshold → VADSpeechStart.
	ev, err := sess.ProcessFrame(loudFrame)
	if err != nil {
		t.Fatalf("ProcessFrame[SpeechStart]: %v", err)
	}
	if ev.Type != vad.VADSpeechStart {
		t.Errorf("SpeechStart frame: got %v, want VADSpeechStart", ev.Type)
	}
	if ev.Probability <= 0 || ev.Probability > 1 {
		t.Errorf("SpeechStart probability %g out of (0, 1]", ev.Probability)
	}

	// Subsequent loud frames emit VADSpeechContinue.
	for i := range 5 {
		ev, err := sess.ProcessFrame(loudFrame)
		if err != nil {
			t.Fatalf("ProcessFrame[SpeechContinue %d]: %v", i, err)
		}
		if ev.Type != vad.VADSpeechContinue {
			t.Errorf("SpeechContinue[%d]: got %v, want VADSpeechContinue", i, ev.Type)
		}
	}
}

func TestProcessFrame_SpeechEnd(t *testing.T) {
	t.Parallel()
	const (
		sampleRate  = 16000
		frameSizeMs = 20
		minSpeech   = 2
		minSilence  = 3
	)
	eng := New(
		WithMinSpeechFrames(minSpeech),
		WithMinSilenceFrames(minSilence),
		// Disable the minimum speech duration guard so this test exercises
		// only the silence-frame counter in isolation.
		WithMinSpeechDurationFrames(1),
		WithSmoothingFactor(0.0),
	)
	sess, err := eng.NewSession(vad.Config{
		SampleRate:       sampleRate,
		FrameSizeMs:      frameSizeMs,
		SpeechThreshold:  0.5,
		SilenceThreshold: 0.3,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	loudFrame := generatePCMFrame(sampleRate, frameSizeMs, 32767)
	silentFrame := generatePCMFrame(sampleRate, frameSizeMs, 0)

	// Drive the session into the speaking state.
	for i := range minSpeech {
		if _, err := sess.ProcessFrame(loudFrame); err != nil {
			t.Fatalf("setup loud frame %d: %v", i, err)
		}
	}

	// The first (minSilence-1) silent frames keep us in speaking → SpeechContinue.
	for i := range minSilence - 1 {
		ev, err := sess.ProcessFrame(silentFrame)
		if err != nil {
			t.Fatalf("silent frame %d: %v", i, err)
		}
		if ev.Type != vad.VADSpeechContinue {
			t.Errorf("silent frame %d: got %v, want VADSpeechContinue", i, ev.Type)
		}
	}

	// The minSilence-th silent frame triggers VADSpeechEnd.
	ev, err := sess.ProcessFrame(silentFrame)
	if err != nil {
		t.Fatalf("final silent frame: %v", err)
	}
	if ev.Type != vad.VADSpeechEnd {
		t.Errorf("got %v, want VADSpeechEnd", ev.Type)
	}
}

func TestProcessFrame_FullCycle(t *testing.T) {
	t.Parallel()
	const (
		sampleRate  = 16000
		frameSizeMs = 20
		minSpeech   = 2
		minSilence  = 2
	)
	eng := New(
		WithMinSpeechFrames(minSpeech),
		WithMinSilenceFrames(minSilence),
		// Disable the minimum speech duration guard so this test exercises
		// only the full silence/speech cycle in isolation.
		WithMinSpeechDurationFrames(1),
		WithSmoothingFactor(0.0),
	)
	sess, err := eng.NewSession(vad.Config{
		SampleRate:       sampleRate,
		FrameSizeMs:      frameSizeMs,
		SpeechThreshold:  0.5,
		SilenceThreshold: 0.3,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	loudFrame := generatePCMFrame(sampleRate, frameSizeMs, 32767)
	silentFrame := generatePCMFrame(sampleRate, frameSizeMs, 0)

	mustProcess := func(frame []byte, label string) vad.VADEvent {
		ev, err := sess.ProcessFrame(frame)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		return ev
	}

	// Phase 1: initial silence — all frames must emit VADSilence.
	for i := range 3 {
		ev := mustProcess(silentFrame, "phase1")
		if ev.Type != vad.VADSilence {
			t.Errorf("phase1[%d]: got %v, want VADSilence", i, ev.Type)
		}
	}

	// Phase 2a: speech onset — first (minSpeech-1) loud frames still Silence.
	for i := range minSpeech - 1 {
		ev := mustProcess(loudFrame, "phase2a")
		if ev.Type != vad.VADSilence {
			t.Errorf("phase2a[%d]: got %v, want VADSilence", i, ev.Type)
		}
	}

	// Phase 2b: minSpeech-th loud frame → SpeechStart.
	ev := mustProcess(loudFrame, "phase2b SpeechStart")
	if ev.Type != vad.VADSpeechStart {
		t.Errorf("phase2b: got %v, want VADSpeechStart", ev.Type)
	}

	// Phase 3: ongoing speech → SpeechContinue.
	for i := range 3 {
		ev := mustProcess(loudFrame, "phase3")
		if ev.Type != vad.VADSpeechContinue {
			t.Errorf("phase3[%d]: got %v, want VADSpeechContinue", i, ev.Type)
		}
	}

	// Phase 4a: trailing silence — first (minSilence-1) frames still SpeechContinue.
	for i := range minSilence - 1 {
		ev := mustProcess(silentFrame, "phase4a")
		if ev.Type != vad.VADSpeechContinue {
			t.Errorf("phase4a[%d]: got %v, want VADSpeechContinue", i, ev.Type)
		}
	}

	// Phase 4b: minSilence-th silent frame → SpeechEnd.
	ev = mustProcess(silentFrame, "phase4b SpeechEnd")
	if ev.Type != vad.VADSpeechEnd {
		t.Errorf("phase4b: got %v, want VADSpeechEnd", ev.Type)
	}

	// Phase 5: back to silence.
	ev = mustProcess(silentFrame, "phase5")
	if ev.Type != vad.VADSilence {
		t.Errorf("phase5: got %v, want VADSilence", ev.Type)
	}
}

func TestReset(t *testing.T) {
	t.Parallel()
	const (
		sampleRate  = 16000
		frameSizeMs = 20
		minSpeech   = 2
	)
	eng := New(
		WithMinSpeechFrames(minSpeech),
		WithSmoothingFactor(0.0),
	)
	sess, err := eng.NewSession(vad.Config{
		SampleRate:       sampleRate,
		FrameSizeMs:      frameSizeMs,
		SpeechThreshold:  0.5,
		SilenceThreshold: 0.3,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	loudFrame := generatePCMFrame(sampleRate, frameSizeMs, 32767)

	// Accumulate (minSpeech-1) frames so the internal counter is non-zero.
	for i := range minSpeech - 1 {
		if _, err := sess.ProcessFrame(loudFrame); err != nil {
			t.Fatalf("pre-reset frame %d: %v", i, err)
		}
	}

	// Reset must clear the counter.
	sess.Reset()

	// After reset we must again need minSpeech frames before SpeechStart.
	for i := range minSpeech - 1 {
		ev, err := sess.ProcessFrame(loudFrame)
		if err != nil {
			t.Fatalf("post-reset frame %d: %v", i, err)
		}
		if ev.Type != vad.VADSilence {
			t.Errorf("post-reset frame %d: got %v, want VADSilence", i, ev.Type)
		}
	}
	ev, err := sess.ProcessFrame(loudFrame)
	if err != nil {
		t.Fatalf("post-reset SpeechStart frame: %v", err)
	}
	if ev.Type != vad.VADSpeechStart {
		t.Errorf("post-reset: got %v, want VADSpeechStart", ev.Type)
	}
}

func TestClose(t *testing.T) {
	t.Parallel()
	sess, err := New().NewSession(validCfg())
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// First Close must succeed.
	if err := sess.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	// ProcessFrame after Close must return an error.
	frame := generatePCMFrame(16000, 20, 0)
	if _, err := sess.ProcessFrame(frame); err == nil {
		t.Error("ProcessFrame after Close: expected error, got nil")
	}

	// Second Close must also succeed (no panic, nil error).
	if err := sess.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestSession_ConcurrentClose(t *testing.T) {
	t.Parallel()
	sess, err := New().NewSession(validCfg())
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			_ = sess.Close()
		}()
	}
	wg.Wait()
}

// TestProcessFrame_MinSpeechDuration verifies that SpeechEnd is not emitted
// before minSpeechDurationFrames have elapsed since SpeechStart, even when
// minSilenceFrames consecutive silent frames arrive immediately after onset.
func TestProcessFrame_MinSpeechDuration(t *testing.T) {
	t.Parallel()
	const (
		sampleRate        = 16000
		frameSizeMs       = 20
		minSpeech         = 3
		minSilence        = 2
		minSpeechDuration = 5
	)
	eng := New(
		WithMinSpeechFrames(minSpeech),
		WithMinSilenceFrames(minSilence),
		WithMinSpeechDurationFrames(minSpeechDuration),
		WithSmoothingFactor(0.0),
	)
	sess, err := eng.NewSession(vad.Config{
		SampleRate:       sampleRate,
		FrameSizeMs:      frameSizeMs,
		SpeechThreshold:  0.5,
		SilenceThreshold: 0.3,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	loudFrame := generatePCMFrame(sampleRate, frameSizeMs, 32767)
	silentFrame := generatePCMFrame(sampleRate, frameSizeMs, 0)

	// Drive into speaking state.
	for i := range minSpeech {
		if _, err := sess.ProcessFrame(loudFrame); err != nil {
			t.Fatalf("setup loud frame %d: %v", i, err)
		}
	}

	// Send minSilence silent frames immediately after SpeechStart. SpeechEnd
	// must NOT fire because speechDurationCount < minSpeechDuration.
	for i := range minSilence {
		ev, err := sess.ProcessFrame(silentFrame)
		if err != nil {
			t.Fatalf("early silent frame %d: %v", i, err)
		}
		if ev.Type == vad.VADSpeechEnd {
			t.Errorf("early silent frame %d: got VADSpeechEnd before minSpeechDurationFrames (%d) elapsed", i, minSpeechDuration)
		}
	}

	// Continue silent frames until we reach (but not exceed) the protection
	// window. None of these should trigger SpeechEnd either.
	for i := minSilence; i < minSpeechDuration-1; i++ {
		ev, err := sess.ProcessFrame(silentFrame)
		if err != nil {
			t.Fatalf("protection-window silent frame %d: %v", i, err)
		}
		if ev.Type == vad.VADSpeechEnd {
			t.Errorf("protection-window frame %d: unexpected VADSpeechEnd before minSpeechDurationFrames (%d) elapsed", i, minSpeechDuration)
		}
	}

	// Now that the protection window has been reached, minSilence more silent
	// frames should drive us to SpeechEnd.
	for i := range minSilence {
		ev, err := sess.ProcessFrame(silentFrame)
		if err != nil {
			t.Fatalf("post-protection silent frame %d: %v", i, err)
		}
		if i == minSilence-1 {
			if ev.Type != vad.VADSpeechEnd {
				t.Errorf("post-protection frame %d: got %v, want VADSpeechEnd", i, ev.Type)
			}
		} else {
			if ev.Type == vad.VADSpeechEnd {
				t.Errorf("post-protection frame %d: premature VADSpeechEnd", i)
			}
		}
	}
}

// TestProcessFrame_TransientNoise verifies that fewer than minSpeechFrames
// consecutive loud frames do not emit VADSpeechStart, and that subsequent
// silence frames emit only VADSilence (not any speech event).
func TestProcessFrame_TransientNoise(t *testing.T) {
	t.Parallel()
	const (
		sampleRate  = 16000
		frameSizeMs = 20
		minSpeech   = 3
	)
	eng := New(
		WithMinSpeechFrames(minSpeech),
		WithSmoothingFactor(0.0),
	)
	sess, err := eng.NewSession(vad.Config{
		SampleRate:       sampleRate,
		FrameSizeMs:      frameSizeMs,
		SpeechThreshold:  0.5,
		SilenceThreshold: 0.3,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	loudFrame := generatePCMFrame(sampleRate, frameSizeMs, 32767)
	silentFrame := generatePCMFrame(sampleRate, frameSizeMs, 0)

	// Send fewer than minSpeech loud frames (simulating a transient noise burst).
	for i := range minSpeech - 1 {
		ev, err := sess.ProcessFrame(loudFrame)
		if err != nil {
			t.Fatalf("transient frame %d: %v", i, err)
		}
		if ev.Type == vad.VADSpeechStart {
			t.Errorf("transient frame %d: unexpected VADSpeechStart", i)
		}
	}

	// Subsequent silence must not produce any speech events — the counter
	// should have been reset when energy dropped below SpeechThreshold.
	for i := range 10 {
		ev, err := sess.ProcessFrame(silentFrame)
		if err != nil {
			t.Fatalf("post-transient silent frame %d: %v", i, err)
		}
		if ev.Type != vad.VADSilence {
			t.Errorf("post-transient frame %d: got %v, want VADSilence", i, ev.Type)
		}
	}
}
