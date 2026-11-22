package planchat

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/billing"
	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
)

// modelSpy records the model and the reply cap of each request it is asked to
// complete.
type modelSpy struct {
	scriptedProvider
	models    []string
	maxTokens []int
}

func (p *modelSpy) Complete(ctx context.Context, req llm.Request) (<-chan llm.StreamEvent, error) {
	p.models = append(p.models, req.Model)
	p.maxTokens = append(p.maxTokens, req.MaxTokens)
	return p.scriptedProvider.Complete(ctx, req)
}

// A Tenant with neither a chat nor an llm Provider Config plans on the
// deployment default (#702): its adapter and its model.
func TestExchange_NoConfigRunsDeploymentDefault(t *testing.T) {
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
	campaignID := uuid.New()
	st := newFakeStore(campaignID)
	provider := &modelSpy{scriptedProvider: scriptedProvider{scripts: [][]llm.StreamEvent{textDone("A plan.")}}}
	var gotID string
	eng := NewEngine(st, nil, &fakeSearcher{}, &fakeWriter{}, fakeRecapper{}, billing.NewOffSession(st, nil, nil), nil, nil,
		WithLLMDefault(d),
		WithProviderFactory(func(providerID, _ string) (llm.Provider, error) {
			gotID = providerID
			return provider, nil
		}))

	var ev collectEvents
	if _, err := eng.Exchange(context.Background(), uuid.New(), storage.Campaign{ID: campaignID}, st.thread.ID, "Plan it", &ev); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if gotID != "anthropic" {
		t.Errorf("provider = %q, want anthropic", gotID)
	}
	if len(provider.models) == 0 || provider.models[0] != "claude-haiku-4-5" {
		t.Errorf("request models = %v, want claude-haiku-4-5", provider.models)
	}
}

// One model default per resolved config (#702 review): a model-less Anthropic
// row plans on the model the voice session runs for it.
func TestExchange_ModellessAnthropicRowRunsTheVoiceModel(t *testing.T) {
	campaignID := uuid.New()
	st := newFakeStore(campaignID)
	st.tenant[storage.ComponentLLM] = storage.ProviderConfig{Provider: "anthropic", CredentialsLast4: "env"}
	provider := &modelSpy{scriptedProvider: scriptedProvider{scripts: [][]llm.StreamEvent{textDone("A plan.")}}}
	eng := NewEngine(st, nil, &fakeSearcher{}, &fakeWriter{}, fakeRecapper{}, billing.NewOffSession(st, nil, nil), nil, nil,
		WithProviderFactory(func(_, _ string) (llm.Provider, error) { return provider, nil }))

	var ev collectEvents
	if _, err := eng.Exchange(context.Background(), uuid.New(), storage.Campaign{ID: campaignID}, st.thread.ID, "Plan it", &ev); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	want := llmbuild.DeploymentDefault{}.VoiceModel("anthropic", "")
	if len(provider.models) == 0 || provider.models[0] != want {
		t.Errorf("request models = %v, want %q", provider.models, want)
	}
}
