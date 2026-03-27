package resilience

import (
	"context"
	"log/slog"
	"sync"

	"github.com/MrWong99/glyphoxa/pkg/provider/tts"
)

// TTSFallback implements [tts.Provider] with automatic failover across multiple
// TTS backends. Each backend has its own circuit breaker.
type TTSFallback struct {
	group *FallbackGroup[tts.Provider]
}

// Compile-time interface assertion.
var _ tts.Provider = (*TTSFallback)(nil)

// NewTTSFallback creates a [TTSFallback] with primary as the preferred backend.
func NewTTSFallback(primary tts.Provider, primaryName string, cfg FallbackConfig) *TTSFallback {
	return &TTSFallback{
		group: NewFallbackGroup(primary, primaryName, cfg),
	}
}

// AddFallback registers an additional TTS provider as a fallback.
func (f *TTSFallback) AddFallback(name string, provider tts.Provider) {
	f.group.AddFallback(name, provider)
}

// SynthesizeStream consumes text fragments and returns a channel of audio bytes,
// trying the first healthy provider. The input text is buffered so it can be
// replayed to a fallback if the primary fails mid-stream. The returned audio
// channel is monitored for premature closure; on mid-stream failure the circuit
// breaker is notified and a retry is attempted with the next healthy provider.
func (f *TTSFallback) SynthesizeStream(ctx context.Context, text <-chan string, voice tts.VoiceProfile) (<-chan []byte, error) {
	// Buffer text from the input channel so it can be replayed on mid-stream failure.
	var (
		bufMu   sync.Mutex
		textBuf []string
		done    bool // true once the input text channel is fully consumed
	)
	bufCh := make(chan string, 16)

	go func() {
		defer close(bufCh)
		for t := range text {
			bufMu.Lock()
			textBuf = append(textBuf, t)
			bufMu.Unlock()
			select {
			case bufCh <- t:
			case <-ctx.Done():
				return
			}
		}
		bufMu.Lock()
		done = true
		bufMu.Unlock()
	}()

	var activeIdx int
	audioCh, err := ExecuteWithResultIndex(f.group, func(p tts.Provider) (<-chan []byte, error) {
		return p.SynthesizeStream(ctx, bufCh, voice)
	}, &activeIdx)
	if err != nil {
		return nil, err
	}

	// Wrap the audio channel to detect mid-stream failures and retry
	// with the next provider if the primary drops the stream.
	out := make(chan []byte, 64)
	go func() {
		defer close(out)

		// Forward audio from the active provider.
		for chunk := range audioCh {
			select {
			case out <- chunk:
			case <-ctx.Done():
				return
			}
		}

		// Check if the text channel was fully consumed. If not, the TTS
		// provider dropped the stream mid-synthesis.
		bufMu.Lock()
		inputDone := done
		remaining := make([]string, len(textBuf))
		copy(remaining, textBuf)
		bufMu.Unlock()

		if inputDone {
			return // Normal completion — all text was consumed.
		}

		// Mid-stream failure: record it on the circuit breaker.
		if activeIdx < len(f.group.entries) {
			entry := &f.group.entries[activeIdx]
			entry.breaker.RecordFailure()
			slog.Warn("tts fallback: mid-stream failure, recorded circuit breaker failure",
				"provider", entry.name)
		}

		// Attempt retry with next healthy provider using buffered text.
		replayCh := make(chan string, len(remaining)+16)
		for _, t := range remaining {
			replayCh <- t
		}
		// Continue draining the original buffer channel into replay.
		go func() {
			defer close(replayCh)
			for t := range bufCh {
				replayCh <- t
			}
		}()

		var retryIdx int
		retryCh, retryErr := ExecuteWithResultIndex(f.group, func(p tts.Provider) (<-chan []byte, error) {
			return p.SynthesizeStream(ctx, replayCh, voice)
		}, &retryIdx)
		if retryErr != nil {
			slog.Error("tts fallback: mid-stream retry failed", "err", retryErr)
			return
		}

		slog.Info("tts fallback: mid-stream retry succeeded with fallback provider")
		for chunk := range retryCh {
			select {
			case out <- chunk:
			case <-ctx.Done():
				return
			}
		}
	}()

	return out, nil
}

// ListVoices returns available voices from the first healthy provider.
func (f *TTSFallback) ListVoices(ctx context.Context) ([]tts.VoiceProfile, error) {
	return ExecuteWithResult(f.group, func(p tts.Provider) ([]tts.VoiceProfile, error) {
		return p.ListVoices(ctx)
	})
}

// CloneVoice creates a new voice profile using the first healthy provider.
func (f *TTSFallback) CloneVoice(ctx context.Context, samples [][]byte) (*tts.VoiceProfile, error) {
	return ExecuteWithResult(f.group, func(p tts.Provider) (*tts.VoiceProfile, error) {
		return p.CloneVoice(ctx, samples)
	})
}
