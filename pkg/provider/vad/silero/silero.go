// Package silero implements the vad.Engine interface using the Silero VAD v5
// ONNX model via the yalue/onnxruntime_go binding.
//
// The ONNX Runtime shared library (libonnxruntime.so) must be available at
// runtime. Use [WithONNXLibPath] to specify a custom path, or install it
// system-wide. Download from https://github.com/microsoft/onnxruntime/releases
// or run: make onnx-libs
//
// The Silero VAD v5 model supports sample rates of 8000 Hz and 16000 Hz only.
// Each session maintains independent LSTM hidden state and a speech/silence
// state machine, making the Engine safe for concurrent use across sessions.
package silero

import (
	"encoding/binary"
	"fmt"
	"math"
	"sync"

	ort "github.com/yalue/onnxruntime_go"

	"github.com/MrWong99/glyphoxa/pkg/provider/vad"
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

// validChunkSizes lists the accepted sample counts per frame for each sample
// rate. Using an unsupported chunk size causes the model to produce near-zero
// probabilities without returning an error.
var validChunkSizes = map[int]map[int]struct{}{
	8000:  {256: {}, 512: {}, 768: {}},
	16000: {512: {}, 1024: {}, 1536: {}},
}

// initOnce guards the single ONNX Runtime environment initialisation.
var (
	initOnce sync.Once
	initErr  error
)

// inferencer runs a single frame through the Silero model.
//
// The interface exists so tests can inject a mock implementation without
// requiring an ONNX Runtime installation.
type inferencer interface {
	// infer takes audio samples and LSTM state, returns speech probability and
	// the updated state for the next frame.
	infer(samples []float32, sr int64, state []float32) (prob float32, stateN []float32, err error)
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

// WithONNXLibPath sets the path to the shared ONNX Runtime library
// (e.g. "/usr/lib/libonnxruntime.so"). When empty, onnxruntime_go uses its
// platform default search path.
func WithONNXLibPath(path string) Option {
	return func(e *Engine) { e.onnxLibPath = path }
}

// Engine is a vad.Engine backed by the Silero VAD v5 ONNX model. It is safe
// for concurrent use: multiple goroutines may call NewSession simultaneously
// to create independent sessions.
type Engine struct {
	modelPath        string
	minSpeechFrames  int
	minSilenceFrames int
	onnxLibPath      string
}

// New creates a new Silero VAD Engine. modelPath must point to the Silero VAD
// v5 ONNX model file on disk. Options can override the defaults.
//
// The ONNX Runtime environment is initialised lazily on the first call and
// shared for the lifetime of the process. Initialisation errors are persistent:
// if the first call fails, all subsequent calls return the same error.
func New(modelPath string, opts ...Option) (*Engine, error) {
	e := &Engine{
		modelPath:        modelPath,
		minSpeechFrames:  3,
		minSilenceFrames: 15,
	}
	for _, o := range opts {
		o(e)
	}

	// Initialise the ONNX Runtime environment exactly once per process.
	initOnce.Do(func() {
		if e.onnxLibPath != "" {
			ort.SetSharedLibraryPath(e.onnxLibPath)
		}
		initErr = ort.InitializeEnvironment()
	})
	if initErr != nil {
		return nil, fmt.Errorf("silero: initialize ONNX Runtime: %w", initErr)
	}

	return e, nil
}

// Close destroys the shared ONNX Runtime environment. It should only be called
// once all sessions created by this process have been closed.
func (e *Engine) Close() error {
	if err := ort.DestroyEnvironment(); err != nil {
		return fmt.Errorf("silero: destroy ONNX environment: %w", err)
	}
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
	inf, err := newONNXInferencer(e.modelPath, cfg.SampleRate, chunkSize)
	if err != nil {
		return nil, fmt.Errorf("silero: create inferencer: %w", err)
	}

	return newSession(cfg, inf, e.minSpeechFrames, e.minSilenceFrames), nil
}

// validateConfig checks that cfg is valid for Silero VAD v5.
func validateConfig(cfg vad.Config) error {
	if cfg.SampleRate <= 0 {
		return fmt.Errorf("silero: SampleRate must be > 0, got %d", cfg.SampleRate)
	}
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
					"valid sizes for %d Hz: 512, 1024, 1536 (e.g. FrameSizeMs=32, 64, or 96)",
				chunkSize, cfg.SampleRate, cfg.FrameSizeMs, cfg.SampleRate)
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

// onnxInferencer implements inferencer using onnxruntime_go. All tensors are
// pre-allocated once and reused across frames via AdvancedSession, matching
// the approach used by known-working Silero VAD Go bindings.
type onnxInferencer struct {
	sess          *ort.AdvancedSession
	inputTensor   *ort.Tensor[float32]
	stateTensor   *ort.Tensor[float32]
	srTensor      *ort.Tensor[int64]
	outTensor     *ort.Tensor[float32]
	stateNTensor  *ort.Tensor[float32]
	context       []float32 // context from previous frame
	effectiveSize int       // chunkSize + contextSize
	chunkSize     int
}

// newONNXInferencer creates an onnxInferencer from the given model file path.
// The sampleRate and chunkSize determine tensor shapes and context buffer size.
func newONNXInferencer(modelPath string, sampleRate, chunkSize int) (*onnxInferencer, error) {
	ctxSize := contextSize(sampleRate)
	effectiveSize := chunkSize + ctxSize

	inputTensor, err := ort.NewEmptyTensor[float32](ort.NewShape(1, int64(effectiveSize)))
	if err != nil {
		return nil, fmt.Errorf("create input tensor: %w", err)
	}
	stateTensor, err := ort.NewEmptyTensor[float32](ort.NewShape(2, 1, 128))
	if err != nil {
		inputTensor.Destroy() //nolint:errcheck
		return nil, fmt.Errorf("create state tensor: %w", err)
	}
	srTensor, err := ort.NewTensor(ort.NewShape(1), []int64{int64(sampleRate)})
	if err != nil {
		inputTensor.Destroy() //nolint:errcheck
		stateTensor.Destroy() //nolint:errcheck
		return nil, fmt.Errorf("create sr tensor: %w", err)
	}
	outTensor, err := ort.NewEmptyTensor[float32](ort.NewShape(1, 1))
	if err != nil {
		inputTensor.Destroy() //nolint:errcheck
		stateTensor.Destroy() //nolint:errcheck
		srTensor.Destroy()    //nolint:errcheck
		return nil, fmt.Errorf("create output tensor: %w", err)
	}
	stateNTensor, err := ort.NewEmptyTensor[float32](ort.NewShape(2, 1, 128))
	if err != nil {
		inputTensor.Destroy() //nolint:errcheck
		stateTensor.Destroy() //nolint:errcheck
		srTensor.Destroy()    //nolint:errcheck
		outTensor.Destroy()   //nolint:errcheck
		return nil, fmt.Errorf("create stateN tensor: %w", err)
	}

	sess, err := ort.NewAdvancedSession(
		modelPath,
		[]string{"input", "state", "sr"},
		[]string{"output", "stateN"},
		[]ort.Value{inputTensor, stateTensor, srTensor},
		[]ort.Value{outTensor, stateNTensor},
		nil,
	)
	if err != nil {
		inputTensor.Destroy()  //nolint:errcheck
		stateTensor.Destroy()  //nolint:errcheck
		srTensor.Destroy()     //nolint:errcheck
		outTensor.Destroy()    //nolint:errcheck
		stateNTensor.Destroy() //nolint:errcheck
		return nil, fmt.Errorf("create ONNX session from %q: %w", modelPath, err)
	}

	return &onnxInferencer{
		sess:          sess,
		inputTensor:   inputTensor,
		stateTensor:   stateTensor,
		srTensor:      srTensor,
		outTensor:     outTensor,
		stateNTensor:  stateNTensor,
		context:       make([]float32, ctxSize),
		effectiveSize: effectiveSize,
		chunkSize:     chunkSize,
	}, nil
}

// infer runs a single audio frame through the Silero VAD v5 model.
// The samples slice must contain exactly chunkSize float32 values.
// The state and stateN parameters are ignored — state is managed internally
// via pre-bound tensors. They are kept for interface compatibility.
func (o *onnxInferencer) infer(samples []float32, _ int64, _ []float32) (float32, []float32, error) {
	// Fill the input tensor: [context | new samples].
	data := o.inputTensor.GetData()
	clear(data)
	copy(data, o.context)
	copy(data[len(o.context):], samples)

	if err := o.sess.Run(); err != nil {
		return 0, nil, fmt.Errorf("run ONNX session: %w", err)
	}

	prob := o.outTensor.GetData()[0]

	// Copy stateN → state for the next frame.
	copy(o.stateTensor.GetData(), o.stateNTensor.GetData())

	// Save the last contextSize samples for the next frame.
	copy(o.context, data[len(data)-len(o.context):])

	return prob, nil, nil
}

// close releases all ONNX resources.
func (o *onnxInferencer) close() error {
	var firstErr error
	for _, d := range []interface{ Destroy() error }{
		o.sess, o.inputTensor, o.stateTensor, o.srTensor, o.outTensor, o.stateNTensor,
	} {
		if err := d.Destroy(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return fmt.Errorf("destroy ONNX resources: %w", firstErr)
	}
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
// frame must be raw little-endian signed 16-bit PCM at the SampleRate and
// FrameSizeMs configured when the session was created.
func (s *session) ProcessFrame(frame []byte) (vad.VADEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return vad.VADEvent{}, fmt.Errorf("silero: ProcessFrame called on closed session")
	}

	chunkSize := s.cfg.SampleRate * s.cfg.FrameSizeMs / 1000
	expectedBytes := chunkSize * 2 // int16 = 2 bytes per sample
	if len(frame) != expectedBytes {
		return vad.VADEvent{}, fmt.Errorf(
			"silero: frame is %d bytes, expected %d (sampleRate=%d, frameSizeMs=%d)",
			len(frame), expectedBytes, s.cfg.SampleRate, s.cfg.FrameSizeMs,
		)
	}

	samples := pcmToFloat32(frame)

	prob, stateN, err := s.inf.infer(samples, int64(s.cfg.SampleRate), s.lstmState)
	if err != nil {
		return vad.VADEvent{}, fmt.Errorf("silero: inference: %w", err)
	}

	// The real onnxInferencer manages state internally and returns nil.
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
		} else {
			s.silenceCount = 0
		}
		return vad.VADEvent{Type: vad.VADSpeechContinue, Probability: prob}
	}

	// Unreachable; keeps exhaustive switch analysis happy.
	return vad.VADEvent{Type: vad.VADSilence, Probability: prob}
}

// Reset clears all accumulated detection state. The LSTM state is zeroed
// and the speech/silence counters are reset. The session remains open and
// ready for new frames.
func (s *session) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.lstmState {
		s.lstmState[i] = 0
	}
	s.state = stateSilence
	s.speechCount = 0
	s.silenceCount = 0
}

// Close releases the ONNX session resources. After Close, ProcessFrame returns
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

// pcmToFloat32 converts raw little-endian signed 16-bit PCM bytes to float32
// samples in the range [-1.0, 1.0].
func pcmToFloat32(pcm []byte) []float32 {
	n := len(pcm) / 2
	out := make([]float32, n)
	const scale = 1.0 / float32(math.MaxInt16+1) // 1.0 / 32768.0
	for i := range n {
		sample := int16(binary.LittleEndian.Uint16(pcm[i*2:]))
		out[i] = float32(sample) * scale
	}
	return out
}
