package llmbuild

import (
	"bytes"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/anthropic"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/gemini"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/groq"
)

// This file is the ONE resolution of the Planning chat model (ADR-0062
// amendment 2026-11-22). The chat model is a Tenant-level CHOICE (provider +
// model) stored as the Tenant's single chat_llm Provider Config; no row means
// "same as voice". The engine (internal/planchat), the Configuration read, the
// chat model catalog and the chat health probe all call [DeploymentDefault.SelectChat],
// so the screen cannot name a model the engine does not run.

// ChatSource says which rung of the chat ladder produced a [ChatLLM].
type ChatSource string

const (
	// ChatSourceButler is the Butler-bound row (agents.chat_llm_provider_config_id).
	// Reserved: nothing writes that column today.
	ChatSourceButler ChatSource = "butler"
	// ChatSourceChoice is the Tenant's chat choice, its chat_llm row.
	ChatSourceChoice ChatSource = "choice"
	// ChatSourceVoice is "same as voice": the Tenant's llm row.
	ChatSourceVoice ChatSource = "voice"
	// ChatSourceDefault is the deployment default: the Tenant has no row at all.
	ChatSourceDefault ChatSource = "default"
)

// ChatKeySource says whose key a [ChatLLM] runs on.
type ChatKeySource string

const (
	// ChatKeyChat is a key saved on the chat row itself (a chat-only key).
	ChatKeyChat ChatKeySource = "chat"
	// ChatKeyProvider is the Tenant's own key in that provider's LLM slot.
	ChatKeyProvider ChatKeySource = "provider"
	// ChatKeyPlatform is the deployment's Platform Key. It is only a CLAIM on
	// the key: [ResolveKeyGated] checks the entitlement at call time, and the
	// env var may be unset.
	ChatKeyPlatform ChatKeySource = "platform"
)

// ChatLLM is the resolved Planning chat LLM. Provider and Model are never
// empty. KeyConfig is the row to hand to [ResolveKeyGated] with KeyComponent;
// nil means the Platform Key.
type ChatLLM struct {
	Provider string
	Model    string
	Source   ChatSource

	KeySource    ChatKeySource
	KeyConfig    *storage.ProviderConfig
	KeyComponent storage.Component
}

// ChatProviders are the providers a chat choice may name, in the order the
// Configuration screen offers them. Gemini is buildable ([New]) and a Tenant
// whose VOICE runs on it still plans on it through "same as voice", but it is
// not an explicit chat choice in v1.
var ChatProviders = []string{groq.ProviderID, anthropic.ProviderID}

// IsChatProvider reports whether providerID may be chosen for the Planning chat.
func IsChatProvider(providerID string) bool {
	for _, p := range ChatProviders {
		if p == providerID {
			return true
		}
	}
	return false
}

// DefaultChatModel names the model an explicit chat choice on providerID runs
// when it names none. It applies ONLY to a chat choice: "same as voice" keeps
// the voice model ([DeploymentDefault.Select]), so ADR-0036's one model default
// per config still holds for the llm row. Anthropic gets its own chat default
// ([anthropic.ChatModel]): the voice default there is Haiku, picked for
// first-token latency, and prep chat wants the stronger model. Groq and Gemini
// keep their voice defaults. Every value returned here must be priced
// (TestDefaultChatModelsArePriced): it can run on the Platform Key.
func DefaultChatModel(providerID string) string {
	switch providerID {
	case anthropic.ProviderID:
		return anthropic.ChatModel
	case gemini.ProviderID:
		return gemini.DefaultModel
	default: // "" and groq
		return groq.DefaultModel
	}
}

// NewChat is [New] for the Planning chat: the same dispatch and the same
// errors, with Anthropic built under the chat thinking policy
// ([anthropic.ChatThinking]) instead of the voice one. A Voice Session never
// calls it, so voice requests are untouched.
func NewChat(providerID, apiKey string) (llm.Provider, error) {
	if providerID == anthropic.ProviderID {
		return anthropic.New(apiKey, anthropic.WithThinkingFor(anthropic.ChatThinking)), nil
	}
	return New(providerID, apiKey)
}

