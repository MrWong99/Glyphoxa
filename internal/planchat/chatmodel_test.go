package planchat

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/billing"
	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/anthropic"
)

// These tests pin the Planning chat model choice (ADR-0062 amendment
// 2026-11-22) through a whole Exchange: which adapter is built, with which
// key, for which model and reply cap.

// builtProvider records what the engine asked the provider factory for.
type builtProvider struct {
	id, key string
	calls   int
}

// chatEngine builds an engine over st whose factory hands out spy and records
// its arguments in the returned builtProvider.
func chatEngine(st *fakeStore, cipher *crypto.Cipher, spy *modelSpy, opts ...Option) (*Engine, *builtProvider) {
	built := &builtProvider{}
	opts = append(opts, WithProviderFactory(func(providerID, apiKey string) (llm.Provider, error) {
		built.id, built.key = providerID, apiKey
		built.calls++
		return spy, nil
	}))
	return NewEngine(st, cipher, &fakeSearcher{}, &fakeWriter{}, fakeRecapper{}, billing.NewOffSession(st, nil, nil), nil, nil, opts...), built
}

func oneReply() *modelSpy {
	return &modelSpy{scriptedProvider: scriptedProvider{scripts: [][]llm.StreamEvent{textDone("A plan.")}}}
}

func testCipher(t *testing.T) *crypto.Cipher {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand key: %v", err)
	}
	c, err := crypto.New(key)
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	return c
}

// sealed builds a Provider Config holding plaintext sealed under cipher.
func sealed(t *testing.T, cipher *crypto.Cipher, provider, model, plaintext string) storage.ProviderConfig {
	t.Helper()
	ct, err := cipher.Seal([]byte(plaintext))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return storage.ProviderConfig{
		ID: uuid.New(), Provider: provider, Model: model,
		CredentialsCiphertext: ct, CredentialsLast4: crypto.Last4(plaintext),
	}
}

// denyPlatformKey is a BYOK-plan Tenant: no platform-key entitlement.
type denyPlatformKey struct{}

func (denyPlatformKey) PlatformKeyAllowed(context.Context, uuid.UUID) (bool, error) {
	return false, nil
}

func exchange(t *testing.T, eng *Engine, st *fakeStore) error {
	t.Helper()
	var ev collectEvents
	_, err := eng.Exchange(context.Background(), uuid.New(), storage.Campaign{ID: st.butler.CampaignID}, st.thread.ID, "Plan it", &ev)
	return err
}

// The acceptance case: a Tenant with no key and no model chooses Anthropic for
// chat while its voice stays on Groq. The exchange runs claude-sonnet-5-5 with
// a 16000-token reply cap on the Platform Key.
func TestExchange_ChatChoiceRunsAnthropicWhileVoiceStaysGroq(t *testing.T) {
	st := newFakeStore(uuid.New())
	st.tenant[storage.ComponentLLM] = storage.ProviderConfig{Provider: "groq", Model: "openai/gpt-oss-120b", CredentialsLast4: "env"}
	st.tenant[storage.ComponentChatLLM] = storage.ProviderConfig{Provider: "anthropic", CredentialsLast4: "env"}
	spy := oneReply()
	eng, built := chatEngine(st, nil, spy)

	if err := exchange(t, eng, st); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if built.id != "anthropic" || built.key != "" {
		t.Errorf("built provider %q with key %q, want anthropic on the platform key", built.id, built.key)
	}
	if len(spy.models) != 1 || spy.models[0] != "claude-sonnet-5-5" {
		t.Errorf("request models = %v, want claude-sonnet-5-5", spy.models)
	}
	if spy.maxTokens[0] != 16000 {
		t.Errorf("max tokens = %d, want 16000 on Anthropic", spy.maxTokens[0])
	}
}

