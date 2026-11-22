package rpc

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/spend"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
)

func voiceHaikuDefault(t *testing.T) llmbuild.DeploymentDefault {
	t.Helper()
	d, err := llmbuild.DeploymentDefaultFromEnv(func(k string) string {
		return map[string]string{
			llmbuild.EnvDefaultProvider: "anthropic",
			llmbuild.EnvDefaultModel:    "claude-haiku-4-5",
			"ANTHROPIC_API_KEY":         "test-key",
		}[k]
	})
	if err != nil {
		t.Fatalf("DeploymentDefaultFromEnv: %v", err)
	}
	return d
}

// The Anthropic catalog comes from the Models API with the key saved in the
// ANTHROPIC slot — not the newest LLM row's key — and pins the voice default
// first.
func TestListModels_AnthropicLiveCatalog(t *testing.T) {
	t.Parallel()
	cipher := voiceTestCipher(t)
	store := &fakeVoiceStore{
		// The Tenant is currently ON groq (its newest LLM row) …
		configs: map[storage.Component]storage.ProviderConfig{
			storage.ComponentLLM: savedConfig(t, cipher, storage.ComponentLLM, "groq", "gsk-groq-secret"),
		},
		// … and has an Anthropic key saved in the other slot.
		older: []storage.ProviderConfig{savedConfig(t, cipher, storage.ComponentLLM, "anthropic", "sk-ant-secret")},
	}
	srv := NewVoiceServer(store, cipher, nil)
	var seen atomic.Value
	srv.listAnthropicModels = func(_ context.Context, apiKey string) ([]string, error) {
		seen.Store(apiKey)
		return []string{"claude-opus-4-8", "claude-haiku-5-5", "claude-haiku-4-5", "claude-opus-4-8"}, nil
	}
	srv.listModels = func(context.Context, string) ([]string, error) {
		t.Error("the Groq catalog was fetched for an Anthropic request")
		return nil, nil
	}

	resp, err := srv.ListModels(tenantCtx(), connect.NewRequest(&managementv1.ListModelsRequest{Provider: "anthropic"}))
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	want := []string{"claude-haiku-5-5", "claude-haiku-4-5", "claude-opus-4-8"}
	if got := resp.Msg.GetModels(); !reflect.DeepEqual(got, want) {
		t.Errorf("models = %v, want %v (voice default pinned, rest sorted, deduped)", got, want)
	}
	if got, _ := seen.Load().(string); got != "sk-ant-secret" {
		t.Errorf("catalog key = %q, want the key saved in the anthropic slot", got)
	}
}

// And the Groq catalog keeps using the Groq slot's key while the Tenant is on
// Anthropic.
func TestListModels_GroqUsesItsOwnSlotKey(t *testing.T) {
	t.Parallel()
	cipher := voiceTestCipher(t)
	store := &fakeVoiceStore{
		configs: map[storage.Component]storage.ProviderConfig{
			storage.ComponentLLM: savedConfig(t, cipher, storage.ComponentLLM, "anthropic", "sk-ant-secret"),
		},
		older: []storage.ProviderConfig{savedConfig(t, cipher, storage.ComponentLLM, "groq", "gsk-groq-secret")},
	}
	srv := NewVoiceServer(store, cipher, nil)
	var seen atomic.Value
	srv.listModels = func(_ context.Context, apiKey string) ([]string, error) {
		seen.Store(apiKey)
		return nil, nil
	}
	if _, err := srv.ListModels(tenantCtx(), connect.NewRequest(&managementv1.ListModelsRequest{Provider: "groq"})); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if got, _ := seen.Load().(string); got != "gsk-groq-secret" {
		t.Errorf("catalog key = %q, want the groq slot's key", got)
	}
}

// A failed Anthropic listing degrades to the two Haiku ids the price table
// knows — never an empty select, and never an unpriced suggestion.
func TestListModels_AnthropicFailureFallsBackToPricedHaiku(t *testing.T) {
	t.Parallel()
	srv := NewVoiceServer(&fakeVoiceStore{}, nil, nil)
	srv.listAnthropicModels = func(context.Context, string) ([]string, error) {
		return nil, errors.New("anthropic unreachable")
	}
	resp, err := srv.ListModels(tenantCtx(), connect.NewRequest(&managementv1.ListModelsRequest{Provider: "anthropic"}))
	if err != nil {
		t.Fatalf("ListModels must not error on catalog failure: %v", err)
	}
	want := []string{"claude-haiku-5-5", "claude-haiku-4-5"}
	if got := resp.Msg.GetModels(); !reflect.DeepEqual(got, want) {
		t.Errorf("fallback models = %v, want %v", got, want)
	}
}

// Every model the catalog offers WITHOUT the provider's say-so — the pinned
// defaults and the fallback lists — has a price row (#702).
func TestOfferedDefaultModelsArePriced(t *testing.T) {
	t.Parallel()
	for provider, models := range map[string][]string{
		"groq":      llmCatalog("groq", llmbuild.DeploymentDefault{}, nil),
		"anthropic": append(llmCatalog("anthropic", llmbuild.DeploymentDefault{}, nil), anthropicFallbackModels...),
	} {
		for _, m := range models {
			if !spend.LLMPriced(observe.Provider(provider), m) {
				t.Errorf("(%s, %s) is offered by default but has no price row", provider, m)
			}
		}
	}
}

