// Package energy implements a pure-Go, energy-based Voice Activity Detection
// engine with no external dependencies. It uses RMS energy calculated from
// raw little-endian int16 PCM samples, adaptive peak tracking for
// auto-calibration to the current audio environment, and exponential moving
// average (EMA) smoothing to distinguish speech from silence.
package energy

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"sync/atomic"

	"github.com/MrWong99/glyphoxa/pkg/provider/vad"
)

// Compile-time assertion that Engine implements vad.Engine.
var _ vad.Engine = (*Engine)(nil)

// Default values for Engine options.
const (
	// defaultMinSpeechFrames is the default number of consecutive above-threshold
	// frames required before a VADSpeechStart event is emitted.
	defaultMinSpeechFrames = 3

	// defaultMinSilenceFrames is the default number of consecutive below-threshold
	// frames required before a VADSpeechEnd event is emitted (~450 ms at 30 ms/frame).
	defaultMinSilenceFrames = 15

	// defaultSmoothingFactor is the default EMA coefficient: 70% history, 30% new value.
	defaultSmoothingFactor = 0.7
)

// peakDecay is the per-frame multiplicative decay applied to the adaptive peak
// energy tracker, providing slow auto-calibration without losing track of loud
// transients.
const peakDecay = 0.9995

// initialPeak is the seed value for the adaptive peak tracker. It is chosen to
// be smaller than any realistic audio RMS so that the first non-silent frame
// immediately drives the peak to the correct level.
const initialPeak = 1.0

// vadState is the internal speech-detection state of a session.
type vadState int

const (
	stateSilence vadState = iota
	stateSpeaking
)

// Option is a functional option that configures an Engine.
type Option func(*Engine)

// WithMinSpeechFrames returns an Option that sets the minimum number of
// consecutive speech frames (i.e. frames whose smoothed normalised energy
// exceeds Config.SpeechThreshold) required before a VADSpeechStart event is
// emitted. Higher values reduce false positives at the cost of slightly
// increased speech-start latency. Default: 3.
func WithMinSpeechFrames(n int) Option {
	return func(e *Engine) {
		e.minSpeechFrames = n
	}
}

// WithMinSilenceFrames returns an Option that sets the minimum number of
// consecutive silence frames (i.e. frames whose smoothed normalised energy
// falls below Config.SilenceThreshold) required before a VADSpeechEnd event
// is emitted. Higher values prevent premature speech-end detection at the cost
// of slightly increased silence-detection latency. Default: 15.
func WithMinSilenceFrames(n int) Option {
	return func(e *Engine) {
		e.minSilenceFrames = n
	}
}

// WithSmoothingFactor returns an Option that sets the EMA smoothing coefficient
// applied to the normalised energy signal before threshold comparisons. The
// value must be in [0.0, 1.0]: 0.0 means no history (instant response), 1.0
// means ignore new data entirely. Default: 0.7.
func WithSmoothingFactor(f float64) Option {
	return func(e *Engine) {
		e.smoothingFactor = f
	}
}

// Engine is the stateless factory for energy-based VAD sessions. It is safe
// for concurrent use; multiple goroutines may call NewSession simultaneously.
type Engine struct {
	minSpeechFrames  int
	minSilenceFrames int
	smoothingFactor  float64
}

