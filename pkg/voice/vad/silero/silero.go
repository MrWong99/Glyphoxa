// Package silero implements the vad.Engine interface using the Silero VAD v5
// model with a bespoke pure-Go forward pass (#468) — no ONNX Runtime, no CGO,
// no shared libraries.
//
// The Silero VAD v5 "op18 ifless" ONNX export is embedded in the binary at
// build time (see embed.go). At engine creation the model's protobuf is parsed
// once (onnx.go); each session compiles the branch for its sample rate into a
// static execution plan (graph.go) and runs it per frame with zero
// allocations.
//
// The model supports 8000 Hz and 16000 Hz, with exactly one valid chunk size
// each (256 and 512 samples — the window the model was trained on). Each
// session maintains independent LSTM hidden state and a speech/silence state
// machine, making the Engine safe for concurrent use across sessions.
package silero

import (
	"fmt"
	"math"
	"sync"

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/vad"
)

// Compile-time interface assertion.
var _ vad.Engine = (*Engine)(nil)

// stateSize is the flattened element count of the LSTM state tensor
// with shape [2, 1, 128].
const stateSize = 2 * 1 * 128

// supportedSampleRates lists the audio sample rates accepted by Silero VAD v5.
var supportedSampleRates = map[int]struct{}{
	8000:  {},
	16000: {},
}

// validChunkSizes lists the accepted sample count per frame for each sample
// rate: the exact window the model was trained on (512 samples at 16 kHz,
// 256 at 8 kHz). Earlier revisions also advertised 2× and 3× multiples, but
// those never worked: the previous ONNX-Runtime stack failed at inference
// time on them, so rejecting them at config validation is strictly an
// improvement (config error instead of a per-frame runtime error).
var validChunkSizes = map[int]map[int]struct{}{
	8000:  {256: {}},
	16000: {512: {}},
}

// parsedModel lazily parses the embedded ONNX model exactly once per process.
// Parse errors are persistent, matching the previous runtime-init behavior.
var parsedModel = sync.OnceValues(func() (*onnxModel, error) {
	return parseONNXModel(modelBytes)
})

// inferencer runs a single frame through the Silero model.
//
// The interface exists so tests can inject a mock implementation and observe
// the session state machine in isolation.
type inferencer interface {
	// infer takes audio samples and LSTM state, returns speech probability and
	// the updated state for the next frame.
	infer(samples []float32, sr int64, state []float32) (prob float32, stateN []float32, err error)
	// reset clears the inferencer's recurrent state (LSTM state and audio
	// context) so the next frame starts from a clean slate.
	reset()
	// close releases any resources held by the inferencer.
	close() error
}

// Option is a functional option for New.
type Option func(*Engine)

// WithMinSpeechFrames sets the minimum number of consecutive high-probability
// frames required to transition from silence to speech. Default: 3.
func WithMinSpeechFrames(n int) Option {
	return func(e *Engine) { e.minSpeechFrames = n }
}

// WithMinSilenceFrames sets the minimum number of consecutive low-probability
// frames required to transition from speech to silence. Default: 15.
func WithMinSilenceFrames(n int) Option {
	return func(e *Engine) { e.minSilenceFrames = n }
}

// Engine is a vad.Engine backed by the Silero VAD v5 model. It is safe for
// concurrent use: multiple goroutines may call NewSession simultaneously to
// create independent sessions.
type Engine struct {
	minSpeechFrames  int
	minSilenceFrames int
}

// New creates a new Silero VAD Engine using the embedded model. Options can
// override the defaults.
//
// The embedded model is parsed lazily on the first call and shared for the
// lifetime of the process. Parse errors are persistent: if the first call
// fails, all subsequent calls return the same error.
func New(opts ...Option) (*Engine, error) {
	e := &Engine{
		minSpeechFrames:  3,
		minSilenceFrames: 15,
	}
	for _, o := range opts {
		o(e)
	}

	if _, err := parsedModel(); err != nil {
		return nil, fmt.Errorf("silero: parse embedded model: %w", err)
	}
	return e, nil
}

// Close releases engine resources. The pure-Go engine holds none — the parsed
// model is a process-wide read-only singleton — so Close is a no-op kept for
// interface stability with earlier ONNX-Runtime-backed revisions.
func (e *Engine) Close() error {
	return nil
}

// NewSession creates a new VAD session with the given configuration. The session
// is immediately ready to accept audio frames via ProcessFrame.
//
// Returns an error if the configuration is invalid (including unsupported sample
// rates; Silero v5 accepts 8000 and 16000 only) or if the model cannot be loaded.
func (e *Engine) NewSession(cfg vad.Config) (vad.SessionHandle, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}

	chunkSize := cfg.SampleRate * cfg.FrameSizeMs / 1000
	inf, err := newGoInferencer(cfg.SampleRate, chunkSize)
	if err != nil {
		return nil, fmt.Errorf("silero: create inferencer: %w", err)
	}

	return newSession(cfg, inf, e.minSpeechFrames, e.minSilenceFrames), nil
}