// A keyless chat choice borrows the Tenant's key in that provider's LLM slot:
// nobody pastes the same key twice.
func TestExchange_ChatChoiceBorrowsTheProviderSlotKey(t *testing.T) {
	cipher := testCipher(t)
	st := newFakeStore(uuid.New())
	st.tenant[storage.ComponentChatLLM] = storage.ProviderConfig{Provider: "anthropic", CredentialsLast4: "env"}
	// The Tenant's voice runs Groq (the newest llm row); its Anthropic key sits
	// in the other LLM slot.
	anthropicKey := sealed(t, cipher, "anthropic", "", "sk-ant-tenant-key-1234")
	anthropicKey.Component = storage.ComponentLLM
	groqKey := sealed(t, cipher, "groq", "", "gsk-tenant-key-9999")
	groqKey.Component = storage.ComponentLLM
	groqKey.UpdatedAt = anthropicKey.UpdatedAt.AddDate(0, 0, 1)
	st.rows = []storage.ProviderConfig{anthropicKey, groqKey}
	spy := oneReply()
	// Even a Tenant with NO platform entitlement runs: the key is its own.
	eng, built := chatEngine(st, cipher, spy, WithKeyEntitlement(denyPlatformKey{}))

	if err := exchange(t, eng, st); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if built.id != "anthropic" || built.key != "sk-ant-tenant-key-1234" {
		t.Errorf("built %q with key %q, want anthropic with the tenant's anthropic key", built.id, built.key)
	}
}

// A key saved on the chat row itself wins over the provider slot's key.
func TestExchange_ChatOwnKeyWins(t *testing.T) {
	cipher := testCipher(t)
	st := newFakeStore(uuid.New())
	st.tenant[storage.ComponentChatLLM] = sealed(t, cipher, "anthropic", "claude-opus-5-5", "sk-ant-chat-only-5678")
	st.tenant[storage.ComponentLLM] = sealed(t, cipher, "anthropic", "", "sk-ant-voice-key-1234")
	spy := oneReply()
	eng, built := chatEngine(st, cipher, spy, WithKeyEntitlement(denyPlatformKey{}))

	if err := exchange(t, eng, st); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if built.key != "sk-ant-chat-only-5678" {
		t.Errorf("key = %q, want the chat-only key", built.key)
	}
	if spy.models[0] != "claude-opus-5-5" {
		t.Errorf("model = %q, want the chosen claude-opus-5-5", spy.models[0])
	}
}

// A chat choice never falls back to another provider. With no key of its own,
// none in that provider's slot and no platform entitlement, the exchange is
// refused with ErrNoPlatformKeyEntitlement — even though the Tenant holds a
// perfectly good Groq key for voice — and nothing is persisted or built.
func TestExchange_ChatChoiceOnPlatformKeyIsEntitlementGated(t *testing.T) {
	cipher := testCipher(t)
	st := newFakeStore(uuid.New())
	st.tenant[storage.ComponentChatLLM] = storage.ProviderConfig{Provider: "anthropic", CredentialsLast4: "env"}
	st.tenant[storage.ComponentLLM] = sealed(t, cipher, "groq", "", "gsk-tenant-key-9999")
	spy := oneReply()
	eng, built := chatEngine(st, cipher, spy, WithKeyEntitlement(denyPlatformKey{}))

	err := exchange(t, eng, st)
	if !errors.Is(err, llmbuild.ErrNoPlatformKeyEntitlement) {
		t.Fatalf("err = %v, want ErrNoPlatformKeyEntitlement", err)
	}
	if built.calls != 0 || spy.calls != 0 {
		t.Errorf("a provider was built (%d) or called (%d) for a refused exchange", built.calls, spy.calls)
	}
	if len(st.messages) != 0 {
		t.Errorf("refused exchange persisted %d messages", len(st.messages))
	}
}

// The priced-only rule of the Platform Key is checked again at call time, not
// only when the choice is saved. A keyless chat choice may hold a free-text
// model that was accepted against the Tenant's own key in that provider's LLM
// slot; if that key is gone by the time the chat runs (a stale voice write can
// turn the row keyless), the choice resolves to the Platform Key, where an
// unpriced model would be metered at the flat fallback rate. The exchange is
// refused before anything spends or persists.
func TestExchange_UnpricedChatChoiceOnThePlatformKeyIsRefused(t *testing.T) {
	st := newFakeStore(uuid.New())
	st.tenant[storage.ComponentChatLLM] = storage.ProviderConfig{Provider: "anthropic", Model: "claude-not-priced-9", CredentialsLast4: "env"}
	// The voice slot of the same provider no longer holds a key of its own.
	st.tenant[storage.ComponentLLM] = storage.ProviderConfig{Provider: "anthropic", CredentialsLast4: "env"}
	spy := oneReply()
	eng, built := chatEngine(st, nil, spy)

	err := exchange(t, eng, st)
	if !errors.Is(err, ErrUnpricedOnPlatformKey) {
		t.Fatalf("err = %v, want ErrUnpricedOnPlatformKey", err)
	}
	if built.calls != 0 || spy.calls != 0 {
		t.Errorf("a provider was built (%d) or called (%d) for a refused exchange", built.calls, spy.calls)
	}
	if len(st.messages) != 0 {
		t.Errorf("refused exchange persisted %d messages", len(st.messages))
	}
}

