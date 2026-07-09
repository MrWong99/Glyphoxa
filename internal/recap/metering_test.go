package recap

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
)

// TestMeteringReportedUsage: a provider-reported EventUsage is recorded verbatim as
// LLMTokens on the recapped provider/model.
func TestMeteringReportedUsage(t *testing.T) {
	st := newFakeStore()
	tenantID := uuid.New()
	butler := storage.Agent{Role: storage.AgentRoleButler, Persona: "Butler."}
	sid := seedSession(st, tenantID, uuid.New(), "English", butler, time.Now(), sampleLines())

	rec := &capRec{}
	factory := func(_, _ string) (llm.Provider, error) {
		return &stubProvider{text: "recap", usage: &llm.Usage{InputTokens: 321, OutputTokens: 99}}, nil
	}
	eng := NewEngine(st, nil, rec, nil, WithProviderFactory(factory))
	if _, err := eng.Recap(context.Background(), []uuid.UUID{sid}); err != nil {
		t.Fatalf("Recap: %v", err)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("LLMTokens calls = %d, want 1", len(rec.calls))
	}
	got := rec.calls[0]
	if got.in != 321 || got.out != 99 {
		t.Errorf("tokens = (%d,%d), want (321,99) from reported usage", got.in, got.out)
	}
	if got.provider != observe.ProviderGroq {
		t.Errorf("provider label = %q, want groq (default)", got.provider)
	}
}

// TestMeteringEstimateNeverZero: with no EventUsage the engine records a ceil(chars/4)
// estimate per direction, never zero (ADR-0045).
func TestMeteringEstimateNeverZero(t *testing.T) {
	st := newFakeStore()
	tenantID := uuid.New()
	butler := storage.Agent{Role: storage.AgentRoleButler, Persona: "Butler."}
	sid := seedSession(st, tenantID, uuid.New(), "English", butler, time.Now(), sampleLines())

	rec := &capRec{}
	factory := func(_, _ string) (llm.Provider, error) {
		return &stubProvider{text: "a non-empty recap body"}, nil // no usage
	}
	eng := NewEngine(st, nil, rec, nil, WithProviderFactory(factory))
	if _, err := eng.Recap(context.Background(), []uuid.UUID{sid}); err != nil {
		t.Fatalf("Recap: %v", err)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("LLMTokens calls = %d, want 1", len(rec.calls))
	}
	got := rec.calls[0]
	if got.in <= 0 || got.out <= 0 {
		t.Errorf("estimate tokens = (%d,%d), want both > 0 (never zero)", got.in, got.out)
	}
}

// TestAttributionLog: the post-run log line carries the recapped session id and the
// estimated USD.
func TestAttributionLog(t *testing.T) {
	st := newFakeStore()
	tenantID := uuid.New()
	butler := storage.Agent{Role: storage.AgentRoleButler, Persona: "Butler."}
	sid := seedSession(st, tenantID, uuid.New(), "English", butler, time.Now(), sampleLines())

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	eng := NewEngine(st, nil, observe.Discard{}, log, WithProviderFactory(func(_, _ string) (llm.Provider, error) {
		return &stubProvider{text: "recap", usage: &llm.Usage{InputTokens: 10, OutputTokens: 5}}, nil
	}))
	if _, err := eng.Recap(context.Background(), []uuid.UUID{sid}); err != nil {
		t.Fatalf("Recap: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "recap: llm usage") {
		t.Errorf("missing attribution log line: %q", out)
	}
	if !strings.Contains(out, sid.String()) {
		t.Errorf("attribution log missing session id: %q", out)
	}
	if !strings.Contains(out, "estimated_usd") {
		t.Errorf("attribution log missing estimated_usd: %q", out)
	}
}

// TestNeverReadsCaps: the Store surface has no spend-cap method, so a recap cannot
// read a tenant cap or gate on it (ADR-0046 is live-session-only). This compiles as
// a structural proof: recap.Store deliberately omits GetTenantSpendCaps, and the
// engine still recaps normally regardless of any caps configured elsewhere.
func TestNeverReadsCaps(t *testing.T) {
	// The interface itself is the assertion — assign the store and confirm no
	// caps-reading method is required to satisfy recap.Store.
	var _ Store = newFakeStore()

	st := newFakeStore()
	tenantID := uuid.New()
	butler := storage.Agent{Role: storage.AgentRoleButler, Persona: "Butler."}
	sid := seedSession(st, tenantID, uuid.New(), "English", butler, time.Now(), sampleLines())
	eng := NewEngine(st, nil, observe.Discard{}, nil, WithProviderFactory(okFactory))
	if _, err := eng.Recap(context.Background(), []uuid.UUID{sid}); err != nil {
		t.Fatalf("Recap must succeed without any cap surface: %v", err)
	}
}
