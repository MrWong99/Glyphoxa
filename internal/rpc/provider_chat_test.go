package rpc_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// These tests pin the Planning chat model choice on the ProviderService
// (ADR-0062 amendment 2026-11-22): the truthful effective_chat_llm read, the
// three save cases, the priced-only rule on the Platform Key, and the delete
// that returns the chat to "same as voice".

const (
	keyChat     = managementv1.ChatLlmKeySource_CHAT_LLM_KEY_SOURCE_CHAT_KEY
	keyProvider = managementv1.ChatLlmKeySource_CHAT_LLM_KEY_SOURCE_PROVIDER_KEY
	keyPlatform = managementv1.ChatLlmKeySource_CHAT_LLM_KEY_SOURCE_PLATFORM
	keyNone     = managementv1.ChatLlmKeySource_CHAT_LLM_KEY_SOURCE_NONE

	srcChoice  = managementv1.ChatLlmSource_CHAT_LLM_SOURCE_CHOICE
	srcVoice   = managementv1.ChatLlmSource_CHAT_LLM_SOURCE_VOICE
	srcDefault = managementv1.ChatLlmSource_CHAT_LLM_SOURCE_PLATFORM_DEFAULT
)

// bothKeys is a deployment holding both LLM Platform Keys.
var bothKeys = map[string]string{"GROQ_API_KEY": "gsk_platform", "ANTHROPIC_API_KEY": "sk-ant-platform"}

func effectiveChat(t *testing.T, client managementv1connect.ProviderServiceClient) *managementv1.EffectiveChatLlm {
	t.Helper()
	resp, err := client.ListProviderConfigs(context.Background(), connect.NewRequest(&managementv1.ListProviderConfigsRequest{}))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	eff := resp.Msg.GetEffectiveChatLlm()
	if eff == nil {
		t.Fatal("effective_chat_llm missing")
	}
	if eff.GetProvider() == "" || eff.GetModel() == "" {
		t.Fatalf("effective_chat_llm has an empty provider or model: %+v", eff)
	}
	return eff
}

func chatOption(eff *managementv1.EffectiveChatLlm, provider string) *managementv1.ChatProviderOption {
	for _, o := range eff.GetProviders() {
		if o.GetProvider() == provider {
			return o
		}
	}
	return nil
}

func saveChat(client managementv1connect.ProviderServiceClient, provider, model, secret string) error {
	_, err := client.SaveProviderConfig(context.Background(), connect.NewRequest(&managementv1.SaveProviderConfigRequest{
		Slot: "chat", Provider: provider, Model: model, Secret: secret,
	}))
	return err
}

func mustSaveChat(t *testing.T, client managementv1connect.ProviderServiceClient, provider, model, secret string) {
	t.Helper()
	if err := saveChat(client, provider, model, secret); err != nil {
		t.Fatalf("save chat (%s, %q): %v", provider, model, err)
	}
}

func deleteChat(client managementv1connect.ProviderServiceClient, slot string) error {
	_, err := client.DeleteProviderConfig(context.Background(), connect.NewRequest(&managementv1.DeleteProviderConfigRequest{Slot: slot}))
	return err
}

// chatRowsOf returns the fake store's chat_llm rows.
func chatRowsOf(store *fakeProviderStore) []storage.ProviderConfig {
	var out []storage.ProviderConfig
	for _, c := range store.configs {
		if c.Component == storage.ComponentChatLLM {
			out = append(out, c)
		}
	}
	return out
}

// engineSelection is what the chat ENGINE would resolve for the store's rows:
// the acceptance bar is that the read names the same provider and model.
func engineSelection(store *fakeProviderStore, d llmbuild.DeploymentDefault) llmbuild.ChatLLM {
	rows, _ := store.ListProviderConfigs(context.Background(), uuid.Nil)
	return d.SelectChat(nil, rows)
}

