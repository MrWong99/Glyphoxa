package llmbuild

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/spend"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/anthropic"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/gemini"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/groq"
)

func TestDefaultChatModel(t *testing.T) {
	t.Parallel()
	for id, want := range map[string]string{
		anthropic.ProviderID: "claude-sonnet-5-5",
		groq.ProviderID:      "openai/gpt-oss-120b",
		gemini.ProviderID:    gemini.DefaultModel,
		"":                   groq.DefaultModel,
	} {
		if got := DefaultChatModel(id); got != want {
			t.Errorf("DefaultChatModel(%q) = %q, want %q", id, got, want)
		}
	}
}

// A chat default can run on the Platform Key with nobody naming it, so each one
// needs a price row: an unpriced model is refused there, and a model-less chat
// choice would then be unusable for exactly the Tenants it is meant for.
func TestDefaultChatModelsArePriced(t *testing.T) {
	t.Parallel()
	for provider := range platformKeyEnv {
		model := DefaultChatModel(provider)
		if !spend.LLMPriced(observe.Provider(provider), model) {
			t.Errorf("chat default (%s, %s) has no price row in internal/spend/prices.go", provider, model)
		}
	}
}

func TestChatProviders(t *testing.T) {
	t.Parallel()
	for _, p := range ChatProviders {
		if _, ok := PlatformKeyEnv(p); !ok {
			t.Errorf("chat provider %q is not one New can build", p)
		}
		if !IsChatProvider(p) {
			t.Errorf("IsChatProvider(%q) = false", p)
		}
	}
	for _, p := range []string{"", "gemini", "openai", "elevenlabs"} {
		if IsChatProvider(p) {
			t.Errorf("IsChatProvider(%q) = true", p)
		}
	}
}

func TestNewChat_UnknownProviderErrors(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"", "no-such-provider"} {
		if p, err := NewChat(id, ""); err == nil {
			t.Errorf("NewChat(%q) = %v, want an error", id, p)
		}
	}
}

func TestNewChat_BuildsEachProvider(t *testing.T) {
	t.Parallel()
	for id := range platformKeyEnv {
		p, err := NewChat(id, "k")
		if err != nil || p == nil {
			t.Errorf("NewChat(%q) = (%v, %v), want a provider", id, p, err)
		}
	}
}

// row builds a Provider Config for the SelectChat table. age orders rows: a
// smaller age is a more recent updated_at.
func row(component storage.Component, provider, model, last4 string, age time.Duration) storage.ProviderConfig {
	return storage.ProviderConfig{
		ID:               uuid.New(),
		Component:        component,
		Provider:         provider,
		Model:            model,
		CredentialsLast4: last4,
		UpdatedAt:        time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC).Add(-age),
	}
}