// validateConfig checks that cfg is valid for Silero VAD v5.
func validateConfig(cfg vad.Config) error {
	if _, ok := supportedSampleRates[cfg.SampleRate]; !ok {
		return fmt.Errorf("silero: unsupported SampleRate %d; Silero v5 supports 8000 and 16000 Hz only", cfg.SampleRate)
	}
	if cfg.FrameSizeMs <= 0 {
		return fmt.Errorf("silero: FrameSizeMs must be > 0, got %d", cfg.FrameSizeMs)
	}
	chunkSize := cfg.SampleRate * cfg.FrameSizeMs / 1000
	if allowed, ok := validChunkSizes[cfg.SampleRate]; ok {
		if _, valid := allowed[chunkSize]; !valid {
			return fmt.Errorf(
				"silero: chunk size %d samples (SampleRate=%d, FrameSizeMs=%d) is not supported; "+
					"valid sizes for %d Hz: %v",
				chunkSize, cfg.SampleRate, cfg.FrameSizeMs, cfg.SampleRate, allowed)
		}
	}
	if cfg.SpeechThreshold < 0 || cfg.SpeechThreshold > 1 {
		return fmt.Errorf("silero: SpeechThreshold %.3f out of range [0.0, 1.0]", cfg.SpeechThreshold)
	}
	if cfg.SilenceThreshold < 0 || cfg.SilenceThreshold > 1 {
		return fmt.Errorf("silero: SilenceThreshold %.3f out of range [0.0, 1.0]", cfg.SilenceThreshold)
	}
	if cfg.SilenceThreshold > cfg.SpeechThreshold {
		return fmt.Errorf("silero: SilenceThreshold %.3f must be <= SpeechThreshold %.3f",
			cfg.SilenceThreshold, cfg.SpeechThreshold)
	}
	return nil
}

// contextSize returns the number of audio context samples the Silero VAD v5
// model requires prepended to each chunk for proper detection.
func contextSize(sampleRate int) int {
	if sampleRate == 8000 {
		return 32
	}
	return 64 // 16 kHz
}

// goInferencer implements inferencer with the bespoke pure-Go forward pass.
// The compiled program owns all tensor buffers; a frame run performs no
// allocations.
type goInferencer struct {
	prog      *program
	context   []float32 // trailing samples of the previous frame
	chunkSize int
}

// newGoInferencer compiles the embedded model's branch for the given sample
// rate into an executable program sized for chunkSize-sample frames.
func newGoInferencer(sampleRate, chunkSize int) (*goInferencer, error) {
	model, err := parsedModel()
	if err != nil {
		return nil, fmt.Errorf("parse embedded model: %w", err)
	}
	ctxSize := contextSize(sampleRate)
	prog, err := compileProgram(model, sampleRate, ctxSize+chunkSize)
	if err != nil {
		return nil, err
	}
	return &goInferencer{
		prog:      prog,
		context:   make([]float32, ctxSize),
		chunkSize: chunkSize,
	}, nil
}

// infer runs a single audio frame through the model. The samples slice must
// contain exactly chunkSize float32 values. The state and stateN parameters
// are unused — recurrent state is managed internally — and kept for interface
// compatibility with the mock inferencer.
func (g *goInferencer) infer(samples []float32, _ int64, _ []float32) (float32, []float32, error) {
	if len(samples) != g.chunkSize {
		return 0, nil, fmt.Errorf("frame has %d samples, want %d", len(samples), g.chunkSize)
	}

	// Fill the model input: [context | new samples].
	in := g.prog.input.f
	copy(in, g.context)
	copy(in[len(g.context):], samples)

	g.prog.run()

	// Save the last contextSize samples for the next frame.
	copy(g.context, in[len(in)-len(g.context):])

	return g.prog.output.f[0], nil, nil
}

// reset clears the LSTM state and the inter-frame audio context.
func (g *goInferencer) reset() {
	g.prog.reset()
	clear(g.context)
}

// close releases resources. The pure-Go inferencer holds only Go memory, so
// this is a no-op.
func (g *goInferencer) close() error {
	return nil
}

// vadState tracks whether the VAD is currently in silence or speech mode.
type vadState int

const (
	stateSilence  vadState = iota // no speech detected
	stateSpeaking                 // speech in progress
)

// session implements vad.SessionHandle for the Silero VAD engine.
type session struct {
	mu               sync.Mutex
	inf              inferencer
	cfg              vad.Config
	minSpeechFrames  int
	minSilenceFrames int

	// LSTM state, fed back into the model each frame. Shape [2, 1, 128].
	lstmState []float32

	// State machine.
	state        vadState
	speechCount  int // consecutive frames above SpeechThreshold
	silenceCount int // consecutive frames below SilenceThreshold

	closed bool
}

