package recap

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/billing"
	"github.com/MrWong99/Glyphoxa/internal/billing/billingtest"
	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
)

// seedRecap seeds one recappable session and returns its tenant and id.
func seedRecap(st *fakeStore) (tenantID, sessionID uuid.UUID) {
	tenantID = uuid.New()
	butler := storage.Agent{Role: storage.AgentRoleButler, Persona: "Butler."}
	return tenantID, seedSession(st, tenantID, uuid.New(), "English", butler, time.Now(), sampleLines())
}

// An off-session recap (the slash command, the GenerateRecap RPC — no caller
// metering on the ctx) lands in the Tenant's Usage Ledger under a real model
// id, even when the Provider Config names a provider without a model (#658).
func TestOffSessionRecapReachesTheUsageLedger(t *testing.T) {
	st := newFakeStore()
	tenantID, sid := seedRecap(st)
	st.byComponent[tenantID] = storage.ProviderConfig{Provider: "groq", CredentialsLast4: llmbuild.EnvPlaceholderLast4}
	usage := &billingtest.Usage{}
	eng := NewEngine(st, nil, billingtest.Recording(usage), nil, nil, WithProviderFactory(func(_, _ string) (llm.Provider, error) {
		return &stubProvider{text: "recap", usage: &llm.Usage{InputTokens: 321, OutputTokens: 99}}, nil
	}))

	if _, err := eng.Recap(context.Background(), []uuid.UUID{sid}); err != nil {
		t.Fatalf("Recap: %v", err)
	}
	row := billingtest.OneRow(t, usage, tenantID, storage.ComponentLLM, "groq", llmbuild.DefaultVoiceModel("groq"))
	if row.LLMInputTokens != 321 || row.LLMOutputTokens != 99 {
		t.Errorf("ledger tokens = %d/%d, want 321/99", row.LLMInputTokens, row.LLMOutputTokens)
	}
}

// Usage reported before a midway failure is still ledgered.
func TestOffSessionRecapFailureStillLedgers(t *testing.T) {
	st := newFakeStore()
	tenantID, sid := seedRecap(st)
	usage := &billingtest.Usage{}
	eng := NewEngine(st, nil, billingtest.Recording(usage), nil, nil, WithProviderFactory(func(_, _ string) (llm.Provider, error) {
		return &stubProvider{text: "partial", usage: &llm.Usage{InputTokens: 4, OutputTokens: 2}, errText: "boom"}, nil
	}))
	if _, err := eng.Recap(context.Background(), []uuid.UUID{sid}); err == nil {
		t.Fatal("Recap err = nil, want failure")
	}
	billingtest.OneRow(t, usage, tenantID, storage.ComponentLLM, "groq", llmbuild.DefaultVoiceModel(""))
}

// A platform-key Tenant with a spent allowance gets no recap and no provider
// call; a BYOK-plan Tenant under the same gate is recapped.
func TestOffSessionRecapAllowanceGate(t *testing.T) {
	st := newFakeStore()
	_, sid := seedRecap(st)
	usage := &billingtest.Usage{}
	eng := NewEngine(st, nil, billingtest.Exhausted(usage), nil, nil, WithProviderFactory(func(_, _ string) (llm.Provider, error) {
		return &stubProvider{text: "recap", capture: func(llm.Request) { t.Error("provider called despite an exhausted allowance") }}, nil
	}))
	if _, err := eng.Recap(context.Background(), []uuid.UUID{sid}); !errors.Is(err, billing.ErrAllowanceExhausted) {
		t.Fatalf("Recap err = %v, want ErrAllowanceExhausted", err)
	}
	if n := len(usage.Rows()); n != 0 {
		t.Errorf("refused recap wrote %d ledger rows", n)
	}

	byok := NewEngine(st, nil, billingtest.BYOK(usage), nil, nil, WithProviderFactory(func(_, _ string) (llm.Provider, error) {
		return &stubProvider{text: "recap", usage: &llm.Usage{InputTokens: 1, OutputTokens: 1}}, nil
	}))
	if _, err := byok.Recap(context.Background(), []uuid.UUID{sid}); err != nil {
		t.Fatalf("BYOK tenant: Recap = %v, want a recap", err)
	}
}

// A recap a CALLER meters (the Butler's recap Tool in a live session, a chat
// exchange) is that caller's spend: it must not ALSO be written to the ledger
// by the engine (the caller's ledger already has it — a second row would bill
// the Tenant twice), and the off-session allowance gate does not apply (the
// caller ran its own).
func TestCallerMeteredRecapBypassesTheOffSessionSeam(t *testing.T) {
	st := newFakeStore()
	_, sid := seedRecap(st)
	usage := &billingtest.Usage{}
	caller := &capRec{}
	eng := NewEngine(st, nil, billingtest.Exhausted(usage), nil, nil, WithProviderFactory(func(_, _ string) (llm.Provider, error) {
		return &stubProvider{text: "recap", usage: &llm.Usage{InputTokens: 40, OutputTokens: 9}}, nil
	}))

	ctx := WithMetering(context.Background(), Metering{Rec: caller})
	if _, err := eng.Recap(ctx, []uuid.UUID{sid}); err != nil {
		t.Fatalf("Recap: %v", err)
	}
	if len(caller.calls) != 1 {
		t.Errorf("caller's recorder calls = %+v, want the one completion", caller.calls)
	}
	if n := len(usage.Rows()); n != 0 {
		t.Errorf("caller-metered recap wrote %d ledger rows of its own; the caller's ledger already holds the usage", n)
	}
}