// The deployment default's model is the pinned first entry of its provider.
func TestListModels_PinsDeploymentDefaultModel(t *testing.T) {
	t.Parallel()
	srv := NewVoiceServer(&fakeVoiceStore{}, nil, nil)
	srv.SetLLMDefault(voiceHaikuDefault(t))
	srv.listAnthropicModels = func(context.Context, string) ([]string, error) {
		return []string{"claude-haiku-5-5", "claude-haiku-4-5"}, nil
	}
	resp, err := srv.ListModels(tenantCtx(), connect.NewRequest(&managementv1.ListModelsRequest{Provider: "anthropic"}))
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if got := resp.Msg.GetModels(); len(got) == 0 || got[0] != "claude-haiku-4-5" {
		t.Errorf("models = %v, want claude-haiku-4-5 first", got)
	}
}

func llmHealth(t *testing.T, srv *VoiceServer) (entries map[string]*managementv1.ProviderHealth) {
	t.Helper()
	srv.newLister = func(string) tts.VoiceLister { return &fakeLister{} }
	srv.pingImage = func(context.Context, string) error { return nil }
	srv.botTag = func(context.Context, string) (string, error) { return "Glyphoxa#4823", nil }
	resp, err := srv.GetProviderHealth(tenantCtx(), connect.NewRequest(&managementv1.GetProviderHealthRequest{}))
	if err != nil {
		t.Fatalf("GetProviderHealth: %v", err)
	}
	entries = map[string]*managementv1.ProviderHealth{}
	for _, p := range resp.Msg.GetProviders() {
		entries[p.GetProvider()] = p
	}
	return entries
}

// An Anthropic-backed Tenant sees ANTHROPIC health, probed with its key — not
// a Groq badge for a provider its sessions never call.
func TestGetProviderHealth_AnthropicTenantShowsAnthropic(t *testing.T) {
	t.Parallel()
	cipher := voiceTestCipher(t)
	store := &fakeVoiceStore{configs: map[storage.Component]storage.ProviderConfig{
		storage.ComponentLLM: savedConfig(t, cipher, storage.ComponentLLM, "anthropic", "sk-ant-secret"),
	}}
	srv := NewVoiceServer(store, cipher, nil)
	var pinged atomic.Value
	srv.pingAnthropic = func(_ context.Context, key string) error { pinged.Store(key); return nil }
	srv.pingLLM = func(context.Context, string) error {
		t.Error("Groq was pinged for an Anthropic-backed Tenant")
		return nil
	}

	entries := llmHealth(t, srv)
	if h := entries["anthropic"]; h == nil || h.GetStatus() != managementv1.HealthStatus_HEALTH_STATUS_HEALTHY {
		t.Errorf("anthropic health = %+v, want healthy", h)
	}
	if _, ok := entries["groq"]; ok {
		t.Error("a groq health entry was reported for an Anthropic-backed Tenant")
	}
	if got, _ := pinged.Load().(string); got != "sk-ant-secret" {
		t.Errorf("ping key = %q, want the tenant's anthropic key", got)
	}
}

// A row-less Tenant's LLM health is the deployment default's provider, on the
// platform key.
func TestGetProviderHealth_NoRowProbesDeploymentDefault(t *testing.T) {
	t.Parallel()
	srv := NewVoiceServer(&fakeVoiceStore{}, nil, nil)
	srv.SetLLMDefault(voiceHaikuDefault(t))
	srv.lookupEnv = func(k string) string { return map[string]string{"ANTHROPIC_API_KEY": "platform"}[k] }
	var pings atomic.Int32
	srv.pingAnthropic = func(_ context.Context, key string) error {
		if key != "" {
			t.Errorf("ping key = %q, want \"\" (the adapter reads the platform env key)", key)
		}
		pings.Add(1)
		return nil
	}
	srv.pingLLM = func(context.Context, string) error {
		t.Error("Groq was pinged though the deployment default is Anthropic")
		return nil
	}
	entries := llmHealth(t, srv)
	if h := entries["anthropic"]; h == nil || h.GetStatus() != managementv1.HealthStatus_HEALTH_STATUS_HEALTHY || pings.Load() != 1 {
		t.Errorf("anthropic health = %+v (pings %d), want one healthy probe", h, pings.Load())
	}
	if _, ok := entries["groq"]; ok {
		t.Error("a groq health entry was reported though nothing runs on Groq")
	}
}

// No platform key for the default provider reads "no key", not a vendor error.
func TestGetProviderHealth_DefaultProviderMissingKey(t *testing.T) {
	t.Parallel()
	srv := NewVoiceServer(&fakeVoiceStore{}, nil, nil)
	srv.SetLLMDefault(voiceHaikuDefault(t))
	srv.lookupEnv = func(string) string { return "" }
	srv.pingAnthropic = func(context.Context, string) error {
		t.Error("pinged with no key anywhere")
		return nil
	}
	entries := llmHealth(t, srv)
	if h := entries["anthropic"]; h == nil || h.GetCode() != "missing_key" {
		t.Errorf("anthropic health = %+v, want degraded missing_key", h)
	}
}