// A Tenant with no row at all: the chat runs the deployment default, on the
// Platform Key.
func TestProviderList_EffectiveChat_PlatformDefault(t *testing.T) {
	t.Parallel()
	d := haikuDefault(t)
	store := newFakeProviderStore()
	eff := effectiveChat(t, llmServer(t, store, d, bothKeys, &fakeKeyGate{allowed: true}))

	if eff.GetProvider() != "anthropic" || eff.GetModel() != "claude-haiku-4-5" {
		t.Errorf("effective chat = %s/%s, want the deployment default anthropic/claude-haiku-4-5", eff.GetProvider(), eff.GetModel())
	}
	if eff.GetSource() != srcDefault || eff.GetKeySource() != keyPlatform {
		t.Errorf("source/key = %v/%v, want PLATFORM_DEFAULT/PLATFORM", eff.GetSource(), eff.GetKeySource())
	}
	if eff.GetStoredModel() != "" || eff.GetKeyLast4() != "" {
		t.Errorf("stored_model/key_last4 = %q/%q, want both empty", eff.GetStoredModel(), eff.GetKeyLast4())
	}
	if sel := engineSelection(store, d); sel.Provider != eff.GetProvider() || sel.Model != eff.GetModel() {
		t.Errorf("the engine resolves %s/%s, the read says %s/%s", sel.Provider, sel.Model, eff.GetProvider(), eff.GetModel())
	}
}

// No chat choice, an own voice LLM: the chat follows voice — same provider,
// same model, same key.
func TestProviderList_EffectiveChat_FollowsVoice(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	client := llmServer(t, store, haikuDefault(t), bothKeys, &fakeKeyGate{allowed: true})
	save(t, client, &managementv1.SaveProviderConfigRequest{Provider: "groq", Secret: "test-groq-own-key-9876", Model: "qwen/qwen3-32b"})

	eff := effectiveChat(t, client)
	if eff.GetProvider() != "groq" || eff.GetModel() != "qwen/qwen3-32b" {
		t.Errorf("effective chat = %s/%s, want the voice LLM groq/qwen/qwen3-32b", eff.GetProvider(), eff.GetModel())
	}
	if eff.GetSource() != srcVoice || eff.GetKeySource() != keyProvider || eff.GetKeyLast4() != "9876" {
		t.Errorf("source/key/last4 = %v/%v/%q, want VOICE/PROVIDER_KEY/9876", eff.GetSource(), eff.GetKeySource(), eff.GetKeyLast4())
	}
	voice, _ := listLLM(t, client)
	if voice.GetProvider() != eff.GetProvider() || voice.GetModel() != eff.GetModel() {
		t.Errorf("same as voice disagrees with effective_llm: %+v vs %+v", eff, voice)
	}
}

// An explicit chat choice: its provider, its model (or the provider's chat
// default), and the key the ladder lands on.
func TestProviderList_EffectiveChat_Choice(t *testing.T) {
	t.Parallel()
	d := haikuDefault(t)
	store := newFakeProviderStore()
	client := llmServer(t, store, d, bothKeys, &fakeKeyGate{allowed: true})
	// Voice on Groq with the tenant's own key; chat put on Anthropic, keyless.
	save(t, client, &managementv1.SaveProviderConfigRequest{Provider: "groq", Secret: "test-groq-own-key-9876"})
	mustSaveChat(t, client, "anthropic", "", "")

	eff := effectiveChat(t, client)
	if eff.GetProvider() != "anthropic" || eff.GetModel() != "claude-sonnet-5-5" {
		t.Errorf("effective chat = %s/%s, want anthropic/claude-sonnet-5-5 (the chat default)", eff.GetProvider(), eff.GetModel())
	}
	if eff.GetSource() != srcChoice || eff.GetKeySource() != keyPlatform || eff.GetStoredModel() != "" || eff.GetKeyLast4() != "" {
		t.Errorf("choice read = %+v, want CHOICE on the PLATFORM key with no stored model", eff)
	}
	if sel := engineSelection(store, d); sel.Provider != eff.GetProvider() || sel.Model != eff.GetModel() {
		t.Errorf("the engine resolves %s/%s, the read says %s/%s", sel.Provider, sel.Model, eff.GetProvider(), eff.GetModel())
	}

	// A named model is reported as stored and as the effective one.
	mustSaveChat(t, client, "anthropic", "claude-opus-5-5", "")
	eff = effectiveChat(t, client)
	if eff.GetModel() != "claude-opus-5-5" || eff.GetStoredModel() != "claude-opus-5-5" {
		t.Errorf("model/stored_model = %q/%q, want claude-opus-5-5 twice", eff.GetModel(), eff.GetStoredModel())
	}

	// A chat-only key is reported with ITS last four, never the voice key's.
	mustSaveChat(t, client, "anthropic", "", "test-anthropic-chat-only-4321")
	eff = effectiveChat(t, client)
	if eff.GetKeySource() != keyChat || eff.GetKeyLast4() != "4321" {
		t.Errorf("key source/last4 = %v/%q, want CHAT_KEY/4321", eff.GetKeySource(), eff.GetKeyLast4())
	}
}

