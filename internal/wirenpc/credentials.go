package wirenpc

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
)

// This file is the issue-#69 credential bridge: it turns the UI-saved BYOK keys
// in provider_config (ADR-0004) into the per-component API keys the live voice
// loop hands its adapters, under the hybrid policy (ADR-0039). A real saved key
// is decrypted and overrides ENV; the seeded "env" placeholder resolves to the
// empty string, which the adapters read as "fall back to your *_API_KEY env var"
// — so the self-host ENV path stays untouched while the web-app BYOK path drives
// the session.

// providerKeys holds the resolved per-component BYOK API keys for one session.
// An empty string means "no DB key for this component — let the adapter fall
// back to its own *_API_KEY env var" (the hybrid policy, ADR-0039). The adapters
// (groq, stt/elevenlabs, tts/elevenlabs) all treat an empty apiKey as exactly
// that, so a zero-value providerKeys reproduces today's ENV-only behavior.
type providerKeys struct {
	llm, tts, stt string
}

// resolveKey resolves ONE component's API key under the hybrid BYOK policy
// (ADR-0039), behind the platform-key entitlement seam (ADR-0054 gate (a),
// ADR-0055).
//
// It is a one-line delegate to [llmbuild.ResolveKeyGated]: the resolver was
// moved verbatim into internal/llmbuild (#272) so the Recap engine shares the
// same BYOK credential resolution as the live voice loop, under identical
// semantics. A nil ent (the allowlist posture wired today) keeps the plain
// [llmbuild.ResolveKey] behavior byte-identical.
func resolveKey(ctx context.Context, ent llmbuild.PlatformKeyEntitlement, tenantID uuid.UUID, cipher *crypto.Cipher, cfg *storage.ProviderConfig, component storage.Component) (string, error) {
	return llmbuild.ResolveKeyGated(ctx, ent, tenantID, cipher, cfg, component)
}

// ResolveDiscordToken resolves the Discord bot token a UI-started session drives
// (issue #87), under the SAME hybrid policy as [resolveKey] (ADR-0039):
//
//   - last4 == "" or the "env" placeholder: no real token in the DB -> the
//     envToken (DISCORD_BOT_TOKEN), preserving the voice-mode / dev / CI path.
//   - a real saved token (last4 != "env") but no cipher: a CLEAR error, never a
//     silent env fall-through (AC3) — boot without $GLYPHOXA_SECRET cannot quietly
//     ignore a saved token and degrade to ENV.
//   - a real saved token the cipher cannot open: a CLEAR error wrapping crypto's.
//   - otherwise: the decrypted plaintext token.
//
// Unlike the provider keys (where "" means "adapter env fallback"), the caller
// treats an empty result as a missing token precondition — the env token is
// already folded in here.
func ResolveDiscordToken(cipher *crypto.Cipher, last4 string, ciphertext []byte, envToken string) (string, error) {
	if last4 == "" || last4 == credPlaceholderLast4 {
		return envToken, nil
	}
	if cipher == nil {
		return "", fmt.Errorf("wirenpc: Discord bot token needs decryption but the credential cipher is unavailable; set $GLYPHOXA_SECRET (ADR-0004)")
	}
	plaintext, err := cipher.Open(ciphertext)
	if err != nil {
		return "", fmt.Errorf("wirenpc: decrypt Discord bot token: %w", err)
	}
	return string(plaintext), nil
}

// resolveProviderKeys resolves all three components a voice session needs.
// Any single component's decryption failure — or entitlement refusal (ADR-0054
// gate (a)) — fails the whole resolution (AC2): a misconfigured key surfaces at
// boot, never as a half-configured session that silently falls back to ENV for
// the broken half.
func resolveProviderKeys(ctx context.Context, ent llmbuild.PlatformKeyEntitlement, tenantID uuid.UUID, cipher *crypto.Cipher, llmCfg, ttsCfg, sttCfg *storage.ProviderConfig) (providerKeys, error) {
	llm, err := resolveKey(ctx, ent, tenantID, cipher, llmCfg, storage.ComponentLLM)
	if err != nil {
		return providerKeys{}, err
	}
	tts, err := resolveKey(ctx, ent, tenantID, cipher, ttsCfg, storage.ComponentTTS)
	if err != nil {
		return providerKeys{}, err
	}
	stt, err := resolveKey(ctx, ent, tenantID, cipher, sttCfg, storage.ComponentSTT)
	if err != nil {
		return providerKeys{}, err
	}
	return providerKeys{llm: llm, tts: tts, stt: stt}, nil
}

// effectiveComponentConfig resolves ONE component's Provider Config the way
// every other tenant-scoped path already does (assist/recap resolveLLMConfig,
// gate #271): the Agent-bound config when present, else the Tenant's
// per-Component provider_config row, else nil (the env fallback, ADR-0039).
// The tenant fallback is what makes WEB-created Agents work: campaign_crud
// never sets llm/voice_provider_config_id — only the demo seed binds them —
// so without it a BYOK tenant's saved key was invisible to the voice session
// and the ADR-0054/0055 entitlement gate refused the session even though the
// key was saved right there in Configuration.
func effectiveComponentConfig(ctx context.Context, st *storage.Store, tenantID uuid.UUID, bound *storage.ProviderConfig, component storage.Component) (*storage.ProviderConfig, error) {
	if bound != nil {
		return bound, nil
	}
	pc, err := st.GetProviderConfigByComponent(ctx, tenantID, component)
	switch {
	case errors.Is(err, storage.ErrNotFound):
		// No config for this component -> the adapter reads its own env var.
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("wirenpc: load %s provider config: %w", component, err)
	default:
		return &pc, nil
	}
}

// resolveSessionKeys is the DB-side seam [RunFromDB] drives: it resolves each
// component's effective Provider Config (Agent-bound, else the Tenant's
// per-Component row — see [effectiveComponentConfig]) and resolves all three
// keys against cipher, behind the tenant's platform-key entitlement
// (Config.KeyEntitlement; nil = the allowlist posture). A missing config is
// treated as an env fallback, not an error. ElevenLabs STT shares the TTS key
// in the demo seed, but the bind is read per-Component for correctness.
//
// It also surfaces the effective LLM config so [RunFromDB] derives
// llmProviderID from the SAME config the key was resolved against — an
// Agent with no binding must not label its rounds (and price its spend) as
// the env default while speaking with the tenant's saved key.
func resolveSessionKeys(ctx context.Context, st *storage.Store, tenantID uuid.UUID, primary storage.LoadedAgent, cipher *crypto.Cipher, ent llmbuild.PlatformKeyEntitlement) (providerKeys, *storage.ProviderConfig, error) {
	llmCfg, err := effectiveComponentConfig(ctx, st, tenantID, primary.LLMConfig, storage.ComponentLLM)
	if err != nil {
		return providerKeys{}, nil, err
	}
	ttsCfg, err := effectiveComponentConfig(ctx, st, tenantID, primary.TTSConfig, storage.ComponentTTS)
	if err != nil {
		return providerKeys{}, nil, err
	}
	// STT is never Agent-joined; it always resolves from the Tenant row.
	sttCfg, err := effectiveComponentConfig(ctx, st, tenantID, nil, storage.ComponentSTT)
	if err != nil {
		return providerKeys{}, nil, err
	}
	keys, err := resolveProviderKeys(ctx, ent, tenantID, cipher, llmCfg, ttsCfg, sttCfg)
	if err != nil {
		return providerKeys{}, nil, err
	}
	return keys, llmCfg, nil
}
