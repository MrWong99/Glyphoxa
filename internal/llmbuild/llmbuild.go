// Package llmbuild constructs an [llm.Provider] from a saved Provider Config
// (ADR-0004), keyed off the config's provider id rather than a hardwired adapter.
//
// It is the shared LLM construction seam the live voice loop (internal/wirenpc)
// and the Recap engine (internal/recap) both use: the BYOK credential resolution
// ([ResolveKey], moved verbatim from wirenpc under the hybrid policy ADR-0039) and
// the provider dispatch ([New], groq/anthropic/gemini keyed off
// provider_config.provider, groq the default per ADR-0036). [FromConfig] bundles
// the two for callers that hold a *storage.ProviderConfig.
package llmbuild

import (
	"fmt"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/anthropic"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/gemini"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/groq"
)

// EnvPlaceholderLast4 marks a provider_config whose real key lives OUTSIDE the DB
// (the keyring / an *_API_KEY env var), not in credentials_ciphertext. It matches
// the seed placeholder wirenpc writes; [ResolveKey] reads it as "-> env fallback".
const EnvPlaceholderLast4 = "env"

// ResolveKey resolves ONE component's API key under the hybrid BYOK policy
// (ADR-0039), moved verbatim from wirenpc's resolveKey (issue #69):
//
//   - cfg == nil, or cfg.CredentialsLast4 == "env" (the seeded placeholder):
//     no real key in the DB -> "" so the adapter falls back to its env var.
//   - a real saved key (last4 != "env") but no cipher: a CLEAR error, never a
//     silent empty key (AC2) — boot without $GLYPHOXA_SECRET cannot quietly
//     ignore a configured key and degrade to ENV.
//   - a real saved key the cipher cannot open: a CLEAR error wrapping crypto's.
//   - otherwise: the decrypted plaintext key.
//
// component only labels the error so an operator knows which key failed.
func ResolveKey(cipher *crypto.Cipher, cfg *storage.ProviderConfig, component storage.Component) (string, error) {
	if cfg == nil || cfg.CredentialsLast4 == EnvPlaceholderLast4 {
		return "", nil
	}
	if cipher == nil {
		return "", fmt.Errorf("llmbuild: %s key needs decryption but the credential cipher is unavailable; set $GLYPHOXA_SECRET (ADR-0004)", component)
	}
	plaintext, err := cipher.Open(cfg.CredentialsCiphertext)
	if err != nil {
		return "", fmt.Errorf("llmbuild: decrypt %s key: %w", component, err)
	}
	return string(plaintext), nil
}

// New builds an [llm.Provider] for providerID with apiKey, keyed off
// provider_config.provider (ADR-0004). An empty id defaults to Groq (the
// deployment LLM, ADR-0036); an unrecognised id is a CLEAR error naming it, never
// a silent hardwired adapter. An empty apiKey lets the adapter fall back to its
// own *_API_KEY env var at request time (the hybrid policy, ADR-0039).
func New(providerID, apiKey string) (llm.Provider, error) {
	switch providerID {
	case "", groq.ProviderID:
		return groq.New(apiKey), nil
	case anthropic.ProviderID:
		return anthropic.New(apiKey), nil
	case gemini.ProviderID:
		return gemini.New(apiKey), nil
	default:
		return nil, fmt.Errorf("llmbuild: unknown LLM provider %q (want %q, %q, or %q)", providerID, groq.ProviderID, anthropic.ProviderID, gemini.ProviderID)
	}
}

// FromConfig resolves a Provider Config into a live [llm.Provider] plus the model
// id the caller should request. It is [ResolveKey] (ComponentLLM) followed by
// [New] keyed off cfg.Provider; the returned model is cfg.Model (empty lets the
// adapter pick its default per [llm.Request.Model]). A nil cfg resolves to Groq
// with an empty env-fallback key and an empty model (the deployment default).
//
// FromConfig is UNGATED by the platform-key entitlement (ADR-0054 seam (a)):
// it is for self-host/deployment-level contexts only. A caller resolving on
// behalf of a tenant must use [ResolveKeyGated] instead, or a BYOK tenant
// silently spends the deployment's env keys.
func FromConfig(cipher *crypto.Cipher, cfg *storage.ProviderConfig) (p llm.Provider, model string, err error) {
	key, err := ResolveKey(cipher, cfg, storage.ComponentLLM)
	if err != nil {
		return nil, "", err
	}
	providerID := ""
	if cfg != nil {
		providerID = cfg.Provider
		model = cfg.Model
	}
	p, err = New(providerID, key)
	if err != nil {
		return nil, "", err
	}
	return p, model, nil
}
