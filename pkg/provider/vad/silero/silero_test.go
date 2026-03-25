package silero

import (
	"encoding/binary"
	"fmt"
	"sync"
	"testing"

	"github.com/MrWong99/glyphoxa/pkg/provider/vad"
)

// ─── Mock inferencer ─────────────────────────────────────────────────────────

// inferCall records a single call to mockInferencer.infer.
type inferCall struct {
	samples []float32
	sr      int64
	state   []float32
}

// mockInferencer implements inferencer for unit tests. It returns a fixed
// probability and identifiable LSTM state so tests can verify state passthrough.
type mockInferencer struct {
	mu sync.Mutex

	// prob is the speech probability returned on every infer call.
	prob float32

	// stateVal is the element value used to fill the returned stateN slice.
	stateVal float32

	// calls records every infer invocation in order.
	calls []inferCall

	closed   bool
	closeErr error
}

func (m *mockInferencer) infer(samples []float32, sr int64, state []float32) (float32, []float32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Record a deep copy so callers can compare per-call state independently.
	call := inferCall{
		samples: make([]float32, len(samples)),
		sr:      sr,
		state:   make([]float32, len(state)),
	}
	copy(call.samples, samples)
	copy(call.state, state)
	m.calls = append(m.calls, call)

	// Return a fresh stateN slice filled with the configured value.
	stateN := make([]float32, stateSize)
	for i := range stateN {
		stateN[i] = m.stateVal
	}
	return m.prob, stateN, nil
}

func (m *mockInferencer) close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return m.closeErr
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
		FrameSizeMs:      32,
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

// ─── contextSize tests ───────────────────────────────────────────────────

func TestContextSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		sampleRate int
		want       int
	}{
		{"8kHz returns 32", 8000, 32},
		{"16kHz returns 64", 16000, 64},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := contextSize(tt.sampleRate)
			if got != tt.want {
				t.Errorf("contextSize(%d) = %d, want %d", tt.sampleRate, got, tt.want)
			}
		})
	}
}

// ─── validateConfig edge cases ──────────────────────────────────────────

