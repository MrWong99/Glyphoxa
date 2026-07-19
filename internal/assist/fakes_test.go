package assist

import (
	"context"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
)

// fakeStore is an in-memory assist.Store for keyless unit tests.
type fakeStore struct {
	butlers     map[uuid.UUID]storage.Agent          // keyed by campaignID
	configs     map[uuid.UUID]storage.ProviderConfig // keyed by config id
	byComponent map[uuid.UUID]storage.ProviderConfig // keyed by tenantID (llm only)
	nodes       map[uuid.UUID][]storage.KGNode       // keyed by campaignID
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		butlers:     map[uuid.UUID]storage.Agent{},
		configs:     map[uuid.UUID]storage.ProviderConfig{},
		byComponent: map[uuid.UUID]storage.ProviderConfig{},
		nodes:       map[uuid.UUID][]storage.KGNode{},
	}
}

func (s *fakeStore) GetButler(_ context.Context, campaignID uuid.UUID) (storage.Agent, error) {
	a, ok := s.butlers[campaignID]
	if !ok {
		return storage.Agent{}, storage.ErrNotFound
	}
	return a, nil
}

func (s *fakeStore) GetProviderConfig(_ context.Context, id uuid.UUID) (storage.ProviderConfig, error) {
	c, ok := s.configs[id]
	if !ok {
		return storage.ProviderConfig{}, storage.ErrNotFound
	}
	return c, nil
}

func (s *fakeStore) GetProviderConfigByComponent(_ context.Context, tenantID uuid.UUID, component storage.Component) (storage.ProviderConfig, error) {
	if component != storage.ComponentLLM {
		return storage.ProviderConfig{}, storage.ErrNotFound
	}
	c, ok := s.byComponent[tenantID]
	if !ok {
		return storage.ProviderConfig{}, storage.ErrNotFound
	}
	return c, nil
}

func (s *fakeStore) ListNodes(_ context.Context, campaignID uuid.UUID) ([]storage.KGNode, error) {
	return s.nodes[campaignID], nil
}

// stubProvider is a scripted [llm.Provider]: one canned text response, an
// optional reported usage, an optional mid-stream error. capture (when set)
// records each request — the prompt-content assertions read it.
type stubProvider struct {
	text    string
	usage   *llm.Usage
	errText string
	capture func(llm.Request)
}

func (p *stubProvider) Complete(_ context.Context, req llm.Request) (<-chan llm.StreamEvent, error) {
	if p.capture != nil {
		p.capture(req)
	}
	ch := make(chan llm.StreamEvent, 8)
	go func() {
		defer close(ch)
		if p.text != "" {
			ch <- llm.StreamEvent{Type: llm.EventText, Text: p.text}
		}
		if p.usage != nil {
			ch <- llm.StreamEvent{Type: llm.EventUsage, Usage: *p.usage}
		}
		if p.errText != "" {
			ch <- llm.StreamEvent{Type: llm.EventError, Err: p.errText}
			return
		}
		ch <- llm.StreamEvent{Type: llm.EventDone, StopReason: "end_turn"}
	}()
	return ch, nil
}

// capRec is a StageRecorder recording the LLMTokens calls the meter tee forwards.
type capRec struct {
	observe.Discard
	calls []llmTok
}

type llmTok struct {
	provider observe.Provider
	model    string
	in, out  int
}

func (r *capRec) LLMTokens(p observe.Provider, model string, in, out int) {
	r.calls = append(r.calls, llmTok{p, model, in, out})
}

// seedCampaign wires a campaign + butler into st and returns the campaign.
func seedCampaign(st *fakeStore, language string) storage.Campaign {
	c := storage.Campaign{
		ID:       uuid.New(),
		TenantID: uuid.New(),
		Name:     "Test Campaign",
		System:   "D&D 5e",
		Language: language,
	}
	st.butlers[c.ID] = storage.Agent{ID: uuid.New(), CampaignID: c.ID, Role: storage.AgentRoleButler}
	return c
}
