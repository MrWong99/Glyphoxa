package rpc

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"

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
	srv.lookupEnv = platformKeysSet
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
		"groq": slices.Concat(
			llmCatalog("groq", llmbuild.DeploymentDefault{}, nil),
			// The Planning chat's catalogs (ADR-0062 amendment 2026-11-22): the
			// pinned chat default and the fallback list.
			chatCatalog("groq", nil), chatFallbackCatalog("groq")),
		"anthropic": slices.Concat(
			llmCatalog("anthropic", llmbuild.DeploymentDefault{}, nil), anthropicFallbackModels,
			chatCatalog("anthropic", nil), chatFallbackCatalog("anthropic"), anthropicChatFallbackModels),
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
	srv.lookupEnv = platformKeysSet
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

// On the PLATFORM key the catalog offers only models the price table knows
// (#702 review): what the screen lists is what SaveProviderConfig accepts for a
// keyless row, and every one of them is metered at its real rate.
func TestListModels_PlatformKeyOffersOnlyPricedModels(t *testing.T) {
	t.Parallel()
	srv := NewVoiceServer(&fakeVoiceStore{}, nil, nil)
	srv.lookupEnv = func(k string) string { return map[string]string{"ANTHROPIC_API_KEY": "sk-ant-platform"}[k] }
	srv.listAnthropicModels = func(context.Context, string) ([]string, error) {
		return []string{"claude-opus-4-1", "claude-haiku-4-5-20991231", "claude-haiku-4-5", "claude-haiku-5-5", "claude-opus-4-8"}, nil
	}
	resp, err := srv.ListModels(tenantCtx(), connect.NewRequest(&managementv1.ListModelsRequest{Provider: "anthropic"}))
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	want := []string{"claude-haiku-5-5", "claude-haiku-4-5", "claude-opus-4-8"}
	if got := resp.Msg.GetModels(); !reflect.DeepEqual(got, want) {
		t.Errorf("models = %v, want only the priced ids %v", got, want)
	}
}

// A Tenant's OWN key lists the provider's whole catalog, priced or not.
func TestListModels_OwnKeyOffersTheWholeCatalog(t *testing.T) {
	t.Parallel()
	cipher := voiceTestCipher(t)
	store := &fakeVoiceStore{configs: map[storage.Component]storage.ProviderConfig{
		storage.ComponentLLM: savedConfig(t, cipher, storage.ComponentLLM, "anthropic", "sk-ant-secret"),
	}}
	srv := NewVoiceServer(store, cipher, nil)
	srv.listAnthropicModels = func(context.Context, string) ([]string, error) {
		return []string{"claude-opus-4-1", "claude-haiku-5-5"}, nil
	}
	resp, err := srv.ListModels(tenantCtx(), connect.NewRequest(&managementv1.ListModelsRequest{Provider: "anthropic"}))
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	want := []string{"claude-haiku-5-5", "claude-opus-4-1"}
	if got := resp.Msg.GetModels(); !reflect.DeepEqual(got, want) {
		t.Errorf("models = %v, want %v", got, want)
	}
}

// No key anywhere — no row, no platform key (a Groq-only deployment viewing
// its Anthropic slot) — is not a catalog OUTAGE: the provider is not called,
// nothing is logged, and the select gets the priced fallback.
func TestListModels_NoKeyAnywhereIsQuiet(t *testing.T) {
	t.Parallel()
	var logged bytes.Buffer
	srv := NewVoiceServer(&fakeVoiceStore{}, nil, slog.New(slog.NewTextHandler(&logged, nil)))
	srv.lookupEnv = func(string) string { return "" }
	srv.listAnthropicModels = func(context.Context, string) ([]string, error) {
		t.Error("the provider was called with no key to send")
		return nil, errors.New("missing API key")
	}
	resp, err := srv.ListModels(tenantCtx(), connect.NewRequest(&managementv1.ListModelsRequest{Provider: "anthropic"}))
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	want := []string{"claude-haiku-5-5", "claude-haiku-4-5"}
	if got := resp.Msg.GetModels(); !reflect.DeepEqual(got, want) {
		t.Errorf("models = %v, want the fallback %v", got, want)
	}
	if logged.Len() != 0 {
		t.Errorf("a missing key was logged as a catalog failure: %s", logged.String())
	}
}

// healthByComponent indexes a health response by what each entry is the health
// OF — the only key that is unique (#702 review).
func healthByComponent(t *testing.T, srv *VoiceServer) map[string]*managementv1.ProviderHealth {
	t.Helper()
	resp, err := srv.GetProviderHealth(tenantCtx(), connect.NewRequest(&managementv1.GetProviderHealthRequest{}))
	if err != nil {
		t.Fatalf("GetProviderHealth: %v", err)
	}
	out := map[string]*managementv1.ProviderHealth{}
	for _, p := range resp.Msg.GetProviders() {
		if _, dup := out[p.GetComponent()]; dup || p.GetComponent() == "" {
			t.Errorf("health entry %+v: component must be set and unique", p)
		}
		out[p.GetComponent()] = p
	}
	return out
}