// A chat choice with no key anywhere reads key_source NONE: the exchange would
// be refused, and the screen must say "needs a key" instead of naming a model
// as if it worked. It never reports another provider's key.
func TestProviderList_EffectiveChat_KeySourceNoneWhenNoKeyAnywhere(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	// A BYOK-plan Tenant (no platform entitlement) whose chat is on Anthropic
	// while its only key is a Groq one.
	store.configs["llm|groq"] = storage.ProviderConfig{Component: storage.ComponentLLM, Provider: "groq", CredentialsLast4: "9876"}
	store.configs["chat_llm|anthropic"] = storage.ProviderConfig{Component: storage.ComponentChatLLM, Provider: "anthropic", CredentialsLast4: "env"}
	eff := effectiveChat(t, llmServer(t, store, llmbuild.DeploymentDefault{}, bothKeys, &fakeKeyGate{allowed: false}))

	if eff.GetProvider() != "anthropic" || eff.GetSource() != srcChoice {
		t.Errorf("effective chat = %s (%v), want the anthropic choice", eff.GetProvider(), eff.GetSource())
	}
	if eff.GetKeySource() != keyNone || eff.GetKeyLast4() != "" {
		t.Errorf("key source/last4 = %v/%q, want NONE and no last four", eff.GetKeySource(), eff.GetKeyLast4())
	}

	// Entitled, but the deployment has no Anthropic key: NONE as well.
	onlyGroq := map[string]string{"GROQ_API_KEY": "gsk_platform"}
	eff = effectiveChat(t, llmServer(t, store, llmbuild.DeploymentDefault{}, onlyGroq, &fakeKeyGate{allowed: true}))
	if eff.GetKeySource() != keyNone {
		t.Errorf("key source = %v with no ANTHROPIC_API_KEY in the deployment, want NONE", eff.GetKeySource())
	}
}

// Every provider the chat can be put on is listed with the key it WOULD run
// on, so the screen labels each option before it is chosen.
func TestProviderList_ChatProviderOptions(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	store.configs["llm|anthropic"] = storage.ProviderConfig{Component: storage.ComponentLLM, Provider: "anthropic", CredentialsLast4: "1234"}

	t.Run("platform plan", func(t *testing.T) {
		t.Parallel()
		eff := effectiveChat(t, llmServer(t, store, llmbuild.DeploymentDefault{}, bothKeys, &fakeKeyGate{allowed: true}))
		if got := len(eff.GetProviders()); got != 2 {
			t.Fatalf("providers = %d, want groq and anthropic", got)
		}
		if eff.GetProviders()[0].GetProvider() != "groq" || eff.GetProviders()[1].GetProvider() != "anthropic" {
			t.Errorf("provider order = %v, want groq then anthropic", eff.GetProviders())
		}
		if o := chatOption(eff, "groq"); !o.GetAvailable() || o.GetKeySource() != keyPlatform || o.GetDefaultModel() != "openai/gpt-oss-120b" {
			t.Errorf("groq option = %+v, want available on the PLATFORM key, default openai/gpt-oss-120b", o)
		}
		if o := chatOption(eff, "anthropic"); !o.GetAvailable() || o.GetKeySource() != keyProvider || o.GetDefaultModel() != "claude-sonnet-5-5" {
			t.Errorf("anthropic option = %+v, want available on the tenant's PROVIDER_KEY, default claude-sonnet-5-5", o)
		}
	})

	t.Run("BYOK plan", func(t *testing.T) {
		t.Parallel()
		eff := effectiveChat(t, llmServer(t, store, llmbuild.DeploymentDefault{}, bothKeys, &fakeKeyGate{allowed: false}))
		if o := chatOption(eff, "groq"); o.GetAvailable() || o.GetKeySource() != keyNone {
			t.Errorf("groq option = %+v, want NOT available (no key, no entitlement)", o)
		}
		if o := chatOption(eff, "anthropic"); !o.GetAvailable() || o.GetKeySource() != keyProvider {
			t.Errorf("anthropic option = %+v, want available on the tenant's own key", o)
		}
	})
}