// SelectChat resolves the Planning chat LLM from the Butler-bound row (nil =
// none) and ALL of the Tenant's Provider Configs. Pure: no I/O, no entitlement
// check, no env read. The ladder:
//
//  0. the Butler-bound row, through [DeploymentDefault.Select] as before;
//  1. the Tenant's chat choice (newest chat_llm row): its provider, its model
//     or else [DefaultChatModel], and a key from [ChatKeyFor];
//  2. "same as voice": the Tenant's newest llm row through Select — the model
//     and key a Voice Session runs;
//  3. the deployment default.
//
// A chat choice NEVER falls back to another provider: with no key of its own
// and none in that provider's LLM slot it resolves to the Platform Key of the
// SAME provider, and the call fails with [ErrNoPlatformKeyEntitlement] when the
// Tenant may not use it.
func (d DeploymentDefault) SelectChat(bound *storage.ProviderConfig, tenantConfigs []storage.ProviderConfig) ChatLLM {
	if bound != nil {
		provider, model := d.Select(bound)
		sel := ChatLLM{Provider: provider, Model: model, Source: ChatSourceButler, KeyConfig: bound, KeyComponent: bound.Component}
		sel.KeySource = ChatKeyPlatform
		if keySaved(bound.CredentialsLast4) {
			sel.KeySource = ChatKeyChat
		}
		return sel
	}

	if chat := newestConfig(tenantConfigs, storage.ComponentChatLLM); chat != nil {
		provider := chat.Provider
		if provider == "" {
			provider = d.Provider()
		}
		model := chat.Model
		if model == "" {
			model = DefaultChatModel(provider)
		}
		sel := ChatLLM{Provider: provider, Model: model, Source: ChatSourceChoice}
		sel.KeySource, sel.KeyConfig, sel.KeyComponent = ChatKeyFor(provider, tenantConfigs)
		return sel
	}

	if voice := newestConfig(tenantConfigs, storage.ComponentLLM); voice != nil {
		provider, model := d.Select(voice)
		sel := ChatLLM{Provider: provider, Model: model, Source: ChatSourceVoice, KeyConfig: voice, KeyComponent: storage.ComponentLLM}
		sel.KeySource = ChatKeyPlatform
		if keySaved(voice.CredentialsLast4) {
			sel.KeySource = ChatKeyProvider
		}
		return sel
	}

	provider, model := d.Select(nil)
	return ChatLLM{
		Provider: provider, Model: model, Source: ChatSourceDefault,
		KeySource: ChatKeyPlatform, KeyComponent: storage.ComponentChatLLM,
	}
}

// ChatKeyFor walks the key ladder of a chat choice on providerID:
//
//	(a) the chat row's own key, when the chat row is on providerID and holds one;
//	(b) the Tenant's key in providerID's LLM slot;
//	(c) the Platform Key (cfg nil): [ResolveKeyGated] decides at call time.
//
// It also answers "what would this provider run on if chosen" for a provider
// the Tenant has NOT chosen, which is how the Configuration screen labels each
// option. component labels key-resolution errors with the slot the key is in.
func ChatKeyFor(providerID string, tenantConfigs []storage.ProviderConfig) (source ChatKeySource, cfg *storage.ProviderConfig, component storage.Component) {
	if chat := newestConfig(tenantConfigs, storage.ComponentChatLLM); chat != nil &&
		chat.Provider == providerID && keySaved(chat.CredentialsLast4) {
		return ChatKeyChat, chat, storage.ComponentChatLLM
	}
	var own *storage.ProviderConfig
	for i := range tenantConfigs {
		c := &tenantConfigs[i]
		if c.Component != storage.ComponentLLM || c.Provider != providerID || !keySaved(c.CredentialsLast4) {
			continue
		}
		if own == nil || newer(c, own) {
			own = c
		}
	}
	if own != nil {
		return ChatKeyProvider, own, storage.ComponentLLM
	}
	return ChatKeyPlatform, nil, storage.ComponentChatLLM
}

// keySaved reports whether last4 marks a real key stored on the row: not
// empty, and not the "env" placeholder [ResolveKey] reads as "ride the
// Platform Key".
func keySaved(last4 string) bool {
	return last4 != "" && last4 != EnvPlaceholderLast4
}

// newestConfig returns the Tenant's newest row of component, nil when it has
// none. "Newest" is updated_at, then id — the order of
// storage.GetProviderConfigByComponent, so a pure resolution over a listed
// slice and a single-row read agree if a race ever leaves two rows.
func newestConfig(configs []storage.ProviderConfig, component storage.Component) *storage.ProviderConfig {
	var best *storage.ProviderConfig
	for i := range configs {
		c := &configs[i]
		if c.Component != component {
			continue
		}
		if best == nil || newer(c, best) {
			best = c
		}
	}
	return best
}

// newer reports whether a sorts before b under "updated_at DESC, id DESC".
func newer(a, b *storage.ProviderConfig) bool {
	if !a.UpdatedAt.Equal(b.UpdatedAt) {
		return a.UpdatedAt.After(b.UpdatedAt)
	}
	return bytes.Compare(a.ID[:], b.ID[:]) > 0
}