// Gemini as the deployment's default LLM makes TWO "gemini" entries — the LLM
// on the platform key and the image slot on the Tenant's own — and they must
// stay tellable apart: a dead image key is the image entry's verdict, not
// hidden behind the LLM's.
func TestGetProviderHealth_GeminiLLMAndImageAreSeparateEntries(t *testing.T) {
	t.Parallel()
	d, err := llmbuild.DeploymentDefaultFromEnv(func(k string) string {
		return map[string]string{llmbuild.EnvDefaultProvider: "gemini", "GEMINI_API_KEY": "platform-gemini"}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	cipher := voiceTestCipher(t)
	srv := NewVoiceServer(&fakeVoiceStore{configs: map[storage.Component]storage.ProviderConfig{
		storage.ComponentImage: savedConfig(t, cipher, storage.ComponentImage, "gemini", "dead-image-key"),
	}}, cipher, nil)
	srv.SetLLMDefault(d)
	srv.lookupEnv = platformKeysSet
	srv.newLister = func(string) tts.VoiceLister { return &fakeLister{} }
	srv.botTag = func(context.Context, string) (string, error) { return "Glyphoxa#4823", nil }
	srv.pingImage = func(_ context.Context, key string) error {
		if key == "dead-image-key" {
			return errors.New("API key not valid")
		}
		return nil
	}

	entries := healthByComponent(t, srv)
	if h := entries["llm"]; h == nil || h.GetProvider() != "gemini" || h.GetStatus() != managementv1.HealthStatus_HEALTH_STATUS_HEALTHY {
		t.Errorf("llm entry = %+v, want gemini healthy (the platform key)", h)
	}
	if h := entries["image"]; h == nil || h.GetProvider() != "gemini" || h.GetStatus() != managementv1.HealthStatus_HEALTH_STATUS_DEGRADED {
		t.Errorf("image entry = %+v, want gemini degraded (the Tenant's dead key)", h)
	}
	for _, c := range []string{"tts", "discord"} {
		if entries[c] == nil {
			t.Errorf("no %q entry", c)
		}
	}
}

// A Tenant whose plan excludes the Platform Keys is told so on the LLM entry —
// even when the deployment HAS the key — and the provider is never pinged.
func TestGetProviderHealth_LLMEntitlementRefusalIsReported(t *testing.T) {
	t.Parallel()
	srv := NewVoiceServer(&fakeVoiceStore{}, nil, nil)
	srv.SetKeyEntitlement(&scriptedEnt{allowed: false})
	srv.lookupEnv = platformKeysSet
	srv.newLister = func(string) tts.VoiceLister { return &fakeLister{} }
	srv.pingImage = func(context.Context, string) error { return nil }
	srv.botTag = func(context.Context, string) (string, error) { return "Glyphoxa#4823", nil }
	srv.pingLLM = func(context.Context, string) error {
		t.Error("the LLM was pinged on a platform key the Tenant is not entitled to")
		return nil
	}
	h := healthByComponent(t, srv)["llm"]
	if h == nil || h.GetProvider() != "groq" || h.GetCode() != "plan_excludes_platform_keys" {
		t.Errorf("llm entry = %+v, want groq degraded plan_excludes_platform_keys", h)
	}
}

// A probe that times out AFTER the Tenant's LLM row was read reports the
// timeout under the provider that row names, not the deployment default's.
func TestGetProviderHealth_TimeoutNamesTheProviderInEffect(t *testing.T) {
	t.Parallel()
	cipher := voiceTestCipher(t)
	store := &fakeVoiceStore{configs: map[storage.Component]storage.ProviderConfig{
		storage.ComponentLLM: savedConfig(t, cipher, storage.ComponentLLM, "anthropic", "sk-ant-secret"),
	}}
	srv := NewVoiceServer(store, cipher, nil)
	srv.probeTimeout = 50 * time.Millisecond
	srv.newLister = func(string) tts.VoiceLister { return &fakeLister{} }
	srv.pingImage = func(context.Context, string) error { return nil }
	srv.botTag = func(context.Context, string) (string, error) { return "Glyphoxa#4823", nil }
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	// A ping that ignores its context: only the probe deadline ends the wait.
	srv.pingAnthropic = func(context.Context, string) error { <-release; return nil }

	h := healthByComponent(t, srv)["llm"]
	if h == nil || h.GetProvider() != "anthropic" || h.GetStatus() != managementv1.HealthStatus_HEALTH_STATUS_DEGRADED {
		t.Errorf("llm entry = %+v, want a degraded ANTHROPIC entry — the provider in effect", h)
	}
}