// The acceptance case: a platform-plan Tenant with no rows chooses Anthropic
// with no key and no model. One keyless row is written; the chat then runs
// claude-sonnet-5-5 and the voice LLM is untouched.
func TestProviderSaveChat_PlatformTenantPicksAnthropic(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	client := llmServer(t, store, llmbuild.DeploymentDefault{}, bothKeys, &fakeKeyGate{allowed: true})
	voiceBefore, _ := listLLM(t, client)

	mustSaveChat(t, client, "anthropic", "", "")

	rows := chatRowsOf(store)
	if len(rows) != 1 || rows[0].Provider != "anthropic" || rows[0].Model != "" || rows[0].CredentialsLast4 != "env" {
		t.Fatalf("chat rows = %+v, want one keyless anthropic row with no model", rows)
	}
	eff := effectiveChat(t, client)
	if eff.GetProvider() != "anthropic" || eff.GetModel() != "claude-sonnet-5-5" || eff.GetKeySource() != keyPlatform {
		t.Errorf("effective chat = %+v, want anthropic/claude-sonnet-5-5 on the PLATFORM key", eff)
	}
	voiceAfter, slots := listLLM(t, client)
	if voiceAfter.GetProvider() != voiceBefore.GetProvider() || voiceAfter.GetModel() != voiceBefore.GetModel() {
		t.Errorf("the voice LLM changed: %+v -> %+v", voiceBefore, voiceAfter)
	}
	if slots["anthropic"].GetInEffect() {
		t.Error("a chat choice made anthropic the voice LLM in effect")
	}
}

// On the Platform Key only a priced model may be stored: an unpriced free-text
// id is refused and nothing is written.
func TestProviderSaveChat_PlatformRefusesUnpricedModel(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	client := llmServer(t, store, llmbuild.DeploymentDefault{}, bothKeys, &fakeKeyGate{allowed: true})

	err := saveChat(client, "anthropic", "claude-fable-5-2", "")
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "no price on record") {
		t.Fatalf("err = %v, want InvalidArgument naming the missing price", err)
	}
	if len(store.chatSaves) != 0 || len(chatRowsOf(store)) != 0 {
		t.Errorf("a refused save wrote: saves=%d rows=%+v", len(store.chatSaves), chatRowsOf(store))
	}

	// The same rule holds for a model-only change on an existing keyless row.
	mustSaveChat(t, client, "anthropic", "claude-opus-5-5", "")
	if err := saveChat(client, "anthropic", "claude-fable-5-2", ""); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("model-only save of an unpriced model: err = %v, want InvalidArgument", err)
	}
	if rows := chatRowsOf(store); len(rows) != 1 || rows[0].Model != "claude-opus-5-5" {
		t.Errorf("chat rows after a refused model change = %+v, want claude-opus-5-5 kept", rows)
	}
}

// The Tenant's own key in that provider's LLM slot is borrowed: no paste, and
// free-text models are its own bill.
func TestProviderSaveChat_OwnProviderKeyAllowsFreeText(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	// No platform entitlement at all: only the tenant's own Anthropic key.
	client := llmServer(t, store, llmbuild.DeploymentDefault{}, bothKeys, &fakeKeyGate{allowed: false})
	save(t, client, &managementv1.SaveProviderConfigRequest{Provider: "anthropic", Secret: "test-anthropic-own-key-1234"})

	mustSaveChat(t, client, "anthropic", "claude-fable-5-2", "")

	rows := chatRowsOf(store)
	if len(rows) != 1 || rows[0].Model != "claude-fable-5-2" || rows[0].CredentialsLast4 != "env" {
		t.Fatalf("chat rows = %+v, want one keyless row naming claude-fable-5-2", rows)
	}
	eff := effectiveChat(t, client)
	if eff.GetKeySource() != keyProvider || eff.GetKeyLast4() != "1234" || eff.GetModel() != "claude-fable-5-2" {
		t.Errorf("effective chat = %+v, want claude-fable-5-2 on the tenant's PROVIDER_KEY ••1234", eff)
	}
}

