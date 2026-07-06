package wirenpc

import (
	"context"
	"sync"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/pkg/tool"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
)

// modelCapturingProvider is a keyless streaming [llm.Provider] that records the
// Model of every [llm.Request] it is handed, then streams a trivial done event.
// It lets the engineFactory tests assert the model resolved from provider_config
// reaches the Groq completion request (#227) with no live API.
type modelCapturingProvider struct {
	mu     sync.Mutex
	models []string
}

func (p *modelCapturingProvider) Complete(_ context.Context, req llm.Request) (<-chan llm.StreamEvent, error) {
	p.mu.Lock()
	p.models = append(p.models, req.Model)
	p.mu.Unlock()
	ch := make(chan llm.StreamEvent)
	go func() {
		defer close(ch)
		ch <- llm.StreamEvent{Type: llm.EventText, Text: "ok"}
		ch <- llm.StreamEvent{Type: llm.EventDone, StopReason: "end_turn"}
	}()
	return ch, nil
}

func (p *modelCapturingProvider) lastModel(t *testing.T) string {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.models) == 0 {
		t.Fatal("provider.Complete was never called")
	}
	return p.models[len(p.models)-1]
}

// generateOnce drives one Agent turn through an engine so the provider records
// the request it carried.
func generateOnce(t *testing.T, spec npcSpec, prov llm.Provider) {
	t.Helper()
	factory := engineFactory(prov, tool.NewRegistry(), "en", observe.Discard{})
	eng := factory(spec)
	if _, err := eng.Generate(context.Background(), []llm.Message{
		{Role: llm.RoleSystem, Text: "You are Bart."},
		{Role: llm.RoleUser, Text: "Hello."},
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
}

// TestEngineFactory_ThreadsConfiguredModel is the #227 unit pin: the model on the
// npcSpec (resolved from provider_config) is the model the Groq request carries.
func TestEngineFactory_ThreadsConfiguredModel(t *testing.T) {
	prov := &modelCapturingProvider{}
	generateOnce(t, npcSpec{agentID: "bart", name: "Bart", model: "meta-llama/llama-4-scout-17b-16e-instruct"}, prov)
	if got := prov.lastModel(t); got != "meta-llama/llama-4-scout-17b-16e-instruct" {
		t.Errorf("request model = %q, want the configured model", got)
	}
}

// TestEngineFactory_EmptyModelFlowsThrough pins the fallback contract: an empty
// spec.model is passed through as "" at the wirenpc layer (NO fallback code
// here), leaving the openaicompat adapter to fill its provider default. This
// keeps the "empty → provider default" behavior with zero duplicated defaulting.
func TestEngineFactory_EmptyModelFlowsThrough(t *testing.T) {
	prov := &modelCapturingProvider{}
	generateOnce(t, npcSpec{agentID: "bart", name: "Bart", model: ""}, prov)
	if got := prov.lastModel(t); got != "" {
		t.Errorf("request model = %q, want empty (adapter fills the default)", got)
	}
}
