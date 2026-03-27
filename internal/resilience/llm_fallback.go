package resilience

import (
	"context"
	"log/slog"

	"github.com/MrWong99/glyphoxa/pkg/provider/llm"
)

// LLMFallback implements [llm.Provider] with automatic failover across multiple
// LLM backends. Each backend has its own circuit breaker; when the primary fails
// or its breaker is open, the next healthy fallback is tried.
type LLMFallback struct {
	group *FallbackGroup[llm.Provider]
}

// Compile-time interface assertion.
var _ llm.Provider = (*LLMFallback)(nil)

// NewLLMFallback creates an [LLMFallback] with primary as the preferred backend.
func NewLLMFallback(primary llm.Provider, primaryName string, cfg FallbackConfig) *LLMFallback {
	return &LLMFallback{
		group: NewFallbackGroup(primary, primaryName, cfg),
	}
}

// AddFallback registers an additional LLM provider as a fallback.
func (f *LLMFallback) AddFallback(name string, provider llm.Provider) {
	f.group.AddFallback(name, provider)
}

// Complete sends the request to the first healthy provider and returns its
// response. If the primary fails, subsequent fallbacks are tried.
func (f *LLMFallback) Complete(ctx context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	return ExecuteWithResult(f.group, func(p llm.Provider) (*llm.CompletionResponse, error) {
		return p.Complete(ctx, req)
	})
}

// StreamCompletion sends the request to the first healthy provider and returns a
// streaming chunk channel. The returned channel is monitored: if a mid-stream
// error chunk (FinishReason == "error") arrives, the circuit breaker is notified
// so future calls prefer a healthy fallback.
func (f *LLMFallback) StreamCompletion(ctx context.Context, req llm.CompletionRequest) (<-chan llm.Chunk, error) {
	var activeIdx int
	ch, err := ExecuteWithResultIndex(f.group, func(p llm.Provider) (<-chan llm.Chunk, error) {
		return p.StreamCompletion(ctx, req)
	}, &activeIdx)
	if err != nil {
		return nil, err
	}

	// Wrap the chunk channel to detect mid-stream errors and record them
	// on the circuit breaker.
	out := make(chan llm.Chunk, 16)
	go func() {
		defer close(out)
		for chunk := range ch {
			if chunk.FinishReason == "error" && activeIdx < len(f.group.entries) {
				entry := &f.group.entries[activeIdx]
				entry.breaker.RecordFailure()
				slog.Warn("llm fallback: mid-stream error, recorded circuit breaker failure",
					"provider", entry.name, "error_text", chunk.Text)
			}
			select {
			case out <- chunk:
			case <-ctx.Done():
				return
			}
		}
	}()

	return out, nil
}

// CountTokens delegates to the first healthy provider's token counter.
func (f *LLMFallback) CountTokens(messages []llm.Message) (int, error) {
	return ExecuteWithResult(f.group, func(p llm.Provider) (int, error) {
		return p.CountTokens(messages)
	})
}

// Capabilities returns the capabilities of the first entry (the primary).
// This does not participate in failover because capabilities are static metadata.
func (f *LLMFallback) Capabilities() llm.ModelCapabilities {
	if len(f.group.entries) > 0 {
		return f.group.entries[0].value.Capabilities()
	}
	return llm.ModelCapabilities{}
}