// No key reachable for that provider: the save is refused and nothing is
// written — in particular the chat does not land on another provider's key.
func TestProviderSaveChat_NoReachableKeyRejected(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		env  map[string]string
		gate *fakeKeyGate
	}{
		"BYOK plan with only a groq key":             {bothKeys, &fakeKeyGate{allowed: false}},
		"entitled but the deployment has no key":     {map[string]string{"GROQ_API_KEY": "gsk_platform"}, &fakeKeyGate{allowed: true}},
		"entitlement read fails (fails closed)":      {bothKeys, &fakeKeyGate{err: context.DeadlineExceeded}},
		"self-host with no anthropic key anywhere":   {map[string]string{}, nil},
		"BYOK plan with an empty deployment":         {map[string]string{}, &fakeKeyGate{allowed: false}},
		"the tenant's key is for the other provider": {map[string]string{}, &fakeKeyGate{allowed: true}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := newFakeProviderStore()
			store.configs["llm|groq"] = storage.ProviderConfig{Component: storage.ComponentLLM, Provider: "groq", CredentialsLast4: "9876"}
			client := llmServer(t, store, llmbuild.DeploymentDefault{}, tc.env, tc.gate)

			err := saveChat(client, "anthropic", "", "")
			if connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("err = %v, want InvalidArgument", err)
			}
			if !strings.Contains(err.Error(), "paste a key") {
				t.Errorf("err = %v, want it to tell the GM to paste a key", err)
			}
			if len(store.chatSaves) != 0 || len(chatRowsOf(store)) != 0 {
				t.Errorf("a refused save wrote: saves=%d rows=%+v", len(store.chatSaves), chatRowsOf(store))
			}
		})
	}
}

// A pasted key is stored for the chat ALONE: sealed on the chat row, never
// returned, and the voice LLM (rows and effective_llm) is exactly as before —
// the "saving an LLM key switches voice" trap (#712 item 1) cannot happen here.
func TestProviderSaveChat_SecretStoresChatOnlyKeyAndLeavesVoiceAlone(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	client := llmServer(t, store, llmbuild.DeploymentDefault{}, map[string]string{}, &fakeKeyGate{allowed: false})
	save(t, client, &managementv1.SaveProviderConfigRequest{Provider: "groq", Secret: "test-groq-own-key-9876", Model: "qwen/qwen3-32b"})
	voiceBefore, _ := listLLM(t, client)
	llmRowBefore := store.configs["llm|groq"]

	const secret = "test-anthropic-chat-only-4321"
	resp, err := client.SaveProviderConfig(context.Background(), connect.NewRequest(&managementv1.SaveProviderConfigRequest{
		Slot: "chat", Provider: "anthropic", Secret: secret, Activate: true, // activate is ignored for chat
	}))
	if err != nil {
		t.Fatalf("save chat key: %v", err)
	}
	cred := resp.Msg.GetCredential()
	if cred.GetComponent() != "chat_llm" || cred.GetProvider() != "anthropic" || cred.GetLast4() != "4321" {
		t.Errorf("returned credential = %+v, want chat_llm/anthropic ••4321", cred)
	}

	rows := chatRowsOf(store)
	if len(rows) != 1 || rows[0].CredentialsLast4 != "4321" {
		t.Fatalf("chat rows = %+v, want one keyed row", rows)
	}
	if bytes.Contains(rows[0].CredentialsCiphertext, []byte(secret)) {
		t.Error("the chat key is stored in plaintext")
	}
	if _, ok := store.configs["llm|anthropic"]; ok {
		t.Error("a chat-only key created an anthropic VOICE row")
	}
	if got := store.configs["llm|groq"]; got.UpdatedAt != llmRowBefore.UpdatedAt || got.Model != llmRowBefore.Model {
		t.Errorf("the voice row was rewritten: %+v -> %+v", llmRowBefore, got)
	}
	voiceAfter, _ := listLLM(t, client)
	if voiceAfter.GetProvider() != voiceBefore.GetProvider() || voiceAfter.GetModel() != voiceBefore.GetModel() {
		t.Errorf("the voice LLM changed: %+v -> %+v", voiceBefore, voiceAfter)
	}

	// Write-only: nothing the read returns contains the secret.
	list, err := client.ListProviderConfigs(context.Background(), connect.NewRequest(&managementv1.ListProviderConfigsRequest{}))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if strings.Contains(list.Msg.String(), secret) {
		t.Error("the chat key is readable through ListProviderConfigs")
	}
}

