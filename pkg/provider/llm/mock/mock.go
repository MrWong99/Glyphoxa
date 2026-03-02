// Package mock provides a test double for the llm.Provider interface.
//
// Use Provider in unit tests to verify that the orchestrator sends correct
// CompletionRequests and to feed controlled responses without a live LLM backend.
// All fields are safe to set before calling any method; mutating them during a
// concurrent call is the caller's responsibility.
//
// Example:
//
//	p := &mock.Provider{
//	    CompleteResponse: &llm.CompletionResponse{Content: "Hello!"},
//	}
//	resp, err := p.Complete(ctx, req)
package mock

import (
	"context"
	"sync"

	"github.com/MrWong99/glyphoxa/pkg/provider/llm"
)

// StreamCall records a single invocation of StreamCompletion.
type StreamCall struct {
	// Ctx is the context passed to StreamCompletion.
	Ctx context.Context
	// Req is the CompletionRequest passed to StreamCompletion.
	Req llm.CompletionRequest
}

// CompleteCall records a single invocation of Complete.
type CompleteCall struct {
	// Ctx is the context passed to Complete.
	Ctx context.Context
	// Req is the CompletionRequest passed to Complete.
	Req llm.CompletionRequest
}

// CountTokensCall records a single invocation of CountTokens.
type CountTokensCall struct {
	// Messages is the slice passed to CountTokens.
	Messages []llm.Message
}

// Provider is a mock implementation of llm.Provider.
// Zero values for response fields cause methods to return zero values and nil errors.
// Set Err fields to inject errors.
type Provider struct {
	mu sync.Mutex

	// --- Configurable responses ---

	// StreamChunks is the sequence of Chunk values emitted on the channel returned
	// by StreamCompletion. All chunks are sent before the channel is closed.
	// When StreamChunksSequence is also set, this field serves as the fallback
	// after the sequence is exhausted.
	StreamChunks []llm.Chunk

	// StreamChunksSequence, when non-empty, provides a different set of chunks for
	// each successive StreamCompletion call. The first call pops the first entry,
	// the second call the second, and so on. Once the sequence is exhausted,
	// subsequent calls fall back to StreamChunks. This is useful for testing
	// multi-turn interactions (e.g., tool-call loops) where the model must return
	// different responses on each invocation.
	StreamChunksSequence [][]llm.Chunk

	// StreamErr, if non-nil, is returned as the error from StreamCompletion instead
	// of starting a channel.
	StreamErr error

	// CompleteResponse is returned by Complete. May be nil (returns nil, nil).
	CompleteResponse *llm.CompletionResponse

	// CompleteErr, if non-nil, is returned as the error from Complete.
	CompleteErr error

	// TokenCount is returned by CountTokens.
	TokenCount int

	// CountTokensErr, if non-nil, is returned as the error from CountTokens.
	CountTokensErr error

	// ModelCapabilities is returned by Capabilities.
	ModelCapabilities llm.ModelCapabilities

	// --- Call records (read after test) ---

	// StreamCalls records every invocation of StreamCompletion in order.
	StreamCalls []StreamCall

	// CompleteCalls records every invocation of Complete in order.
	CompleteCalls []CompleteCall

	// CountTokensCalls records every invocation of CountTokens in order.
	CountTokensCalls []CountTokensCall

	// CapabilitiesCallCount is the number of times Capabilities was called.
	CapabilitiesCallCount int
}

// StreamCompletion records the call and returns a channel that emits StreamChunks.
// If StreamErr is set, it returns nil, StreamErr without opening a channel.
func (p *Provider) StreamCompletion(ctx context.Context, req llm.CompletionRequest) (<-chan llm.Chunk, error) {
	p.mu.Lock()
	if p.StreamErr != nil {
		err := p.StreamErr
		p.StreamCalls = append(p.StreamCalls, StreamCall{Ctx: ctx, Req: req})
		p.mu.Unlock()
		return nil, err
	}
	// If a sequence is configured and not exhausted, pop the next entry.
	var source []llm.Chunk
	if len(p.StreamChunksSequence) > 0 {
		source = p.StreamChunksSequence[0]
		p.StreamChunksSequence = p.StreamChunksSequence[1:]
	} else {
		source = p.StreamChunks
	}
	chunks := make([]llm.Chunk, len(source))
	copy(chunks, source)
	p.StreamCalls = append(p.StreamCalls, StreamCall{Ctx: ctx, Req: req})
	p.mu.Unlock()

	ch := make(chan llm.Chunk, len(chunks))
	go func() {
		defer close(ch)
		for _, c := range chunks {
			select {
			case <-ctx.Done():
				return
			case ch <- c:
			}
		}
	}()
	return ch, nil
}

// Complete records the call and returns CompleteResponse, CompleteErr.
func (p *Provider) Complete(ctx context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.CompleteCalls = append(p.CompleteCalls, CompleteCall{Ctx: ctx, Req: req})
	return p.CompleteResponse, p.CompleteErr
}

// CountTokens records the call and returns TokenCount, CountTokensErr.
func (p *Provider) CountTokens(messages []llm.Message) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	msgs := make([]llm.Message, len(messages))
	copy(msgs, messages)
	p.CountTokensCalls = append(p.CountTokensCalls, CountTokensCall{Messages: msgs})
	return p.TokenCount, p.CountTokensErr
}

// Capabilities records the call and returns ModelCapabilities.
func (p *Provider) Capabilities() llm.ModelCapabilities {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.CapabilitiesCallCount++
	return p.ModelCapabilities
}

// Reset clears all recorded calls. Thread-safe.
func (p *Provider) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.StreamCalls = nil
	p.CompleteCalls = nil
	p.CountTokensCalls = nil
	p.CapabilitiesCallCount = 0
}

// Ensure Provider implements llm.Provider at compile time.
var _ llm.Provider = (*Provider)(nil)
