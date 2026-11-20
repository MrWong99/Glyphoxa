package assist

import (
	"context"
	"errors"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/billing"
	"github.com/MrWong99/Glyphoxa/internal/billing/billingtest"
	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
)

// assistCalls are the three paid entry points (#658): persona drafting,
// knowledge drafting, and the text call the map-pin suggester borrows.
var assistCalls = []struct {
	name string
	run  func(e *Engine, c storage.Campaign) error
}{
	{"GeneratePersona", func(e *Engine, c storage.Campaign) error {
		_, err := e.GeneratePersona(context.Background(), c, PersonaInput{Prompt: "a gruff innkeeper"})
		return err
	}},
	{"GenerateKnowledge", func(e *Engine, c storage.Campaign) error {
		_, err := e.GenerateKnowledge(context.Background(), c, "a harbour town")
		return err
	}},
	{"CallText", func(e *Engine, c storage.Campaign) error {
		_, err := e.CallText(context.Background(), c, "map_pin_suggestions", "system", "user", 64)
		return err
	}},
}

// Every assist call lands its usage in the Tenant's Usage Ledger under a real
// model id — including on a Provider Config that names a provider but no model,
// where the price row must be the provider's default model, never "".
func TestAssistCallsReachTheUsageLedger(t *testing.T) {
	for _, tc := range assistCalls {
		t.Run(tc.name, func(t *testing.T) {
			st := newFakeStore()
			c := seedCampaign(st, "")
			st.byComponent[c.TenantID] = storage.ProviderConfig{
				Provider: "groq", Component: storage.ComponentLLM, CredentialsLast4: llmbuild.EnvPlaceholderLast4,
			}
			usage := &billingtest.Usage{}
			eng := NewEngine(st, nil, billingtest.Recording(usage), nil, nil, WithProviderFactory(func(_, _ string) (llm.Provider, error) {
				// A reply every entry point can parse or return as-is; a failed
				// parse must still be billed, so the error is not asserted here.
				return &stubProvider{text: "You are someone.", usage: &llm.Usage{InputTokens: 42, OutputTokens: 7}}, nil
			}))

			_ = tc.run(eng, c)

			row := billingtest.OneRow(t, usage, c.TenantID, storage.ComponentLLM, "groq", llmbuild.DefaultModel("groq"))
			if row.LLMInputTokens != 42 || row.LLMOutputTokens != 7 {
				t.Errorf("ledger tokens = %d/%d, want 42/7", row.LLMInputTokens, row.LLMOutputTokens)
			}
		})
	}
}

// Usage the provider reported before a mid-stream failure is still ledgered.
func TestAssistFailedCallStillLedgersReportedUsage(t *testing.T) {
	st := newFakeStore()
	c := seedCampaign(st, "")
	usage := &billingtest.Usage{}
	eng := NewEngine(st, nil, billingtest.Recording(usage), nil, nil, WithProviderFactory(func(_, _ string) (llm.Provider, error) {
		return &stubProvider{text: "partial", usage: &llm.Usage{InputTokens: 5, OutputTokens: 2}, errText: "boom"}, nil
	}))
	if _, err := eng.GeneratePersona(context.Background(), c, PersonaInput{Prompt: "x"}); err == nil {
		t.Fatal("err = nil, want the stream error")
	}
	billingtest.OneRow(t, usage, c.TenantID, storage.ComponentLLM, "groq", llmbuild.DefaultModel(""))
}

// A platform-key Tenant whose allowance is spent is refused before the
// provider is even built; a BYOK-plan Tenant under the same gate is not.
func TestAssistCallsAllowanceGate(t *testing.T) {
	for _, tc := range assistCalls {
		t.Run(tc.name+"/exhausted", func(t *testing.T) {
			st := newFakeStore()
			c := seedCampaign(st, "")
			usage := &billingtest.Usage{}
			eng := NewEngine(st, nil, billingtest.Exhausted(usage), nil, nil, WithProviderFactory(func(_, _ string) (llm.Provider, error) {
				t.Error("provider built despite an exhausted allowance")
				return &stubProvider{text: "x"}, nil
			}))
			if err := tc.run(eng, c); !errors.Is(err, billing.ErrAllowanceExhausted) {
				t.Fatalf("err = %v, want ErrAllowanceExhausted", err)
			}
			if n := len(usage.Rows()); n != 0 {
				t.Errorf("refused call wrote %d ledger rows", n)
			}
		})
		t.Run(tc.name+"/byok", func(t *testing.T) {
			st := newFakeStore()
			c := seedCampaign(st, "")
			usage := &billingtest.Usage{}
			calls := 0
			eng := NewEngine(st, nil, billingtest.BYOK(usage), nil, nil, WithProviderFactory(func(_, _ string) (llm.Provider, error) {
				calls++
				return &stubProvider{text: "You are someone.", usage: &llm.Usage{InputTokens: 1, OutputTokens: 1}}, nil
			}))
			if err := tc.run(eng, c); errors.Is(err, billing.ErrAllowanceExhausted) {
				t.Fatalf("BYOK tenant refused: %v", err)
			}
			if calls != 1 {
				t.Errorf("provider built %d times, want 1", calls)
			}
		})
	}
}