func TestSelectChat(t *testing.T) {
	t.Parallel()
	anthropicDefault, err := DeploymentDefaultFromEnv(env(map[string]string{
		EnvDefaultProvider: "anthropic", EnvDefaultModel: "claude-haiku-5-5", anthropic.APIKeyEnv: "k",
	}))
	if err != nil {
		t.Fatal(err)
	}
	var groqDefault DeploymentDefault

	voiceAnthropicKeyed := row(storage.ComponentLLM, "anthropic", "", "1234", time.Hour)
	voiceGroqKeyed := row(storage.ComponentLLM, "groq", "", "9999", time.Hour)
	chatAnthropicKeyless := row(storage.ComponentChatLLM, "anthropic", "", "env", time.Minute)
	chatAnthropicKeyed := row(storage.ComponentChatLLM, "anthropic", "", "4321", time.Minute)
	chatAnthropicOpus := row(storage.ComponentChatLLM, "anthropic", "claude-opus-5-5", "env", time.Minute)
	chatGroqOld := row(storage.ComponentChatLLM, "groq", "qwen/qwen3-32b", "env", 2*time.Hour)
	chatGroqKeyless := row(storage.ComponentChatLLM, "groq", "", "env", time.Minute)
	butlerRow := row(storage.ComponentChatLLM, "groq", "openai/gpt-oss-20b", "5555", 3*time.Hour)

	type want struct {
		provider, model string
		source          ChatSource
		keySource       ChatKeySource
		keyConfig       *storage.ProviderConfig // nil = the Platform Key
		keyComponent    storage.Component
	}
	for _, tc := range []struct {
		name    string
		d       DeploymentDefault
		bound   *storage.ProviderConfig
		configs []storage.ProviderConfig
		want    want
	}{
		{
			name: "(a) no rows: the deployment default on the platform key",
			d:    anthropicDefault,
			want: want{"anthropic", "claude-haiku-5-5", ChatSourceDefault, ChatKeyPlatform, nil, storage.ComponentChatLLM},
		},
		{
			name:    "(b) only a keyed anthropic llm row without a model: same as voice",
			d:       groqDefault,
			configs: []storage.ProviderConfig{voiceAnthropicKeyed},
			want:    want{"anthropic", "claude-haiku-5-5", ChatSourceVoice, ChatKeyProvider, &voiceAnthropicKeyed, storage.ComponentLLM},
		},
		{
			name:    "(c) keyless chat choice borrows the key in that provider's LLM slot",
			d:       groqDefault,
			configs: []storage.ProviderConfig{chatAnthropicKeyless, voiceAnthropicKeyed},
			want:    want{"anthropic", "claude-sonnet-5-5", ChatSourceChoice, ChatKeyProvider, &voiceAnthropicKeyed, storage.ComponentLLM},
		},
		{
			name:    "(d) keyless chat choice with voice on another provider: platform key, never the other provider",
			d:       groqDefault,
			configs: []storage.ProviderConfig{chatAnthropicKeyless, voiceGroqKeyed},
			want:    want{"anthropic", "claude-sonnet-5-5", ChatSourceChoice, ChatKeyPlatform, nil, storage.ComponentChatLLM},
		},
		{
			name:    "(e) the chat row's own key wins over the provider slot",
			d:       groqDefault,
			configs: []storage.ProviderConfig{chatAnthropicKeyed, voiceAnthropicKeyed},
			want:    want{"anthropic", "claude-sonnet-5-5", ChatSourceChoice, ChatKeyChat, &chatAnthropicKeyed, storage.ComponentChatLLM},
		},
		{
			name:    "(f) an explicit chat model is kept",
			d:       groqDefault,
			configs: []storage.ProviderConfig{chatAnthropicOpus},
			want:    want{"anthropic", "claude-opus-5-5", ChatSourceChoice, ChatKeyPlatform, nil, storage.ComponentChatLLM},
		},
		{
			name:    "(g) two chat rows: the newest wins",
			d:       groqDefault,
			configs: []storage.ProviderConfig{chatGroqOld, chatAnthropicOpus},
			want:    want{"anthropic", "claude-opus-5-5", ChatSourceChoice, ChatKeyPlatform, nil, storage.ComponentChatLLM},
		},
		{
			name:    "(h) the Butler-bound row wins over everything",
			d:       anthropicDefault,
			bound:   &butlerRow,
			configs: []storage.ProviderConfig{chatAnthropicKeyed, voiceAnthropicKeyed},
			want:    want{"groq", "openai/gpt-oss-20b", ChatSourceButler, ChatKeyChat, &butlerRow, storage.ComponentChatLLM},
		},
		{
			name: "(i) an llm row with last4 env or empty is not a key",
			d:    groqDefault,
			configs: []storage.ProviderConfig{
				chatAnthropicKeyless,
				row(storage.ComponentLLM, "anthropic", "", "env", time.Hour),
				row(storage.ComponentLLM, "anthropic", "", "", 2*time.Hour),
			},
			want: want{"anthropic", "claude-sonnet-5-5", ChatSourceChoice, ChatKeyPlatform, nil, storage.ComponentChatLLM},
		},
		{
			name:    "(j) chat on groq under an anthropic deployment default: the groq default model",
			d:       anthropicDefault,
			configs: []storage.ProviderConfig{chatGroqKeyless},
			want:    want{"groq", groq.DefaultModel, ChatSourceChoice, ChatKeyPlatform, nil, storage.ComponentChatLLM},
		},
		{
			name:    "a chat choice on the deployment-default provider takes the CHAT default, not the deployment model",
			d:       anthropicDefault,
			configs: []storage.ProviderConfig{chatAnthropicKeyless},
			want:    want{"anthropic", "claude-sonnet-5-5", ChatSourceChoice, ChatKeyPlatform, nil, storage.ComponentChatLLM},
		},
		{
			name:    "same as voice on an env-placeholder llm row rides the platform key",
			d:       groqDefault,
			configs: []storage.ProviderConfig{row(storage.ComponentLLM, "groq", "qwen/qwen3-32b", "env", time.Hour)},
			want:    want{"groq", "qwen/qwen3-32b", ChatSourceVoice, ChatKeyPlatform, nil, storage.ComponentLLM},
		},
		{
			name: "other components never take part",
			d:    groqDefault,
			configs: []storage.ProviderConfig{
				row(storage.ComponentTTS, "elevenlabs", "", "7777", time.Second),
				row(storage.ComponentImage, "gemini", "", "8888", time.Second),
			},
			want: want{"groq", groq.DefaultModel, ChatSourceDefault, ChatKeyPlatform, nil, storage.ComponentChatLLM},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.d.SelectChat(tc.bound, tc.configs)
			if got.Provider == "" || got.Model == "" {
				t.Fatalf("provider/model empty: %+v", got)
			}
			if got.Provider != tc.want.provider || got.Model != tc.want.model {
				t.Errorf("resolved %s/%s, want %s/%s", got.Provider, got.Model, tc.want.provider, tc.want.model)
			}
			if got.Source != tc.want.source {
				t.Errorf("source = %q, want %q", got.Source, tc.want.source)
			}
			if got.KeySource != tc.want.keySource {
				t.Errorf("key source = %q, want %q", got.KeySource, tc.want.keySource)
			}
			if got.KeyComponent != tc.want.keyComponent {
				t.Errorf("key component = %q, want %q", got.KeyComponent, tc.want.keyComponent)
			}
			// A platform-key resolution must hand ResolveKey something it reads
			// as "no saved key": nil, or a row carrying the env placeholder.
			if tc.want.keyConfig == nil {
				if key, err := ResolveKey(nil, got.KeyConfig, got.KeyComponent); err != nil || key != "" {
					t.Errorf("platform rung resolved key %q, err %v; want the env fallback", key, err)
				}
				return
			}
			if got.KeyConfig == nil || got.KeyConfig.ID != tc.want.keyConfig.ID {
				t.Errorf("key config = %+v, want row %s", got.KeyConfig, tc.want.keyConfig.ID)
			}
		})
	}
}

