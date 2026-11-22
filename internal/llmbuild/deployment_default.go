package llmbuild

import (
	"fmt"
	"strings"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/spend"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/anthropic"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/gemini"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/groq"
)

// The two envs that choose the deployment-default LLM (#702, ADR-0036 amendment
// 2026-11-22). Both are read ONCE at boot by [DeploymentDefaultFromEnv].
const (
	// EnvDefaultProvider names the provider a Tenant with no LLM Provider
	// Config runs on: groq, anthropic or gemini. Unset keeps the code default.
	EnvDefaultProvider = "GLYPHOXA_LLM_DEFAULT_PROVIDER"
	// EnvDefaultModel names the model that provider is asked for when nothing
	// more specific names one. Unset keeps the provider's own default.
	EnvDefaultModel = "GLYPHOXA_LLM_DEFAULT_MODEL"
)

// DeploymentDefault is the deployment's default LLM: the provider and model a
// Tenant runs when it has saved no LLM Provider Config of its own (#702). It is
// the ONE place "no row" turns into a concrete provider — the voice session, the
// Highlight classifier, recap, assist, the planning chat and the Configuration
// screen all resolve through it, so they cannot disagree about which adapter a
// row-less Tenant gets, which key env it spends, or which price row meters it.
//
// The zero value is the code default (Groq, openai/gpt-oss-120b; ADR-0036), so
// a deployment that sets neither env — and every test that builds none —
// behaves exactly as before the type existed. A Tenant's or Agent's own
// Provider Config always wins over it.
type DeploymentDefault struct {
	provider string // "" = the code default, groq
	model    string // "" = the provider's own default
}

// platformKeyEnv maps each provider [New] can build to the env var its adapter
// falls back to (the Platform Key, ADR-0039). It doubles as the set of valid
// values for [EnvDefaultProvider].
var platformKeyEnv = map[string]string{
	groq.ProviderID:      groq.APIKeyEnv,
	anthropic.ProviderID: anthropic.APIKeyEnv,
	gemini.ProviderID:    gemini.APIKeyEnv,
}

// PlatformKeyEnv names the env var providerID's adapter reads when a Tenant has
// saved no key (the Platform Key, ADR-0039); ok is false for a provider [New]
// cannot build.
func PlatformKeyEnv(providerID string) (name string, ok bool) {
	name, ok = platformKeyEnv[providerID]
	return name, ok
}

// DeploymentDefaultFromEnv reads [EnvDefaultProvider] and [EnvDefaultModel]
// through getenv and validates them, so a typo or a missing key stops the boot
// instead of failing every Voice Session later:
//
//   - both unset: the zero value — today's behaviour, no key required (a
//     BYOK-only deployment boots with no platform LLM key at all);
//   - an unknown provider: an error naming the accepted set;
//   - a NAMED provider whose Platform Key env is empty: an error naming that
//     env. Naming a default provider is a promise that row-less Tenants can
//     speak, and they cannot without its key.
//
// Values are trimmed; the provider is case-insensitive. A model with no
// provider applies to Groq, the code-default provider.
func DeploymentDefaultFromEnv(getenv func(string) string) (DeploymentDefault, error) {
	provider := strings.ToLower(strings.TrimSpace(getenv(EnvDefaultProvider)))
	model := strings.TrimSpace(getenv(EnvDefaultModel))
	if provider == "" {
		return DeploymentDefault{model: model}, nil
	}
	keyEnv, ok := platformKeyEnv[provider]
	if !ok {
		return DeploymentDefault{}, fmt.Errorf("%s=%q is not an LLM provider this build has (want %q, %q, or %q)",
			EnvDefaultProvider, provider, groq.ProviderID, anthropic.ProviderID, gemini.ProviderID)
	}
	if strings.TrimSpace(getenv(keyEnv)) == "" {
		return DeploymentDefault{}, fmt.Errorf("%s=%s needs the platform key %s, which is not set: tenants without their own key could not speak",
			EnvDefaultProvider, provider, keyEnv)
	}
	return DeploymentDefault{provider: provider, model: model}, nil
}

// Provider is the default provider id, never empty (groq when unset).
func (d DeploymentDefault) Provider() string {
	if d.provider == "" {
		return groq.ProviderID
	}
	return d.provider
}

// Select resolves a Tenant's effective LLM Provider Config (nil = no row) to
// the provider to build and the model to request. NEITHER is ever empty: the
// model is the row's, else the one [DeploymentDefault.VoiceModel] names for
// that provider. There is ONE model default per resolved config — the voice
// session, recap, assist, the planning chat and the Configuration screen all
// take it from here — so a model-less row cannot run (and be billed as) one
// model in a Voice Session and another off-session.
func (d DeploymentDefault) Select(cfg *storage.ProviderConfig) (providerID, model string) {
	providerID = d.Provider()
	if cfg != nil {
		if cfg.Provider != "" {
			providerID = cfg.Provider
		}
		model = cfg.Model
	}
	return providerID, d.VoiceModel(providerID, model)
}

// Priced reports whether the deployment default's voice model has a price row
// ([spend.LLMPriced]). The boot warns when it does not: an unpriced model is
// charged at the conservative fallback and can drain a trial allowance.
func (d DeploymentDefault) Priced() bool {
	return spend.LLMPriced(observe.Provider(d.Provider()), d.VoiceModel(d.Provider(), ""))
}

// VoiceModel names the model a config on providerID runs — in a Voice Session
// and off-session alike ([DeploymentDefault.Select]): model when the Provider
// Config names one, else the deployment model when providerID is the
// deployment default, else the provider's voice default ([DefaultVoiceModel]).
// It is never empty, so the model requested and the model the spend meter
// prices are one and the same.
func (d DeploymentDefault) VoiceModel(providerID, model string) string {
	if model != "" {
		return model
	}
	if providerID == "" {
		providerID = d.Provider()
	}
	if providerID == d.Provider() && d.model != "" {
		return d.model
	}
	return DefaultVoiceModel(providerID)
}