// The same free-text model on the Tenant's OWN key is the Tenant's business:
// the call-time check only guards the Platform Key.
func TestExchange_UnpricedChatChoiceOnTheTenantsOwnKeyRuns(t *testing.T) {
	cipher := testCipher(t)
	st := newFakeStore(uuid.New())
	st.tenant[storage.ComponentChatLLM] = storage.ProviderConfig{Provider: "anthropic", Model: "claude-not-priced-9", CredentialsLast4: "env"}
	st.tenant[storage.ComponentLLM] = sealed(t, cipher, "anthropic", "", "test-anthropic-own-key-5151")
	spy := oneReply()
	eng, built := chatEngine(st, cipher, spy)

	if err := exchange(t, eng, st); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if built.key != "test-anthropic-own-key-5151" || spy.models[0] != "claude-not-priced-9" {
		t.Errorf("built with key %q, model %q; want the Tenant's own key and the free-text model", built.key, spy.models[0])
	}
}

// No chat row: the chat follows voice — its provider, its model, its key — and
// keeps the 4096-token cap off Anthropic.
func TestExchange_NoChatRowFollowsVoice(t *testing.T) {
	cipher := testCipher(t)
	st := newFakeStore(uuid.New())
	st.tenant[storage.ComponentLLM] = sealed(t, cipher, "groq", "qwen/qwen3-32b", "gsk-tenant-key-9999")
	spy := oneReply()
	eng, built := chatEngine(st, cipher, spy)

	if err := exchange(t, eng, st); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if built.id != "groq" || built.key != "gsk-tenant-key-9999" {
		t.Errorf("built %q with key %q, want groq with the voice key", built.id, built.key)
	}
	if spy.models[0] != "qwen/qwen3-32b" || spy.maxTokens[0] != 4096 {
		t.Errorf("request = %s / %d tokens, want qwen/qwen3-32b / 4096", spy.models[0], spy.maxTokens[0])
	}
}

// "Same as voice" on Anthropic also gets the Anthropic reply cap: the cap
// follows the provider that answers, not the rung that picked it.
func TestExchange_SameAsVoiceOnAnthropicGetsTheAnthropicCap(t *testing.T) {
	st := newFakeStore(uuid.New())
	st.tenant[storage.ComponentLLM] = storage.ProviderConfig{Provider: "anthropic", CredentialsLast4: "env"}
	spy := oneReply()
	eng, _ := chatEngine(st, nil, spy)

	if err := exchange(t, eng, st); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if spy.models[0] != "claude-haiku-5-5" || spy.maxTokens[0] != 16000 {
		t.Errorf("request = %s / %d tokens, want the voice model claude-haiku-5-5 / 16000", spy.models[0], spy.maxTokens[0])
	}
}