// A model-only save keeps the chat-only key: the store is asked to keep it
// (KeepKey) and is never handed a ciphertext to re-upsert (#712 item 3).
func TestProviderSaveChat_ModelOnlyKeepsChatKey(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	client := llmServer(t, store, llmbuild.DeploymentDefault{}, map[string]string{}, &fakeKeyGate{allowed: false})
	mustSaveChat(t, client, "anthropic", "", "test-anthropic-chat-only-4321")
	before := chatRowsOf(store)[0]

	mustSaveChat(t, client, "anthropic", "some-future-model", "")

	after := chatRowsOf(store)[0]
	if after.Model != "some-future-model" {
		t.Errorf("model = %q, want some-future-model (free text on the tenant's own key)", after.Model)
	}
	if !bytes.Equal(after.CredentialsCiphertext, before.CredentialsCiphertext) || after.CredentialsLast4 != "4321" {
		t.Errorf("the chat key changed on a model-only save: %q", after.CredentialsLast4)
	}
	last := store.chatSaves[len(store.chatSaves)-1]
	if !last.KeepKey || len(last.Ciphertext) != 0 {
		t.Errorf("model-only save = %+v, want KeepKey and no ciphertext", last)
	}

	// Clearing the model goes back to the provider's chat default.
	mustSaveChat(t, client, "anthropic", "", "")
	if eff := effectiveChat(t, client); eff.GetModel() != "claude-sonnet-5-5" || eff.GetStoredModel() != "" || eff.GetKeySource() != keyChat {
		t.Errorf("after clearing the model: %+v, want claude-sonnet-5-5 on the CHAT_KEY", eff)
	}
}

// Whatever the sequence of saves, the Tenant has exactly one chat row.
func TestProviderSaveChat_SwitchProviderLeavesOneRow(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	client := llmServer(t, store, llmbuild.DeploymentDefault{}, bothKeys, &fakeKeyGate{allowed: true})

	for i, step := range []struct{ provider, model, secret string }{
		{"groq", "", ""},
		{"anthropic", "claude-opus-5-5", ""},
		{"groq", "", "test-groq-chat-only-7777"},
		{"anthropic", "", ""},
		{"anthropic", "", "test-anthropic-chat-only-4321"},
		{"groq", "openai/gpt-oss-20b", ""},
	} {
		mustSaveChat(t, client, step.provider, step.model, step.secret)
		rows := chatRowsOf(store)
		if len(rows) != 1 || rows[0].Provider != step.provider {
			t.Fatalf("step %d: chat rows = %+v, want exactly one on %s", i, rows, step.provider)
		}
	}
	// The last switch dropped the Anthropic chat-only key with its row.
	if eff := effectiveChat(t, client); eff.GetProvider() != "groq" || eff.GetKeySource() != keyPlatform {
		t.Errorf("effective chat = %+v, want groq on the PLATFORM key", eff)
	}
}

func TestProviderSaveChat_UnknownProviderRejected(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	client := llmServer(t, store, llmbuild.DeploymentDefault{}, bothKeys, &fakeKeyGate{allowed: true})

	for _, provider := range []string{"", "gemini", "elevenlabs", "openai"} {
		for _, secret := range []string{"", "test-real-looking-key-1234"} {
			if err := saveChat(client, provider, "", secret); connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Errorf("provider %q (secret set: %v): err = %v, want InvalidArgument", provider, secret != "", err)
			}
		}
	}
	// An unknown slot is refused too, rather than read as a provider key save.
	_, err := client.SaveProviderConfig(context.Background(), connect.NewRequest(&managementv1.SaveProviderConfigRequest{
		Slot: "voice", Provider: "groq", Secret: "test-real-looking-key-1234",
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("unknown slot: err = %v, want InvalidArgument", err)
	}
	if len(store.chatSaves) != 0 || len(store.configs) != 0 {
		t.Errorf("refused saves wrote: %+v", store.configs)
	}
}

// A secret whose last four are the keyless placeholder would be stored as a
// "keyless" row holding a free-text model — the priced-only bypass. Refused.
func TestProviderSaveChat_PlaceholderShapedSecretRejected(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	client := llmServer(t, store, llmbuild.DeploymentDefault{}, bothKeys, &fakeKeyGate{allowed: true})

	if err := saveChat(client, "anthropic", "claude-fable-5-2", "env"); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("err = %v, want InvalidArgument", err)
	}
	if len(store.chatSaves) != 0 {
		t.Errorf("a placeholder-shaped secret was saved: %+v", store.chatSaves)
	}
}

