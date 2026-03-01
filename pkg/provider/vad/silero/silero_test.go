//go:build onnxruntime

package silero

import (
	"encoding/binary"
	"sync"
	"testing"

	"github.com/MrWong99/glyphoxa/pkg/provider/vad"
)

// ─── Mock inferencer ─────────────────────────────────────────────────────────

// inferCall records a single call to mockInferencer.infer.
type inferCall struct {
	samples []float32
	sr      int64
	h       []float32
	c       []float32
}

// mockInferencer implements inferencer for unit tests. It returns a fixed
// probability and identifiable LSTM state so tests can verify state passthrough.
type mockInferencer struct {
	mu sync.Mutex

	// prob is the speech probability returned on every infer call.
	prob float32

	// hnVal and cnVal are the element values used to fill the returned hn/cn
	// slices. Using distinct values lets tests verify LSTM state passthrough.
	hnVal float32
	cnVal float32

	// calls records every infer invocation in order.
	calls []inferCall

	closed   bool
	closeErr error
}

func (m *mockInferencer) infer(samples []float32, sr int64, h, c []float32) (float32, []float32, []float32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Record a deep copy so callers can compare per-call state independently.
	call := inferCall{
		samples: make([]float32, len(samples)),
		sr:      sr,
		h:       make([]float32, len(h)),
		c:       make([]float32, len(c)),
	}
	copy(call.samples, samples)
	copy(call.h, h)
	copy(call.c, c)
	m.calls = append(m.calls, call)

	// Return fresh hn/cn slices filled with the configured values.
	hn := make([]float32, lstmStateSize)
	cn := make([]float32, lstmStateSize)
	for i := range hn {
		hn[i] = m.hnVal
		cn[i] = m.cnVal
	}
	return m.prob, hn, cn, nil
}

func (m *mockInferencer) close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return m.closeErr
}

func (m *mockInferencer) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *mockInferencer) callAt(i int) inferCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls[i]
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// validConfig returns a valid Config for 16 kHz / 30 ms frames.
func validConfig() vad.Config {
	return vad.Config{
		SampleRate:       16000,
		FrameSizeMs:      30,
		SpeechThreshold:  0.5,
		SilenceThreshold: 0.35,
	}
}

// silenceFrame returns a zero-filled PCM frame for the given config.
func silenceFrame(cfg vad.Config) []byte {
	chunkSize := cfg.SampleRate * cfg.FrameSizeMs / 1000
	return make([]byte, chunkSize*2)
}

// makeSession builds a session with the provided mock (bypassing Engine so no
// ONNX Runtime is needed).
func makeSession(t *testing.T, cfg vad.Config, m *mockInferencer, minSpeech, minSilence int) *session {
	t.Helper()
	if err := validateConfig(cfg); err != nil {
		t.Fatalf("validateConfig: %v", err)
	}
	return newSession(cfg, m, minSpeech, minSilence)
}

// int16LEFrame builds a PCM frame where every sample equals val (int16, LE).
func int16LEFrame(cfg vad.Config, val int16) []byte {
	chunkSize := cfg.SampleRate * cfg.FrameSizeMs / 1000
	frame := make([]byte, chunkSize*2)
	for i := 0; i < chunkSize; i++ {
		binary.LittleEndian.PutUint16(frame[i*2:], uint16(val))
	}
	return frame
}

// ─── Tests ───────────────────────────────────────────────────────────────────

func TestNewSession_ValidConfig(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		cfg  vad.Config
	}{
		{
			name: "16kHz_30ms",
			cfg:  validConfig(),
		},
		{
			name: "8kHz_20ms",
			cfg: vad.Config{
				SampleRate:       8000,
				FrameSizeMs:      20,
				SpeechThreshold:  0.6,
				SilenceThreshold: 0.4,
			},
		},
		{
			name: "equal_thresholds",
			cfg: vad.Config{
				SampleRate:       16000,
				FrameSizeMs:      10,
				SpeechThreshold:  0.5,
				SilenceThreshold: 0.5,
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m := &mockInferencer{}
			sess := makeSession(t, tc.cfg, m, 3, 15)

			if sess == nil {
				t.Fatal("expected non-nil session")
			}
			if err := sess.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}
		})
	}
}