// newSession constructs a session backed by the given inferencer. cfg must
// have already been validated by validateConfig.
func newSession(cfg vad.Config, inf inferencer, minSpeech, minSilence int) *session {
	return &session{
		inf:              inf,
		cfg:              cfg,
		minSpeechFrames:  minSpeech,
		minSilenceFrames: minSilence,
		lstmState:        make([]float32, stateSize),
	}
}

// ProcessFrame analyses a single audio frame and returns the detection result.
// The frame's SampleRate and FrameMs must match the values from the Config the
// session was created with.
func (s *session) ProcessFrame(frame audio.Frame) (vad.VADEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return vad.VADEvent{}, fmt.Errorf("silero: ProcessFrame called on closed session")
	}

	if frame.SampleRate() != s.cfg.SampleRate || frame.FrameMs() != s.cfg.FrameSizeMs {
		return vad.VADEvent{}, fmt.Errorf(
			"silero: frame is %d Hz/%d ms, want %d Hz/%d ms",
			frame.SampleRate(), frame.FrameMs(),
			s.cfg.SampleRate, s.cfg.FrameSizeMs,
		)
	}

	samples := samplesToFloat32(frame.Samples())

	prob, stateN, err := s.inf.infer(samples, int64(s.cfg.SampleRate), s.lstmState)
	if err != nil {
		return vad.VADEvent{}, fmt.Errorf("silero: inference: %w", err)
	}

	// The real goInferencer manages state internally and returns nil.
	// The mock inferencer returns a non-nil stateN for test verification.
	if stateN != nil {
		s.lstmState = stateN
	}

	return s.step(float64(prob)), nil
}

// step applies the speech/silence state machine for one frame. Must be called
// with s.mu held.
func (s *session) step(prob float64) vad.VADEvent {
	switch s.state {
	case stateSilence:
		if prob > s.cfg.SpeechThreshold {
			s.speechCount++
			if s.speechCount >= s.minSpeechFrames {
				s.state = stateSpeaking
				s.speechCount = 0
				return vad.VADEvent{Type: vad.VADSpeechStart, Probability: prob}
			}
		} else {
			s.speechCount = 0
		}
		return vad.VADEvent{Type: vad.VADSilence, Probability: prob}

	case stateSpeaking:
		if prob < s.cfg.SilenceThreshold {
			s.silenceCount++
			if s.silenceCount >= s.minSilenceFrames {
				s.state = stateSilence
				s.silenceCount = 0
				return vad.VADEvent{Type: vad.VADSpeechEnd, Probability: prob}
			}
			if s.silenceCount == 1 {
				// First sub-threshold frame after voiced frames: the speaker just
				// fell silent, though the utterance stays open until the hangover
				// elapses. Surfaced so the barge-in confirm window can disarm on the
				// real end of voicing instead of the hangover-delayed speech_end
				// (#431); segmentation semantics are untouched.
				return vad.VADEvent{Type: vad.VADVoicingStopped, Probability: prob}
			}
			return vad.VADEvent{Type: vad.VADSpeechContinue, Probability: prob}
		}
		if s.silenceCount > 0 {
			s.silenceCount = 0
			// Voicing picked back up before the hangover elapsed: no new
			// speech_start fires (the utterance never closed), so this is the
			// only onset signal a mid-utterance resumption produces (#431).
			return vad.VADEvent{Type: vad.VADVoicingResumed, Probability: prob}
		}
		return vad.VADEvent{Type: vad.VADSpeechContinue, Probability: prob}
	}

	// Unreachable; keeps exhaustive switch analysis happy.
	return vad.VADEvent{Type: vad.VADSilence, Probability: prob}
}

// Reset clears all accumulated detection state: the inferencer's recurrent
// state (LSTM state and audio context) and the speech/silence counters. The
// session remains open and ready for new frames.
func (s *session) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.inf.reset()
	for i := range s.lstmState {
		s.lstmState[i] = 0
	}
	s.state = stateSilence
	s.speechCount = 0
	s.silenceCount = 0
}

// Close releases the session resources. After Close, ProcessFrame returns
// an error. Calling Close more than once is safe and returns nil.
func (s *session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}
	s.closed = true
	if err := s.inf.close(); err != nil {
		return fmt.Errorf("silero: close session: %w", err)
	}
	return nil
}

// samplesToFloat32 scales signed 16-bit PCM samples to float32 in the range
// [-1.0, 1.0], as expected by the Silero VAD v5 model.
func samplesToFloat32(samples []int16) []float32 {
	out := make([]float32, len(samples))
	const scale = 1.0 / float32(math.MaxInt16+1) // 1.0 / 32768.0
	for i, s := range samples {
		out[i] = float32(s) * scale
	}
	return out
}
