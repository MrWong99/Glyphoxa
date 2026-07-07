package orchestrator_test

import (
	"sync"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/observe"
)

// providerCall is one captured [observe.StageRecorder.ProviderCall]: the bounded
// labels the counter carries.
type providerCall struct {
	stage    observe.Stage
	provider observe.Provider
	outcome  observe.Outcome
}

// providerErr is one captured [observe.StageRecorder.ProviderError].
type providerErr struct {
	stage    observe.Stage
	provider observe.Provider
}

// metricsSpy is a [observe.StageRecorder] that captures the #125-wired emit-sites
// (STTRequest, TTSTotal, VADHangover, ProviderCall, ProviderError) so the stage
// tests can assert the counters/histograms move with the right labels without a
// Prometheus backend. Embeds [observe.Discard] so every other method is a no-op.
// Safe for concurrent use.
type metricsSpy struct {
	observe.Discard
	mu sync.Mutex

	sttRequests []observe.Provider
	ttsTotals   []observe.Provider
	vadHangs    []time.Duration
	calls       []providerCall
	errs        []providerErr

	// #127 usage meters.
	sttAudio []time.Duration // STTAudioSeconds durations
	ttsChars []ttsCharsRec   // TTSCharacters (provider, chars)
}

// ttsCharsRec is one captured TTSCharacters call.
type ttsCharsRec struct {
	provider observe.Provider
	chars    int
}

func (s *metricsSpy) STTRequest(p observe.Provider, _ time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sttRequests = append(s.sttRequests, p)
}

func (s *metricsSpy) TTSTotal(p observe.Provider, _ time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ttsTotals = append(s.ttsTotals, p)
}

func (s *metricsSpy) VADHangover(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vadHangs = append(s.vadHangs, d)
}

func (s *metricsSpy) ProviderCall(stage observe.Stage, p observe.Provider, o observe.Outcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, providerCall{stage: stage, provider: p, outcome: o})
}

func (s *metricsSpy) ProviderError(stage observe.Stage, p observe.Provider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errs = append(s.errs, providerErr{stage: stage, provider: p})
}

func (s *metricsSpy) STTAudioSeconds(_ observe.Provider, d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sttAudio = append(s.sttAudio, d)
}

func (s *metricsSpy) TTSCharacters(p observe.Provider, chars int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ttsChars = append(s.ttsChars, ttsCharsRec{provider: p, chars: chars})
}

func (s *metricsSpy) snapshot() ([]observe.Provider, []observe.Provider, []time.Duration, []providerCall, []providerErr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]observe.Provider(nil), s.sttRequests...),
		append([]observe.Provider(nil), s.ttsTotals...),
		append([]time.Duration(nil), s.vadHangs...),
		append([]providerCall(nil), s.calls...),
		append([]providerErr(nil), s.errs...)
}

// audioSeconds returns the captured STTAudioSeconds durations.
func (s *metricsSpy) audioSeconds() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.sttAudio...)
}

// characters returns the captured TTSCharacters records.
func (s *metricsSpy) characters() []ttsCharsRec {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ttsCharsRec(nil), s.ttsChars...)
}
