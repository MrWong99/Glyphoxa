package agenttool_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/tool"
	"github.com/MrWong99/Glyphoxa/pkg/voice/agenttool"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/providererr"
)

// refusingProvider ends every completion with the provider's refusal class and
// counts how often it was asked.
type refusingProvider struct{ calls atomic.Int32 }

func (p *refusingProvider) Complete(context.Context, llm.Request) (<-chan llm.StreamEvent, error) {
	p.calls.Add(1)
	ch := make(chan llm.StreamEvent, 1)
	ch <- llm.StreamEvent{Type: llm.EventError, Err: "anthropic: the model refused the request (stop_reason refusal, category cyber)", ErrClass: llm.ErrClassRefusal}
	close(ch)
	return ch, nil
}

// TestEngine_Refusal_SurfacesTypedAndIsNotRetried pins the engine half of the
// refusal outcome (#702): a provider decline fails the turn with a typed
// [*providererr.RefusalError] — so a caller can tell "the provider declined"
// from a transport fault without reading the message — and it is asked exactly
// once: the same prompt would be declined again.
func TestEngine_Refusal_SurfacesTypedAndIsNotRetried(t *testing.T) {
	p := &refusingProvider{}
	eng := agenttool.NewEngine(p, tool.NewGrantSet(tool.NewRegistry()), "", "claude-haiku-5-5", 0, 0)

	text, err := eng.GenerateStream(t.Context(), []llm.Message{{Role: llm.RoleUser, Text: "hi"}}, func(string) error { return nil })
	var refusal *providererr.RefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("err = %v (text %q), want a *providererr.RefusalError", err, text)
	}
	if text != "" {
		t.Errorf("text = %q, want none: a refusal is not a reply", text)
	}
	if n := p.calls.Load(); n != 1 {
		t.Errorf("provider asked %d times, want 1 (a refusal is never retried)", n)
	}
}
