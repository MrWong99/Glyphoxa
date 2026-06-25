package wirenpc

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

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
// (ADR-0039):
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
func resolveKey(cipher *crypto.Cipher, cfg *storage.ProviderConfig, component storage.Component) (string, error) {
	if cfg == nil || cfg.CredentialsLast4 == credPlaceholderLast4 {
		return "", nil
	}
	if cipher == nil {
		return "", fmt.Errorf("wirenpc: %s key needs decryption but the credential cipher is unavailable; set $GLYPHOXA_SECRET (ADR-0004)", component)
	}
	plaintext, err := cipher.Open(cfg.CredentialsCiphertext)
	if err != nil {
		return "", fmt.Errorf("wirenpc: decrypt %s key: %w", component, err)
	}
	return string(plaintext), nil
}

// resolveProviderKeys resolves all three components a voice session needs.
// Any single component's decryption failure fails the whole resolution (AC2):
// a misconfigured key surfaces at boot, never as a half-configured session that
// silently falls back to ENV for the broken half.
func resolveProviderKeys(cipher *crypto.Cipher, llmCfg, ttsCfg, sttCfg *storage.ProviderConfig) (providerKeys, error) {
	llm, err := resolveKey(cipher, llmCfg, storage.ComponentLLM)
	if err != nil {
		return providerKeys{}, err
	}
	tts, err := resolveKey(cipher, ttsCfg, storage.ComponentTTS)
	if err != nil {
		return providerKeys{}, err
	}
	stt, err := resolveKey(cipher, sttCfg, storage.ComponentSTT)
	if err != nil {
		return providerKeys{}, err
	}
	return providerKeys{llm: llm, tts: tts, stt: stt}, nil
}

// resolveSessionKeys is the DB-side seam [RunFromDB] drives: it reads the
// Tenant's STT Provider Config by Component (STT is not Agent-joined, unlike the
// LLM/TTS configs the primary agent's LoadedAgent already carries) and resolves
// all three keys against cipher. A missing STT config is treated as an env
// fallback, not an error. ElevenLabs STT shares the TTS key in the demo seed,
// but the bind is read per-Component for correctness.
func resolveSessionKeys(ctx context.Context, st *storage.Store, tenantID uuid.UUID, primary storage.LoadedAgent, cipher *crypto.Cipher) (providerKeys, error) {
	var sttCfg *storage.ProviderConfig
	pc, err := st.GetProviderConfigByComponent(ctx, tenantID, storage.ComponentSTT)
	switch {
	case errors.Is(err, storage.ErrNotFound):
		// No STT config bound -> the adapter reads ELEVENLABS_API_KEY (env).
	case err != nil:
		return providerKeys{}, fmt.Errorf("wirenpc: load STT provider config: %w", err)
	default:
		sttCfg = &pc
	}
	return resolveProviderKeys(cipher, primary.LLMConfig, primary.TTSConfig, sttCfg)
}