func TestValidateConfig_SpeechThresholdOutOfRange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     vad.Config
		wantErr bool
	}{
		{
			name: "negative speech threshold",
			cfg: vad.Config{
				SampleRate:       16000,
				FrameSizeMs:      32,
				SpeechThreshold:  -0.1,
				SilenceThreshold: 0.0,
			},
			wantErr: true,
		},
		{
			name: "speech threshold above 1",
			cfg: vad.Config{
				SampleRate:       16000,
				FrameSizeMs:      32,
				SpeechThreshold:  1.1,
				SilenceThreshold: 0.5,
			},
			wantErr: true,
		},
		{
			name: "negative silence threshold",
			cfg: vad.Config{
				SampleRate:       16000,
				FrameSizeMs:      32,
				SpeechThreshold:  0.5,
				SilenceThreshold: -0.1,
			},
			wantErr: true,
		},
		{
			name: "silence threshold above 1",
			cfg: vad.Config{
				SampleRate:       16000,
				FrameSizeMs:      32,
				SpeechThreshold:  0.5,
				SilenceThreshold: 1.5,
			},
			wantErr: true,
		},
		{
			name: "silence > speech threshold",
			cfg: vad.Config{
				SampleRate:       16000,
				FrameSizeMs:      32,
				SpeechThreshold:  0.3,
				SilenceThreshold: 0.5,
			},
			wantErr: true,
		},
		{
			name: "valid equal thresholds",
			cfg: vad.Config{
				SampleRate:       16000,
				FrameSizeMs:      32,
				SpeechThreshold:  0.5,
				SilenceThreshold: 0.5,
			},
			wantErr: false,
		},
		{
			name: "zero frame size ms",
			cfg: vad.Config{
				SampleRate:       16000,
				FrameSizeMs:      0,
				SpeechThreshold:  0.5,
				SilenceThreshold: 0.3,
			},
			wantErr: true,
		},
		{
			name: "negative frame size ms",
			cfg: vad.Config{
				SampleRate:       16000,
				FrameSizeMs:      -10,
				SpeechThreshold:  0.5,
				SilenceThreshold: 0.3,
			},
			wantErr: true,
		},
		{
			name: "invalid chunk size for 16kHz",
			cfg: vad.Config{
				SampleRate:       16000,
				FrameSizeMs:      30, // 16000*30/1000 = 480, not in {512,1024,1536}
				SpeechThreshold:  0.5,
				SilenceThreshold: 0.3,
			},
			wantErr: true,
		},
		{
			name: "valid 8kHz 64ms",
			cfg: vad.Config{
				SampleRate:       8000,
				FrameSizeMs:      64, // 8000*64/1000 = 512, valid
				SpeechThreshold:  0.5,
				SilenceThreshold: 0.3,
			},
			wantErr: false,
		},
		{
			name: "valid 8kHz 96ms",
			cfg: vad.Config{
				SampleRate:       8000,
				FrameSizeMs:      96, // 8000*96/1000 = 768, valid
				SpeechThreshold:  0.5,
				SilenceThreshold: 0.3,
			},
			wantErr: false,
		},
		{
			name: "invalid chunk size for 8kHz",
			cfg: vad.Config{
				SampleRate:       8000,
				FrameSizeMs:      50, // 8000*50/1000 = 400, not in {256,512,768}
				SpeechThreshold:  0.5,
				SilenceThreshold: 0.3,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateConfig(tt.cfg)
			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// ─── ProcessFrame edge cases ────────────────────────────────────────────

func TestProcessFrame_WrongFrameSize(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	m := &mockInferencer{prob: 0.1}
	sess := makeSession(t, cfg, m, 3, 15)
	t.Cleanup(func() { _ = sess.Close() })

	// Send a frame that's too short.
	shortFrame := make([]byte, 10) // expected: chunkSize * 2
	_, err := sess.ProcessFrame(shortFrame)
	if err == nil {
		t.Fatal("expected error for wrong frame size, got nil")
	}
}

// ─── step edge cases ────────────────────────────────────────────────────

func TestProcessFrame_SpeechCountResetOnLowProb(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	const minSpeech = 3

	m := &mockInferencer{}
	sess := makeSession(t, cfg, m, minSpeech, 15)
	t.Cleanup(func() { _ = sess.Close() })

	frame := silenceFrame(cfg)

	// Two high-prob frames, then one low-prob. Should reset speech count.
	m.mu.Lock()
	m.prob = 0.9
	m.mu.Unlock()
	for range 2 {
		sess.ProcessFrame(frame) //nolint:errcheck
	}
	// Now drop below threshold — speech count should reset.
	m.mu.Lock()
	m.prob = 0.1
	m.mu.Unlock()
	evt, err := sess.ProcessFrame(frame)
	if err != nil {
		t.Fatalf("ProcessFrame: %v", err)
	}
	if evt.Type != vad.VADSilence {
		t.Errorf("expected VADSilence after count reset, got %v", evt.Type)
	}
	if sess.speechCount != 0 {
		t.Errorf("speechCount = %d, want 0 after reset", sess.speechCount)
	}
}

func TestProcessFrame_SilenceCountResetOnHighProb(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	const minSpeech, minSilence = 2, 5

	m := &mockInferencer{prob: 0.9}
	sess := makeSession(t, cfg, m, minSpeech, minSilence)
	t.Cleanup(func() { _ = sess.Close() })

	frame := silenceFrame(cfg)

	// Drive into speech state.
	for range minSpeech {
		sess.ProcessFrame(frame) //nolint:errcheck
	}
	if sess.state != stateSpeaking {
		t.Fatalf("expected stateSpeaking, got %v", sess.state)
	}

	// A few low-prob frames (not enough for speech end).
	m.mu.Lock()
	m.prob = 0.1
	m.mu.Unlock()
	for range minSilence - 1 {
		sess.ProcessFrame(frame) //nolint:errcheck
	}
	if sess.silenceCount != minSilence-1 {
		t.Fatalf("silenceCount = %d, want %d", sess.silenceCount, minSilence-1)
	}

	// One high-prob frame should reset silence count.
	m.mu.Lock()
	m.prob = 0.9
	m.mu.Unlock()
	evt, err := sess.ProcessFrame(frame)
	if err != nil {
		t.Fatalf("ProcessFrame: %v", err)
	}
	if evt.Type != vad.VADSpeechContinue {
		t.Errorf("expected VADSpeechContinue, got %v", evt.Type)
	}
	if sess.silenceCount != 0 {
		t.Errorf("silenceCount = %d, want 0 after reset", sess.silenceCount)
	}
}

// ─── newSession tests ───────────────────────────────────────────────────

func TestNewSession_InitialState(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	m := &mockInferencer{}
	sess := newSession(cfg, m, 5, 10)

	if sess.state != stateSilence {
		t.Errorf("initial state = %v, want stateSilence", sess.state)
	}
	if sess.speechCount != 0 {
		t.Errorf("initial speechCount = %d, want 0", sess.speechCount)
	}
	if sess.silenceCount != 0 {
		t.Errorf("initial silenceCount = %d, want 0", sess.silenceCount)
	}
	if sess.minSpeechFrames != 5 {
		t.Errorf("minSpeechFrames = %d, want 5", sess.minSpeechFrames)
	}
	if sess.minSilenceFrames != 10 {
		t.Errorf("minSilenceFrames = %d, want 10", sess.minSilenceFrames)
	}
	if len(sess.lstmState) != stateSize {
		t.Errorf("lstmState len = %d, want %d", len(sess.lstmState), stateSize)
	}
	if sess.closed {
		t.Error("session should not be closed initially")
	}

	_ = sess.Close()
}

// ─── PCM conversion additional ──────────────────────────────────────────

func TestPCMToFloat32_Empty(t *testing.T) {
	t.Parallel()

	got := pcmToFloat32(nil)
	if len(got) != 0 {
		t.Errorf("expected empty result for nil input, got len %d", len(got))
	}
}

func TestPCMToFloat32_MultiSample(t *testing.T) {
	t.Parallel()

	// Encode two samples: 0 and 16384
	pcm := make([]byte, 4)
	binary.LittleEndian.PutUint16(pcm[0:], 0)
	binary.LittleEndian.PutUint16(pcm[2:], uint16(int16(16384)))

	got := pcmToFloat32(pcm)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0] != 0 {
		t.Errorf("got[0] = %v, want 0", got[0])
	}
	const eps = 1e-6
	want := float32(16384.0 / 32768.0)
	if diff := got[1] - want; diff < -eps || diff > eps {
		t.Errorf("got[1] = %v, want %v", got[1], want)
	}
}

// ─── Option functions ───────────────────────────────────────────────────

func TestWithMinSpeechFrames(t *testing.T) {
	t.Parallel()

	e := &Engine{}
	WithMinSpeechFrames(7)(e)
	if e.minSpeechFrames != 7 {
		t.Errorf("minSpeechFrames = %d, want 7", e.minSpeechFrames)
	}
}

func TestWithMinSilenceFrames(t *testing.T) {
	t.Parallel()

	e := &Engine{}
	WithMinSilenceFrames(20)(e)
	if e.minSilenceFrames != 20 {
		t.Errorf("minSilenceFrames = %d, want 20", e.minSilenceFrames)
	}
}

func TestWithONNXLibPath(t *testing.T) {
	t.Parallel()

	e := &Engine{}
	WithONNXLibPath("/opt/onnx/lib")(e)
	if e.onnxLibPath != "/opt/onnx/lib" {
		t.Errorf("onnxLibPath = %q, want %q", e.onnxLibPath, "/opt/onnx/lib")
	}
}

// ─── Close with inferencer error ────────────────────────────────────────

func TestClose_InferencerError(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	m := &mockInferencer{
		prob:     0.1,
		closeErr: fmt.Errorf("onnx cleanup failed"),
	}
	sess := makeSession(t, cfg, m, 3, 15)

	err := sess.Close()
	if err == nil {
		t.Fatal("expected error from Close when inferencer close fails")
	}
	if !m.closed {
		t.Error("expected inferencer to be marked as closed")
	}
}

// ─── Tests ───────────────────────────────────────────────────────────────────

func TestNewSession_ValidConfig(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		cfg  vad.Config
	}{
		{
			name: "16kHz_32ms",
			cfg:  validConfig(),
		},
		{
			name: "8kHz_32ms",
			cfg: vad.Config{
				SampleRate:       8000,
				FrameSizeMs:      32,
				SpeechThreshold:  0.6,
				SilenceThreshold: 0.4,
			},
		},
		{
			name: "16kHz_64ms",
			cfg: vad.Config{
				SampleRate:       16000,
				FrameSizeMs:      64,
				SpeechThreshold:  0.5,
				SilenceThreshold: 0.5,
			},
		},
	}

	for _, tc := range cases {
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
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := vad.Config{
				SampleRate:       tc.sampleRate,
				FrameSizeMs:      32,
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
	for i := range 10 {
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
	for i := range minSpeech - 1 {
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
	for i := range 5 {
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
	// The mock returns stateN filled with 0.42.
	m := &mockInferencer{
		prob:     0.1, // below threshold — stays silent
		stateVal: 0.42,
	}
	sess := makeSession(t, cfg, m, 3, 15)
	t.Cleanup(func() { _ = sess.Close() })

	frame := silenceFrame(cfg)

	// First frame: state should be all zeros (initial state).
	if _, err := sess.ProcessFrame(frame); err != nil {
		t.Fatalf("frame 1: %v", err)
	}
	call0 := m.callAt(0)
	for i, v := range call0.state {
		if v != 0 {
			t.Errorf("frame 1: state[%d] = %v, want 0 (initial state)", i, v)
		}
	}

	// Second frame: state must equal the stateN returned by the first call.
	if _, err := sess.ProcessFrame(frame); err != nil {
		t.Fatalf("frame 2: %v", err)
	}
	call1 := m.callAt(1)
	for i, v := range call1.state {
		if v != m.stateVal {
			t.Errorf("frame 2: state[%d] = %v, want %v (stateN from frame 1)", i, v, m.stateVal)
		}
	}
}

func TestReset_ClearsState(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	const minSpeech = 2
	m := &mockInferencer{prob: 0.9, stateVal: 0.5}
	sess := makeSession(t, cfg, m, minSpeech, 15)
	t.Cleanup(func() { _ = sess.Close() })

	frame := silenceFrame(cfg)

	// Drive the session into speech state.
	for i := range minSpeech {
		if _, err := sess.ProcessFrame(frame); err != nil {
			t.Fatalf("setup frame %d: %v", i, err)
		}
	}
	// Verify we're speaking.
	if sess.state != stateSpeaking {
		t.Fatalf("expected stateSpeaking before reset, got %v", sess.state)
	}
	// LSTM state should be non-zero after frames.
	if sess.lstmState[0] == 0 {
		t.Error("expected non-zero lstmState before reset")
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
	for i, v := range sess.lstmState {
		if v != 0 {
			t.Errorf("lstmState[%d] after Reset: %v, want 0", i, v)
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