// Saving a chat key needs the credential cipher; a keyless choice does not.
func TestProviderSaveChat_NoCipher(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	srv := rpc.NewProviderServer(store, nil, nil)
	srv.SetEnvLookupForTest(func(k string) string { return bothKeys[k] })
	client, _ := clientForServer(t, srv)

	if err := saveChat(client, "anthropic", "", "test-anthropic-chat-only-4321"); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("keyed save without a cipher: err = %v, want FailedPrecondition", err)
	}
	if err := saveChat(client, "anthropic", "", ""); err != nil {
		t.Errorf("keyless save without a cipher: %v", err)
	}
}

// The pre-amendment client saved the chat as slot "chat", provider "groq",
// with a pasted key. That request still works and still means the same thing.
func TestProviderSaveChat_LegacyClientGroqWithSecretStillWorks(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	client := llmServer(t, store, llmbuild.DeploymentDefault{}, map[string]string{}, &fakeKeyGate{allowed: false})

	mustSaveChat(t, client, "groq", "openai/gpt-oss-120b", "test-groq-legacy-chat-key-5555")

	eff := effectiveChat(t, client)
	if eff.GetProvider() != "groq" || eff.GetModel() != "openai/gpt-oss-120b" || eff.GetKeySource() != keyChat || eff.GetKeyLast4() != "5555" {
		t.Errorf("effective chat = %+v, want groq/openai/gpt-oss-120b on the CHAT_KEY ••5555", eff)
	}
}

// A chat save or delete leaves the provider-health cache alone: the health
// read has no chat entry, and its LLM entry is the voice LLM, which a chat
// choice never changes. Busting it would re-probe every vendor for nothing.
func TestProviderSaveChat_KeepsHealthCache(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	srv := rpc.NewProviderServer(store, testCipher(t), nil)
	srv.SetEnvLookupForTest(func(k string) string { return bothKeys[k] })
	busts := 0
	srv.SetHealthInvalidator(func(uuid.UUID) { busts++ })
	client, _ := clientForServer(t, srv)

	mustSaveChat(t, client, "anthropic", "", "test-anthropic-chat-only-4321")
	if err := deleteChat(client, "chat"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if busts != 0 {
		t.Errorf("health busts after a chat save and delete = %d, want 0", busts)
	}
}

// Delete returns the Tenant to "same as voice".
func TestProviderDeleteChat_BackToSameAsVoice(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	client := llmServer(t, store, llmbuild.DeploymentDefault{}, bothKeys, &fakeKeyGate{allowed: true})
	save(t, client, &managementv1.SaveProviderConfigRequest{Provider: "groq", Secret: "test-groq-own-key-9876", Model: "qwen/qwen3-32b"})
	mustSaveChat(t, client, "anthropic", "claude-opus-5-5", "test-anthropic-chat-only-4321")

	if err := deleteChat(client, "chat"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if rows := chatRowsOf(store); len(rows) != 0 {
		t.Errorf("chat rows left: %+v", rows)
	}
	if _, ok := store.configs["llm|groq"]; !ok {
		t.Error("the delete removed the voice row")
	}
	eff := effectiveChat(t, client)
	if eff.GetSource() != srcVoice || eff.GetProvider() != "groq" || eff.GetModel() != "qwen/qwen3-32b" {
		t.Errorf("effective chat after delete = %+v, want VOICE groq/qwen/qwen3-32b", eff)
	}
}

func TestProviderDeleteChat_Idempotent(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	client, _ := newProviderClient(t, store, testCipher(t))

	for i := range 2 {
		if err := deleteChat(client, "chat"); err != nil {
			t.Fatalf("delete %d with nothing stored: %v", i, err)
		}
	}
	if store.chatDeletes != 2 {
		t.Errorf("store deletes = %d, want 2", store.chatDeletes)
	}
}

func TestProviderDelete_NonChatSlotRejected(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	store.configs["llm|groq"] = storage.ProviderConfig{Component: storage.ComponentLLM, Provider: "groq", CredentialsLast4: "9876"}
	client, _ := newProviderClient(t, store, testCipher(t))

	// "groq", "anthropic" and "voice_llm" are deletable voice LLM slots since
	// the ADR-0004 amendment of 2026-11-26 (provider_usage_test.go).
	for _, slot := range []string{"", "llm", "elevenlabs", "gemini", "discord"} {
		if err := deleteChat(client, slot); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("slot %q: err = %v, want InvalidArgument", slot, err)
		}
	}
	if store.chatDeletes != 0 || len(store.configs) != 1 {
		t.Errorf("a refused delete reached the store: deletes=%d configs=%+v", store.chatDeletes, store.configs)
	}
}
