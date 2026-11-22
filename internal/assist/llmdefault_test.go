package assist

import (
	"context"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/billing/billingtest"
	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
)

// A Tenant with no LLM Provider Config drafts on the deployment default (#702):
// that provider's adapter, that model requested AND priced under the real
// provider label.
func TestGeneratePersona_NoConfigRunsDeploymentDefault(t *testing.T) {
	d, err := llmbuild.DeploymentDefaultFromEnv(func(k string) string {
		return map[string]string{
			llmbuild.EnvDefaultProvider: "anthropic",
			llmbuild.EnvDefaultModel:    "claude-haiku-4-5",
			"ANTHROPIC_API_KEY":         "test-key",
		}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	st := newFakeStore()
	c := seedCampaign(st, "")
	rec := &capRec{}
	var (
		req   llm.Request
		gotID string
	)
	eng := NewEngine(st, nil, billingtest.Ungated(), rec, nil, WithLLMDefault(d),
		WithProviderFactory(func(providerID, _ string) (llm.Provider, error) {
			gotID = providerID
			return &stubProvider{text: "You are someone.", usage: &llm.Usage{InputTokens: 42, OutputTokens: 7}, capture: func(r llm.Request) { req = r }}, nil
		}))
	if _, err := eng.GeneratePersona(context.Background(), c, PersonaInput{Prompt: "x"}); err != nil {
		t.Fatalf("GeneratePersona: %v", err)
	}
	if gotID != "anthropic" {
		t.Errorf("provider = %q, want anthropic", gotID)
	}
	if req.Model != "claude-haiku-4-5" {
		t.Errorf("request model = %q, want claude-haiku-4-5", req.Model)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("LLMTokens calls = %d, want 1", len(rec.calls))
	}
	if got := rec.calls[0]; got.provider != observe.ProviderAnthropic || got.model != "claude-haiku-4-5" {
		t.Errorf("priced (%s,%s), want (anthropic, claude-haiku-4-5)", got.provider, got.model)
	}
}