// The Usage Ledger row carries the chat provider and model, so the exchange is
// priced on what actually answered — not on the voice model.
func TestExchange_UsageIsPricedOnTheChatModel(t *testing.T) {
	st := newFakeStore(uuid.New())
	st.tenant[storage.ComponentLLM] = storage.ProviderConfig{Provider: "groq", CredentialsLast4: "env"}
	st.tenant[storage.ComponentChatLLM] = storage.ProviderConfig{Provider: "anthropic", Model: "claude-opus-5-5", CredentialsLast4: "env"}
	eng, _ := chatEngine(st, nil, oneReply())

	if err := exchange(t, eng, st); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if len(st.usage) != 1 {
		t.Fatalf("usage rows = %+v, want one", st.usage)
	}
	row := st.usage[0]
	if row.Provider != "anthropic" || row.Model != "claude-opus-5-5" {
		t.Errorf("ledger row = %s/%s, want anthropic/claude-opus-5-5", row.Provider, row.Model)
	}
	// 10 input + 5 output tokens at $4 / $20 per MTok.
	if want := (10*4.0 + 5*20.0) / 1e6; row.EstimatedUSD < want*0.999 || row.EstimatedUSD > want*1.001 {
		t.Errorf("estimated USD = %g, want %g (the claude-opus-5-5 rate)", row.EstimatedUSD, want)
	}
}

// The usage log line names what answered and why, so a pod log shows the
// provider, the model, the rung and the key an exchange ran on.
func TestExchange_UsageLogNamesProviderModelAndSources(t *testing.T) {
	var logs bytes.Buffer
	st := newFakeStore(uuid.New())
	st.tenant[storage.ComponentChatLLM] = storage.ProviderConfig{Provider: "anthropic", CredentialsLast4: "env"}
	spy := oneReply()
	eng := NewEngine(st, nil, &fakeSearcher{}, &fakeWriter{}, fakeRecapper{}, billing.NewOffSession(st, nil, nil), nil,
		slog.New(slog.NewTextHandler(&logs, nil)),
		WithProviderFactory(func(string, string) (llm.Provider, error) { return spy, nil }))

	if err := exchange(t, eng, st); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	var line string
	for _, l := range strings.Split(logs.String(), "\n") {
		if strings.Contains(l, "planchat: llm usage") {
			line = l
		}
	}
	for _, want := range []string{"provider=anthropic", "model=claude-sonnet-5-5", "llm_source=choice", "key_source=platform"} {
		if !strings.Contains(line, want) {
			t.Errorf("usage log line lacks %q: %s", want, line)
		}
	}
}

// An engine built without a factory uses the CHAT one (llmbuild.NewChat): its
// Anthropic client thinks at effort medium on the chat default. Swapping the
// default back to llmbuild.New would send Sonnet no thinking config at all and
// leave the 16000-token cap without the policy it was sized for.
func TestNewEngine_DefaultFactoryIsTheChatFactory(t *testing.T) {
	st := newFakeStore(uuid.New())
	eng := NewEngine(st, nil, &fakeSearcher{}, &fakeWriter{}, fakeRecapper{}, billing.NewOffSession(st, nil, nil), nil, nil)

	p, err := eng.newProvider(anthropic.ProviderID, "k")
	if err != nil {
		t.Fatalf("build anthropic: %v", err)
	}
	client, ok := p.(*anthropic.Client)
	if !ok {
		t.Fatalf("anthropic provider is %T, want *anthropic.Client", p)
	}
	want := anthropic.Thinking{Mode: anthropic.ThinkingAdaptive, Effort: "medium"}
	if got := client.ThinkingFor(anthropic.ChatModel); got != want {
		t.Errorf("thinking for %s = %+v, want %+v", anthropic.ChatModel, got, want)
	}
}

// The default factory builds the provider the choice names. Pinned through the
// missing-key refusal of the real adapter (no network), which names the
// provider that was built.
func TestNewEngine_DefaultFactoryBuildsTheChosenProvider(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GROQ_API_KEY", "")
	st := newFakeStore(uuid.New())
	st.tenant[storage.ComponentLLM] = storage.ProviderConfig{Provider: "groq", CredentialsLast4: "env"}
	st.tenant[storage.ComponentChatLLM] = storage.ProviderConfig{Provider: "anthropic", CredentialsLast4: "env"}
	eng := NewEngine(st, nil, &fakeSearcher{}, &fakeWriter{}, fakeRecapper{}, billing.NewOffSession(st, nil, nil), nil, nil)

	err := exchange(t, eng, st)
	if err == nil {
		t.Fatal("Exchange succeeded with no LLM key in the process")
	}
	if msg := err.Error(); !strings.Contains(msg, "anthropic") || strings.Contains(msg, "groq") {
		t.Errorf("err = %v, want the Anthropic adapter's missing-key refusal", err)
	}
}
