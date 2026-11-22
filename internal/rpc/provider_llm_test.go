package rpc_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
)

// haikuDefault is a deployment whose default LLM is Claude Haiku 4.5 (#702).
func haikuDefault(t *testing.T) llmbuild.DeploymentDefault {
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

// llmServer builds a ProviderServer for the LLM-slot tests: d is the deployment
// default, env the Platform Keys that exist, gate the entitlement (nil = all).
func llmServer(t *testing.T, store *fakeProviderStore, d llmbuild.DeploymentDefault, env map[string]string, gate *fakeKeyGate) managementv1connect.ProviderServiceClient {
	t.Helper()
	srv := rpc.NewProviderServer(store, testCipher(t), nil)
	srv.SetLLMDefault(d)
	srv.SetEnvLookupForTest(func(k string) string { return env[k] })
	if gate != nil {
		srv.SetKeyEntitlement(gate)
	}
	client, _ := clientForServer(t, srv)
	return client
}

func listLLM(t *testing.T, client managementv1connect.ProviderServiceClient) (*managementv1.EffectiveLlm, map[string]*managementv1.ProviderCredential) {
	t.Helper()
	resp, err := client.ListProviderConfigs(context.Background(), connect.NewRequest(&managementv1.ListProviderConfigsRequest{}))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	slots := map[string]*managementv1.ProviderCredential{}
	for _, c := range resp.Msg.GetCredentials() {
		if c.GetComponent() == "llm" {
			slots[c.GetProvider()] = c
		}
	}
	return resp.Msg.GetEffectiveLlm(), slots
}

func save(t *testing.T, client managementv1connect.ProviderServiceClient, req *managementv1.SaveProviderConfigRequest) *managementv1.ProviderCredential {
	t.Helper()
	resp, err := client.SaveProviderConfig(context.Background(), connect.NewRequest(req))
	if err != nil {
		t.Fatalf("Save(%s): %v", req.GetProvider(), err)
	}
	return resp.Msg.GetCredential()
}

// A Tenant with no LLM row runs the platform default; unset envs mean Groq.
func TestProviderList_EffectiveLLM_CodeDefault(t *testing.T) {
	t.Parallel()
	eff, slots := listLLM(t, llmServer(t, newFakeProviderStore(), llmbuild.DeploymentDefault{}, nil, nil))
	if eff.GetProvider() != "groq" || eff.GetModel() != "openai/gpt-oss-120b" || !eff.GetPlatformDefault() {
		t.Errorf("effective_llm = %+v, want groq / openai/gpt-oss-120b / platform default", eff)
	}
	if slots["groq"] == nil || slots["anthropic"] == nil {
		t.Fatalf("LLM slots = %v, want groq and anthropic", slots)
	}
	if !slots["groq"].GetInEffect() || slots["anthropic"].GetInEffect() {
		t.Errorf("in_effect: groq=%v anthropic=%v, want groq only", slots["groq"].GetInEffect(), slots["anthropic"].GetInEffect())
	}
}

// With a deployment default the row-less Tenant sees THAT provider in effect.
func TestProviderList_EffectiveLLM_DeploymentDefault(t *testing.T) {
	t.Parallel()
	eff, slots := listLLM(t, llmServer(t, newFakeProviderStore(), haikuDefault(t), map[string]string{"ANTHROPIC_API_KEY": "k"}, nil))
	if eff.GetProvider() != "anthropic" || eff.GetModel() != "claude-haiku-4-5" || !eff.GetPlatformDefault() {
		t.Errorf("effective_llm = %+v, want anthropic / claude-haiku-4-5 / platform default", eff)
	}
	if !slots["anthropic"].GetInEffect() || slots["groq"].GetInEffect() {
		t.Errorf("in_effect: groq=%v anthropic=%v, want anthropic only", slots["groq"].GetInEffect(), slots["anthropic"].GetInEffect())
	}
	if !slots["anthropic"].GetProvidedByPlatform() {
		t.Error("anthropic slot: provided_by_platform = false with ANTHROPIC_API_KEY set and no gate")
	}
}

// Saving an Anthropic key stores it write-only in its own slot and puts the
// Tenant on it; the Groq slot is untouched.
func TestProviderSave_AnthropicSlot(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	client := llmServer(t, store, llmbuild.DeploymentDefault{}, nil, nil)
	save(t, client, &managementv1.SaveProviderConfigRequest{Provider: "groq", Secret: "gsk-groq-secret-1111", Model: "openai/gpt-oss-20b"})
	cred := save(t, client, &managementv1.SaveProviderConfigRequest{Provider: "anthropic", Secret: "sk-ant-api-secret-9999", Model: "claude-haiku-5-5"})
	if cred.GetProvider() != "anthropic" || cred.GetComponent() != "llm" || cred.GetLast4() != "9999" || !cred.GetEverSaved() {
		t.Errorf("saved credential = %+v", cred)
	}
	row, ok := store.configs["llm|anthropic"]
	if !ok {
		t.Fatal("no (llm, anthropic) row stored")
	}
	if string(row.CredentialsCiphertext) == "sk-ant-api-secret-9999" || len(row.CredentialsCiphertext) == 0 {
		t.Error("the anthropic key must be sealed at rest")
	}

	eff, slots := listLLM(t, client)
	if eff.GetProvider() != "anthropic" || eff.GetModel() != "claude-haiku-5-5" || eff.GetPlatformDefault() {
		t.Errorf("effective_llm = %+v, want the Tenant's anthropic / claude-haiku-5-5", eff)
	}
	if slots["groq"].GetLast4() != "1111" || slots["groq"].GetModel() != "openai/gpt-oss-20b" || slots["groq"].GetInEffect() {
		t.Errorf("groq slot = %+v, want its own key and model, not in effect", slots["groq"])
	}
	if slots["anthropic"].GetLast4() != "9999" || !slots["anthropic"].GetInEffect() {
		t.Errorf("anthropic slot = %+v, want last4 9999, in effect", slots["anthropic"])
	}
}

// A model-only save reaches the slot it names even when another LLM slot was
// saved more recently, and an Anthropic row with no model shows the Haiku
// voice default, not the adapter's flagship.
func TestProviderSave_ModelOnlyTargetsItsOwnSlot(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	client := llmServer(t, store, llmbuild.DeploymentDefault{}, nil, nil)
	save(t, client, &managementv1.SaveProviderConfigRequest{Provider: "anthropic", Secret: "sk-ant-api-secret-9999"})
	if eff, _ := listLLM(t, client); eff.GetModel() != "claude-haiku-5-5" {
		t.Errorf("model-less anthropic row: effective model = %q, want claude-haiku-5-5", eff.GetModel())
	}
	save(t, client, &managementv1.SaveProviderConfigRequest{Provider: "groq", Secret: "gsk-groq-secret-1111"})

	cred := save(t, client, &managementv1.SaveProviderConfigRequest{Provider: "anthropic", Model: "claude-haiku-4-5"})
	if cred.GetModel() != "claude-haiku-4-5" || cred.GetLast4() != "9999" {
		t.Errorf("model-only save on anthropic = %+v, want model claude-haiku-4-5 on the kept key", cred)
	}
	if got := store.configs["llm|groq"].Model; got != "" {
		t.Errorf("groq row model = %q, want it untouched", got)
	}
}

// activate switches the LLM in effect without re-entering a key.
func TestProviderSave_ActivateSwitchesLLMInEffect(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	client := llmServer(t, store, llmbuild.DeploymentDefault{}, nil, nil)
	save(t, client, &managementv1.SaveProviderConfigRequest{Provider: "groq", Secret: "gsk-groq-secret-1111", Model: "openai/gpt-oss-20b"})
	save(t, client, &managementv1.SaveProviderConfigRequest{Provider: "anthropic", Secret: "sk-ant-api-secret-9999", Model: "claude-haiku-4-5"})
	sealedBefore := string(store.configs["llm|groq"].CredentialsCiphertext)

	save(t, client, &managementv1.SaveProviderConfigRequest{Provider: "groq", Activate: true})

	eff, slots := listLLM(t, client)
	if eff.GetProvider() != "groq" || eff.GetModel() != "openai/gpt-oss-20b" || eff.GetPlatformDefault() {
		t.Errorf("effective_llm = %+v, want the Tenant's groq / openai/gpt-oss-20b", eff)
	}
	if !slots["groq"].GetInEffect() || slots["anthropic"].GetInEffect() {
		t.Error("in_effect did not move to groq")
	}
	if got := store.configs["llm|groq"]; string(got.CredentialsCiphertext) != sealedBefore || got.CredentialsLast4 != "1111" {
		t.Error("activate must leave the sealed key untouched")
	}
	if got := store.configs["llm|anthropic"]; got.CredentialsLast4 != "9999" || got.Model != "claude-haiku-4-5" {
		t.Errorf("the other slot's row changed: %+v", got)
	}
}

// A Tenant on the Platform Keys can choose a provider it never saved a key
// for: the choice is a keyless row that keeps riding the platform key.
func TestProviderSave_ActivatePlatformProvidedSlot(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	env := map[string]string{"GROQ_API_KEY": "gsk", "ANTHROPIC_API_KEY": "sk-ant"}
	client := llmServer(t, store, llmbuild.DeploymentDefault{}, env, &fakeKeyGate{allowed: true})

	cred := save(t, client, &managementv1.SaveProviderConfigRequest{Provider: "anthropic", Activate: true, Model: "claude-haiku-4-5"})
	if cred.GetEverSaved() || cred.GetLast4() != "" {
		t.Errorf("a keyless choice must not read as a saved key: %+v", cred)
	}
	row := store.configs["llm|anthropic"]
	if row.CredentialsLast4 != llmbuild.EnvPlaceholderLast4 {
		t.Errorf("row last4 = %q, want the env placeholder (the adapter rides the platform key)", row.CredentialsLast4)
	}

	eff, slots := listLLM(t, client)
	if eff.GetProvider() != "anthropic" || eff.GetModel() != "claude-haiku-4-5" || eff.GetPlatformDefault() {
		t.Errorf("effective_llm = %+v, want the Tenant's own choice anthropic / claude-haiku-4-5", eff)
	}
	if !slots["anthropic"].GetInEffect() || !slots["anthropic"].GetProvidedByPlatform() {
		t.Errorf("anthropic slot = %+v, want in effect and still provided by the platform", slots["anthropic"])
	}
}

// Without a saved key and without a platform key to ride there is nothing to
// activate: no row is written.
func TestProviderSave_ActivateNeedsAKeyFromSomewhere(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		env  map[string]string
		gate *fakeKeyGate
	}{
		"deployment has no anthropic key": {map[string]string{"GROQ_API_KEY": "gsk"}, &fakeKeyGate{allowed: true}},
		"BYOK plan tenant":                {map[string]string{"ANTHROPIC_API_KEY": "sk-ant"}, &fakeKeyGate{allowed: false}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := newFakeProviderStore()
			client := llmServer(t, store, llmbuild.DeploymentDefault{}, tc.env, tc.gate)
			_, err := client.SaveProviderConfig(context.Background(), connect.NewRequest(&managementv1.SaveProviderConfigRequest{Provider: "anthropic", Activate: true}))
			if connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("err = %v, want CodeInvalidArgument", err)
			}
			if len(store.configs) != 0 {
				t.Errorf("a refused activate wrote rows: %v", store.configs)
			}
		})
	}
}

