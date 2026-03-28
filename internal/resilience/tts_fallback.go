package resilience

import (
	"context"
	"log/slog"
	"sync"
	"time"

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
	)
	bufCh := make(chan string, 16)
	// doneCh is closed when the input text channel is fully consumed.
	doneCh := make(chan struct{})

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
		close(doneCh)
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

		// Determine whether the audio stream ended normally or was cut
		// short. The buffer goroutine closes doneCh when all input text
		// has been forwarded to bufCh. Wait briefly for that signal; if
		// text is still in flight the provider dropped mid-stream.
		select {
		case <-doneCh:
			return // Normal completion — all text was consumed.
		case <-ctx.Done():
			return
		default:
		}
		// doneCh not ready — the buffer goroutine may need a moment to
		// finish (common scheduling race on fast paths). Give it 1 ms
		// before assuming a genuine mid-stream failure.
		select {
		case <-doneCh:
			return // Normal completion — buffer goroutine caught up.
		case <-time.After(time.Millisecond):
			// Buffer goroutine still hasn't finished — likely mid-stream
			// failure.
		case <-ctx.Done():
			return
		}

		bufMu.Lock()
		remaining := make([]string, len(textBuf))
		copy(remaining, textBuf)
		bufMu.Unlock()

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