// New constructs an Engine with sensible defaults, then applies opts in order.
func New(opts ...Option) *Engine {
	e := &Engine{
		minSpeechFrames:  defaultMinSpeechFrames,
		minSilenceFrames: defaultMinSilenceFrames,
		smoothingFactor:  defaultSmoothingFactor,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// NewSession creates a new, immediately usable VAD session for a single audio
// stream. It validates cfg and returns an error if any field is out of range.
//
// Validation rules:
//   - SampleRate > 0
//   - FrameSizeMs > 0
//   - SpeechThreshold in (0.0, 1.0]
//   - SilenceThreshold in [0.0, SpeechThreshold)
func (e *Engine) NewSession(cfg vad.Config) (vad.SessionHandle, error) {
	if cfg.SampleRate <= 0 {
		return nil, fmt.Errorf("energy: sample rate must be > 0, got %d", cfg.SampleRate)
	}
	if cfg.FrameSizeMs <= 0 {
		return nil, fmt.Errorf("energy: frame size must be > 0 ms, got %d", cfg.FrameSizeMs)
	}
	if cfg.SpeechThreshold <= 0 || cfg.SpeechThreshold > 1.0 {
		return nil, fmt.Errorf("energy: speech threshold %g is not in (0.0, 1.0]", cfg.SpeechThreshold)
	}
	if cfg.SilenceThreshold < 0 || cfg.SilenceThreshold >= cfg.SpeechThreshold {
		return nil, fmt.Errorf("energy: silence threshold %g must be in [0.0, %g)", cfg.SilenceThreshold, cfg.SpeechThreshold)
	}

	// 16-bit samples: 2 bytes per sample.
	frameBytes := cfg.SampleRate * cfg.FrameSizeMs / 1000 * 2

	return &session{
		cfg:              cfg,
		frameBytes:       frameBytes,
		minSpeechFrames:  e.minSpeechFrames,
		minSilenceFrames: e.minSilenceFrames,
		smoothingFactor:  e.smoothingFactor,
		peakEnergy:       initialPeak,
	}, nil
}

// session implements vad.SessionHandle. All detection state is protected by mu;
// the closed flag uses an atomic so that Close is always safe to call
// concurrently without acquiring mu.
type session struct {
	mu     sync.Mutex
	closed uint32 // atomic: 1 when the session has been closed

	cfg              vad.Config
	frameBytes       int
	minSpeechFrames  int
	minSilenceFrames int
	smoothingFactor  float64

	// Detection state — all guarded by mu.
	currentState             vadState
	smoothedEnergy           float64
	peakEnergy               float64
	consecutiveSpeechFrames  int
	consecutiveSilenceFrames int
}

// ProcessFrame analyses a single PCM audio frame and returns the VAD result.
//
// frame must contain exactly (SampleRate * FrameSizeMs / 1000) little-endian
// int16 samples (i.e. SampleRate * FrameSizeMs / 1000 * 2 bytes). An error is
// returned if the frame length is wrong or if the session has been closed.
func (s *session) ProcessFrame(frame []byte) (vad.VADEvent, error) {
	// Fast path: avoid acquiring mu if already closed.
	if atomic.LoadUint32(&s.closed) != 0 {
		return vad.VADEvent{}, errors.New("energy: session is closed")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Re-check after the lock in case Close raced with our fast-path check.
	if atomic.LoadUint32(&s.closed) != 0 {
		return vad.VADEvent{}, errors.New("energy: session is closed")
	}

	if len(frame) != s.frameBytes {
		return vad.VADEvent{}, fmt.Errorf(
			"energy: frame is %d bytes but expected %d bytes (SampleRate %d, FrameSizeMs %d)",
			len(frame), s.frameBytes, s.cfg.SampleRate, s.cfg.FrameSizeMs,
		)
	}

	// Compute RMS energy of the little-endian int16 samples.
	rms := computeRMS(frame)

	// Adaptive peak tracking: decay the running peak, then update if rms is higher.
	s.peakEnergy *= peakDecay
	if rms > s.peakEnergy {
		s.peakEnergy = rms
	}

	// Normalise energy to [0.0, 1.0] relative to the adaptive peak.
	var normalised float64
	if s.peakEnergy > 0 {
		normalised = rms / s.peakEnergy
		if normalised > 1.0 {
			normalised = 1.0
		}
	}

	// Apply EMA: smoothingFactor * old + (1 - smoothingFactor) * new.
	s.smoothedEnergy = s.smoothingFactor*s.smoothedEnergy + (1-s.smoothingFactor)*normalised

	prob := s.smoothedEnergy

	switch s.currentState {
	case stateSilence:
		if prob > s.cfg.SpeechThreshold {
			s.consecutiveSpeechFrames++
			if s.consecutiveSpeechFrames >= s.minSpeechFrames {
				s.currentState = stateSpeaking
				s.consecutiveSpeechFrames = 0
				s.consecutiveSilenceFrames = 0
				slog.Debug("vad/energy: speech start", "prob", prob, "rms", rms, "peak", s.peakEnergy)
				return vad.VADEvent{Type: vad.VADSpeechStart, Probability: prob}, nil
			}
		} else {
			s.consecutiveSpeechFrames = 0
		}
		return vad.VADEvent{Type: vad.VADSilence, Probability: prob}, nil

	case stateSpeaking:
		if prob < s.cfg.SilenceThreshold {
			s.consecutiveSilenceFrames++
			if s.consecutiveSilenceFrames >= s.minSilenceFrames {
				s.currentState = stateSilence
				s.consecutiveSilenceFrames = 0
				s.consecutiveSpeechFrames = 0
				slog.Debug("vad/energy: speech end", "prob", prob)
				return vad.VADEvent{Type: vad.VADSpeechEnd, Probability: prob}, nil
			}
		} else {
			s.consecutiveSilenceFrames = 0
		}
		return vad.VADEvent{Type: vad.VADSpeechContinue, Probability: prob}, nil

	default:
		return vad.VADEvent{Type: vad.VADSilence, Probability: prob}, nil
	}
}

// Reset clears all accumulated detection state, returning the session to its
// initial condition as if it had just been created. It is safe to call after
// Close; in that case it resets state but ProcessFrame will still return an
// error.
func (s *session) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentState = stateSilence
	s.smoothedEnergy = 0
	s.peakEnergy = initialPeak
	s.consecutiveSpeechFrames = 0
	s.consecutiveSilenceFrames = 0
}

// Close marks the session as closed. Any subsequent call to ProcessFrame
// returns an error. Calling Close more than once is safe and always returns nil.
func (s *session) Close() error {
	atomic.StoreUint32(&s.closed, 1)
	return nil
}

// computeRMS returns the root-mean-square energy of a buffer of little-endian
// int16 PCM samples. Returns 0.0 for empty input.
func computeRMS(frame []byte) float64 {
	n := len(frame) / 2
	if n == 0 {
		return 0
	}
	var sumSq float64
	for i := range n {
		sample := int16(binary.LittleEndian.Uint16(frame[i*2:]))
		v := float64(sample)
		sumSq += v * v
	}
	return math.Sqrt(sumSq / float64(n))
}
