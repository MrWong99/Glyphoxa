package highlight

import (
	"context"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
)

// stallingProvider models a wedged provider stream: it never sends and closes
// only when the caller's ctx ends, exactly as the real adapters do on cancel.
type stallingProvider struct{}

func (stallingProvider) Complete(ctx context.Context, _ llm.Request) (<-chan llm.StreamEvent, error) {
	ch := make(chan llm.StreamEvent)
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch, nil
}

// TestRunClassifierTimeoutUnpinsWorker pins the per-classify deadline: a stalled
// provider stream ends as an LLM error within the bound instead of pinning the
// detector's single worker for the rest of the session.
func TestRunClassifierTimeoutUnpinsWorker(t *testing.T) {
	t.Parallel()
	// Built but never started: runClassifier is exercised directly, so there is
	// no worker to Close (Close assumes a started detector).
	d := newDetector(stallingProvider{}, "m", nil, nil, nil, nil, nil, Config{})
	d.classifyTimeout = 50 * time.Millisecond

	done := make(chan observe.HighlightOutcome, 1)
	go func() {
		_, outcome, _ := d.runClassifier(llm.Request{})
		done <- outcome
	}()
	select {
	case outcome := <-done:
		if outcome != observe.HighlightLLMError {
			t.Fatalf("outcome = %v, want HighlightLLMError", outcome)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runClassifier did not return: the stalled stream pinned the worker")
	}
}
