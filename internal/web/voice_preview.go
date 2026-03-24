package web

import (
	"context"
	"sync"
	"time"

	"github.com/MrWong99/glyphoxa/internal/agent/npcstore"
)

// VoicePreviewProvider synthesizes a short audio preview for an NPC voice.
type VoicePreviewProvider interface {
	// SynthesizePreview generates audio bytes for the given text using the
	// specified voice configuration. It returns the audio data, the content
	// type (e.g. "audio/mpeg"), and any error.
	SynthesizePreview(ctx context.Context, text string, voice npcstore.VoiceConfig) ([]byte, string, error)
}

// voicePreviewRateLimiter enforces a per-user rate limit on voice preview
// requests using a sliding window.
type voicePreviewRateLimiter struct {
	mu      sync.Mutex
	windows map[string][]time.Time
	limit   int
	window  time.Duration
}

// newVoicePreviewRateLimiter creates a rate limiter allowing limit requests per
// window duration per user.
func newVoicePreviewRateLimiter(limit int, window time.Duration) *voicePreviewRateLimiter {
	return &voicePreviewRateLimiter{
		windows: make(map[string][]time.Time),
		limit:   limit,
		window:  window,
	}
}

// Allow checks whether the given user ID is within the rate limit. If allowed,
// the request is recorded and true is returned.
func (rl *voicePreviewRateLimiter) Allow(userID string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-rl.window)

	// Prune expired entries.
	times := rl.windows[userID]
	start := 0
	for start < len(times) && times[start].Before(cutoff) {
		start++
	}
	times = times[start:]

	if len(times) >= rl.limit {
		rl.windows[userID] = times
		return false
	}

	rl.windows[userID] = append(times, now)
	return true
}