func TestNewSession_InvalidSampleRate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		sampleRate int
		wantErr    bool
	}{
		{"valid_8000", 8000, false},
		{"valid_16000", 16000, false},
		{"invalid_44100", 44100, true},
		{"invalid_48000", 48000, true},
		{"invalid_22050", 22050, true},
		{"invalid_0", 0, true},
		{"invalid_negative", -1, true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := vad.Config{
				SampleRate:       tc.sampleRate,
				FrameSizeMs:      30,
				SpeechThreshold:  0.5,
				SilenceThreshold: 0.35,
			}
			err := validateConfig(cfg)
			if tc.wantErr && err == nil {
				t.Error("expected error for unsupported sample rate, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestProcessFrame_SilenceWithMock(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	m := &mockInferencer{prob: 0.1} // well below SpeechThreshold=0.5
	sess := makeSession(t, cfg, m, 3, 15)
	t.Cleanup(func() { _ = sess.Close() })

	frame := silenceFrame(cfg)
	for i := 0; i < 10; i++ {
		evt, err := sess.ProcessFrame(frame)
		if err != nil {
			t.Fatalf("frame %d: ProcessFrame: %v", i, err)
		}
		if evt.Type != vad.VADSilence {
			t.Errorf("frame %d: got %v, want VADSilence", i, evt.Type)
		}
		if evt.Probability != float64(m.prob) {
			t.Errorf("frame %d: probability = %v, want %v", i, evt.Probability, m.prob)
		}
	}
}

func TestProcessFrame_SpeechWithMock(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	m := &mockInferencer{prob: 0.9} // above SpeechThreshold=0.5
	const minSpeech = 3
	sess := makeSession(t, cfg, m, minSpeech, 15)
	t.Cleanup(func() { _ = sess.Close() })

	frame := silenceFrame(cfg)

	// Frames 0 and 1 should still be VADSilence (not enough consecutive speech).
	for i := 0; i < minSpeech-1; i++ {
		evt, err := sess.ProcessFrame(frame)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if evt.Type != vad.VADSilence {
			t.Errorf("frame %d: got %v, want VADSilence (not enough speech frames yet)", i, evt.Type)
		}
	}

	// Frame at minSpeech-1 threshold triggers SpeechStart.
	evt, err := sess.ProcessFrame(frame)
	if err != nil {
		t.Fatalf("speech-start frame: %v", err)
	}
	if evt.Type != vad.VADSpeechStart {
		t.Errorf("speech-start frame: got %v, want VADSpeechStart", evt.Type)
	}

	// Subsequent frames should be VADSpeechContinue.
	for i := 0; i < 5; i++ {
		evt, err := sess.ProcessFrame(frame)
		if err != nil {
			t.Fatalf("continue frame %d: %v", i, err)
		}
		if evt.Type != vad.VADSpeechContinue {
			t.Errorf("continue frame %d: got %v, want VADSpeechContinue", i, evt.Type)
		}
	}
}

func TestProcessFrame_StateTransitions(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	const minSpeech, minSilence = 2, 3

	type step struct {
		prob     float32
		wantType vad.VADEventType
	}

	steps := []step{
		// Silence: speech count builds up but hasn't reached minSpeech.
		{0.8, vad.VADSilence},
		// Speech starts on second consecutive high-prob frame.
		{0.8, vad.VADSpeechStart},
		// Ongoing speech.
		{0.8, vad.VADSpeechContinue},
		// Silence count builds up (below SilenceThreshold=0.35).
		{0.1, vad.VADSpeechContinue},
		{0.1, vad.VADSpeechContinue},
		// Speech ends on third consecutive low-prob frame.
		{0.1, vad.VADSpeechEnd},
		// Back to silence.
		{0.1, vad.VADSilence},
	}

	// Run sequentially (state machine is stateful).
	m := &mockInferencer{}
	sess := makeSession(t, cfg, m, minSpeech, minSilence)
	t.Cleanup(func() { _ = sess.Close() })

	frame := silenceFrame(cfg)
	for i, s := range steps {
		m.mu.Lock()
		m.prob = s.prob
		m.mu.Unlock()

		evt, err := sess.ProcessFrame(frame)
		if err != nil {
			t.Fatalf("step %d: ProcessFrame: %v", i, err)
		}
		if evt.Type != s.wantType {
			t.Errorf("step %d (prob=%.2f): got %v, want %v", i, s.prob, evt.Type, s.wantType)
		}
	}
}

func TestProcessFrame_LSTMStatePassthrough(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	// The mock returns hn filled with 0.42 and cn filled with 0.84.
	m := &mockInferencer{
		prob:  0.1, // below threshold — stays silent
		hnVal: 0.42,
		cnVal: 0.84,
	}
	sess := makeSession(t, cfg, m, 3, 15)
	t.Cleanup(func() { _ = sess.Close() })

	frame := silenceFrame(cfg)

	// First frame: h and c should be all zeros (initial state).
	if _, err := sess.ProcessFrame(frame); err != nil {
		t.Fatalf("frame 1: %v", err)
	}
	call0 := m.callAt(0)
	for i, v := range call0.h {
		if v != 0 {
			t.Errorf("frame 1: h[%d] = %v, want 0 (initial state)", i, v)
		}
	}
	for i, v := range call0.c {
		if v != 0 {
			t.Errorf("frame 1: c[%d] = %v, want 0 (initial state)", i, v)
		}
	}

	// Second frame: h and c must equal the hn/cn returned by the first call.
	if _, err := sess.ProcessFrame(frame); err != nil {
		t.Fatalf("frame 2: %v", err)
	}
	call1 := m.callAt(1)
	for i, v := range call1.h {
		if v != m.hnVal {
			t.Errorf("frame 2: h[%d] = %v, want %v (hn from frame 1)", i, v, m.hnVal)
		}
	}
	for i, v := range call1.c {
		if v != m.cnVal {
			t.Errorf("frame 2: c[%d] = %v, want %v (cn from frame 1)", i, v, m.cnVal)
		}
	}
}

func TestReset_ClearsState(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	const minSpeech = 2
	m := &mockInferencer{prob: 0.9, hnVal: 0.5, cnVal: 0.7}
	sess := makeSession(t, cfg, m, minSpeech, 15)
	t.Cleanup(func() { _ = sess.Close() })

	frame := silenceFrame(cfg)

	// Drive the session into speech state.
	for i := 0; i < minSpeech; i++ {
		if _, err := sess.ProcessFrame(frame); err != nil {
			t.Fatalf("setup frame %d: %v", i, err)
		}
	}
	// Verify we're speaking.
	if sess.state != stateSpeaking {
		t.Fatalf("expected stateSpeaking before reset, got %v", sess.state)
	}
	// LSTM state should be non-zero after frames.
	if sess.h[0] == 0 {
		t.Error("expected non-zero h before reset")
	}

	// Reset.
	sess.Reset()

	// State machine must be back to silence with zeroed counters.
	if sess.state != stateSilence {
		t.Errorf("state after Reset: got %v, want stateSilence", sess.state)
	}
	if sess.speechCount != 0 {
		t.Errorf("speechCount after Reset: %d, want 0", sess.speechCount)
	}
	if sess.silenceCount != 0 {
		t.Errorf("silenceCount after Reset: %d, want 0", sess.silenceCount)
	}

	// LSTM state must be zeroed.
	for i, v := range sess.h {
		if v != 0 {
			t.Errorf("h[%d] after Reset: %v, want 0", i, v)
		}
	}
	for i, v := range sess.c {
		if v != 0 {
			t.Errorf("c[%d] after Reset: %v, want 0", i, v)
		}
	}

	// The next frame after Reset should start counting again from scratch.
	m.prob = 0.9
	evt, err := sess.ProcessFrame(frame)
	if err != nil {
		t.Fatalf("post-reset frame: %v", err)
	}
	// One frame — not yet at minSpeech=2, so still silence.
	if evt.Type != vad.VADSilence {
		t.Errorf("first post-reset frame: got %v, want VADSilence", evt.Type)
	}
}

func TestClose_RejectsSubsequentCalls(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	m := &mockInferencer{prob: 0.1}
	sess := makeSession(t, cfg, m, 3, 15)

	// First Close must succeed.
	if err := sess.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if !m.closed {
		t.Error("expected mockInferencer.closed = true after Close")
	}

	// Second Close must be a no-op (returns nil).
	if err := sess.Close(); err != nil {
		t.Errorf("second Close: %v (expected nil)", err)
	}

	// ProcessFrame must return an error after Close.
	frame := silenceFrame(cfg)
	_, err := sess.ProcessFrame(frame)
	if err == nil {
		t.Error("ProcessFrame after Close: expected error, got nil")
	}
}

func TestPCMConversion(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		input   []int16 // values to encode as LE int16
		wantLen int
		// spot-check the first element
		wantFirst float32
	}{
		{
			name:      "zeros",
			input:     []int16{0, 0, 0},
			wantLen:   3,
			wantFirst: 0.0,
		},
		{
			name:      "max_positive",
			input:     []int16{32767},
			wantLen:   1,
			wantFirst: 32767.0 / 32768.0, // ~0.999969
		},
		{
			name:      "min_negative",
			input:     []int16{-32768},
			wantLen:   1,
			wantFirst: -1.0,
		},
		{
			name:      "mid_positive",
			input:     []int16{16384},
			wantLen:   1,
			wantFirst: 16384.0 / 32768.0, // 0.5
		},
		{
			name:      "mid_negative",
			input:     []int16{-16384},
			wantLen:   1,
			wantFirst: -16384.0 / 32768.0, // -0.5
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Encode int16 values as little-endian bytes.
			pcm := make([]byte, len(tc.input)*2)
			for i, v := range tc.input {
				binary.LittleEndian.PutUint16(pcm[i*2:], uint16(v))
			}

			got := pcmToFloat32(pcm)

			if len(got) != tc.wantLen {
				t.Fatalf("len = %d, want %d", len(got), tc.wantLen)
			}

			const eps = 1e-6
			diff := got[0] - tc.wantFirst
			if diff < -eps || diff > eps {
				t.Errorf("got[0] = %v, want %v (diff=%v)", got[0], tc.wantFirst, diff)
			}

			// All outputs must be in [-1.0, 1.0].
			for i, v := range got {
				if v < -1.0 || v > 1.0 {
					t.Errorf("got[%d] = %v out of range [-1.0, 1.0]", i, v)
				}
			}
		})
	}
}