// activate is an LLM-slot notion: on any other slot it creates nothing.
func TestProviderSave_ActivateIgnoredOffLLMSlots(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	client := llmServer(t, store, llmbuild.DeploymentDefault{}, map[string]string{"ELEVENLABS_API_KEY": "el"}, nil)
	_, err := client.SaveProviderConfig(context.Background(), connect.NewRequest(&managementv1.SaveProviderConfigRequest{Provider: "elevenlabs", Activate: true}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument || len(store.configs) != 0 {
		t.Errorf("err = %v, rows = %d; want CodeInvalidArgument and no rows", err, len(store.configs))
	}
}

// Switching the LLM in effect changes which provider the health probe reports,
// so the cached verdict (for the OTHER provider) must go with it.
func TestProviderSave_ActivateBustsHealthCache(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	srv := rpc.NewProviderServer(store, testCipher(t), nil)
	busted := 0
	srv.SetHealthInvalidator(func(uuid.UUID) { busted++ })
	client, _ := clientForServer(t, srv)
	save(t, client, &managementv1.SaveProviderConfigRequest{Provider: "groq", Secret: "gsk-groq-secret-1111"})
	save(t, client, &managementv1.SaveProviderConfigRequest{Provider: "anthropic", Secret: "sk-ant-api-secret-9999"})
	before := busted

	save(t, client, &managementv1.SaveProviderConfigRequest{Provider: "groq", Activate: true})
	if busted != before+1 {
		t.Errorf("health cache busted %d times on activate, want 1", busted-before)
	}
	// A plain model-only save on the slot already in effect keeps the verdict:
	// neither the key nor the provider changed.
	save(t, client, &managementv1.SaveProviderConfigRequest{Provider: "groq", Model: "openai/gpt-oss-20b"})
	if busted != before+1 {
		t.Errorf("a model-only save busted the health cache (%d extra)", busted-before-1)
	}
}