// Two rows written in the same instant resolve by id, the tie-break of
// storage.GetProviderConfigByComponent (updated_at DESC, id DESC), whatever
// order the slice lists them in.
func TestSelectChat_TieBreaksOnID(t *testing.T) {
	t.Parallel()
	low := row(storage.ComponentChatLLM, "groq", "", "env", time.Minute)
	high := row(storage.ComponentChatLLM, "anthropic", "", "env", time.Minute)
	low.ID = uuid.MustParse("00000000-0000-0000-0000-000000000001")
	high.ID = uuid.MustParse("ffffffff-0000-0000-0000-000000000001")
	var d DeploymentDefault
	for _, configs := range [][]storage.ProviderConfig{{low, high}, {high, low}} {
		if got := d.SelectChat(nil, configs); got.Provider != "anthropic" {
			t.Errorf("SelectChat picked %q, want the higher id (anthropic)", got.Provider)
		}
	}
}

// ChatKeyFor also answers for a provider the Tenant has NOT chosen: the
// Configuration screen labels each option with the key it would run on.
func TestChatKeyFor_UnchosenProvider(t *testing.T) {
	t.Parallel()
	chat := row(storage.ComponentChatLLM, "groq", "", "1111", time.Minute)
	voice := row(storage.ComponentLLM, "anthropic", "", "2222", time.Hour)
	configs := []storage.ProviderConfig{chat, voice}

	if src, cfg, _ := ChatKeyFor("groq", configs); src != ChatKeyChat || cfg == nil || cfg.ID != chat.ID {
		t.Errorf("groq: %q %+v, want the chat row's own key", src, cfg)
	}
	// The chat-only Groq key must not be offered to Anthropic.
	if src, cfg, comp := ChatKeyFor("anthropic", configs); src != ChatKeyProvider || cfg == nil || cfg.ID != voice.ID || comp != storage.ComponentLLM {
		t.Errorf("anthropic: %q %+v %q, want the tenant's anthropic llm key", src, cfg, comp)
	}
	if src, cfg, _ := ChatKeyFor("gemini", configs); src != ChatKeyPlatform || cfg != nil {
		t.Errorf("gemini: %q %+v, want the platform rung", src, cfg)
	}
}
