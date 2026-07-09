package recap

import (
	"context"
	"crypto/rand"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
)

func newCipher(t *testing.T) *crypto.Cipher {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}
	c, err := crypto.New(key)
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	return c
}

// fakeStore is an in-memory recap.Store for keyless unit tests.
type fakeStore struct {
	sessions    map[uuid.UUID]storage.VoiceSession
	lines       map[uuid.UUID][]storage.TranscriptLine
	campaigns   map[uuid.UUID]storage.Campaign
	butlers     map[uuid.UUID]storage.Agent          // keyed by campaignID
	configs     map[uuid.UUID]storage.ProviderConfig // keyed by config id
	byComponent map[uuid.UUID]storage.ProviderConfig // keyed by tenantID (llm only)
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		sessions:    map[uuid.UUID]storage.VoiceSession{},
		lines:       map[uuid.UUID][]storage.TranscriptLine{},
		campaigns:   map[uuid.UUID]storage.Campaign{},
		butlers:     map[uuid.UUID]storage.Agent{},
		configs:     map[uuid.UUID]storage.ProviderConfig{},
		byComponent: map[uuid.UUID]storage.ProviderConfig{},
	}
}

func (s *fakeStore) GetVoiceSession(_ context.Context, id uuid.UUID) (storage.VoiceSession, error) {
	vs, ok := s.sessions[id]
	if !ok {
		return storage.VoiceSession{}, storage.ErrNotFound
	}
	return vs, nil
}

func (s *fakeStore) ListTranscriptLines(_ context.Context, sessionID uuid.UUID) ([]storage.TranscriptLine, error) {
	return s.lines[sessionID], nil
}

func (s *fakeStore) GetCampaign(_ context.Context, id uuid.UUID) (storage.Campaign, error) {
	c, ok := s.campaigns[id]
	if !ok {
		return storage.Campaign{}, storage.ErrNotFound
	}
	return c, nil
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

// stubProvider is a scripted [llm.Provider]: one canned text response, an optional
// reported usage, an optional mid-stream error, and an optional missing-done to
// exercise the truncation guard. capture (when set) records each request.
type stubProvider struct {
	text    string
	usage   *llm.Usage
	errText string
	noDone  bool
	capture func(llm.Request)
}

// capRec is a StageRecorder that records the LLMTokens calls the meter tee forwards.
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
		// Usage rides before the error/truncation so a failed call can still see the
		// provider-reported usage that already arrived (ADR-0045 error rule).
		if p.usage != nil {
			ch <- llm.StreamEvent{Type: llm.EventUsage, Usage: *p.usage}
		}
		if p.errText != "" {
			ch <- llm.StreamEvent{Type: llm.EventError, Err: p.errText}
			return
		}
		if !p.noDone {
			ch <- llm.StreamEvent{Type: llm.EventDone, StopReason: "end_turn"}
		}
	}()
	return ch, nil
}
