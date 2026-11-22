package spend

import (
	"time"

	"github.com/MrWong99/Glyphoxa/internal/observe"
)

// Prices for the per-session spend meter (ADR-0046). EVERY figure here is an
// ESTIMATE, not a billed amount: vendor pricing is plan/tier/region dependent and
// changes without notice, so these code constants only approximate the true cost.
// They exist to answer "roughly how much has this Voice Session spent" well enough
// to enforce an operator-set cap — never to reconcile an invoice. A DB/config
// price surface is deferred until someone needs to edit prices without a deploy.
//
// Keyed by (component, provider, model) where the model actually distinguishes a
// price (LLM; TTS since #702, whose capture point now carries the synthesis
// model); STT is keyed by provider alone because its usage capture point
// (ADR-0045) carries no model. An unknown key falls back to a deliberately
// CONSERVATIVE (high) default so a missing entry over-estimates — a cap trips
// early rather than letting an unpriced provider run unbounded — plus a warn-once.

// llmKey identifies an LLM price row: Groq prices input and output tokens
// differently, and different models carry different rates.
type llmKey struct {
	provider observe.Provider
	model    string
}

// llmRate is a per-1,000,000-token price split by direction (USD).
type llmRate struct {
	inputPerMTok  float64
	outputPerMTok float64
}

// llmPrices are the known per-1M-token LLM rates. ESTIMATES.
var llmPrices = map[llmKey]llmRate{
	// Groq Llama 3.3 70B Versatile — Groq public API pricing, captured 2026-07-07:
	// $0.59 / 1M input tokens, $0.79 / 1M output tokens. ESTIMATE (tier-dependent).
	// Kept: existing campaigns still run this via their provider_config model.
	{observe.ProviderGroq, "llama-3.3-70b-versatile"}: {inputPerMTok: 0.59, outputPerMTok: 0.79},
	// Groq tool-capable catalog (#424/#426) — console.groq.com/docs/models pricing,
	// retrieved 2026-07-13. ESTIMATES (tier-dependent). openai/gpt-oss-120b is the
	// new deployment default (#424), so its entry keeps the recap/live price meter
	// off the conservative-default warn path.
	{observe.ProviderGroq, "openai/gpt-oss-120b"}:                       {inputPerMTok: 0.15, outputPerMTok: 0.60},
	{observe.ProviderGroq, "openai/gpt-oss-20b"}:                        {inputPerMTok: 0.075, outputPerMTok: 0.30},
	{observe.ProviderGroq, "meta-llama/llama-4-scout-17b-16e-instruct"}: {inputPerMTok: 0.11, outputPerMTok: 0.34},
	{observe.ProviderGroq, "qwen/qwen3-32b"}:                            {inputPerMTok: 0.29, outputPerMTok: 0.59},
	// Groq qwen3.8-27b (preview) — console.groq.com/docs/models, retrieved
	// 2026-11-22: $0.80 / $4.00. A voice-LLM bench candidate (#702). ESTIMATE.
	{observe.ProviderGroq, "qwen/qwen3.8-27b"}: {inputPerMTok: 0.80, outputPerMTok: 4.00},
	// Claude Haiku — platform.claude.com pricing, retrieved 2026-11-22 (#702).
	// Haiku 5.5 has two rate cards by prompt length: $0.10 / $0.50 up to 100K
	// prompt tokens (priced here; a voice prompt is a few thousand) and
	// $0.50 / $2.50 above. Cache reads (0.1x) and writes (1.25x) have no rate
	// here: the adapter reports them inside the input count, at the full input
	// rate. Thinking tokens are billed as output. ESTIMATES.
	{observe.ProviderAnthropic, "claude-haiku-5-5"}:          {inputPerMTok: 0.10, outputPerMTok: 0.50},
	{observe.ProviderAnthropic, "claude-haiku-4-5"}:          {inputPerMTok: 1.00, outputPerMTok: 5.00},
	{observe.ProviderAnthropic, "claude-haiku-4-5-20251001"}: {inputPerMTok: 1.00, outputPerMTok: 5.00},
	// Claude Opus 4.8 — the Anthropic adapter's own fallback model. No engine
	// runs it unasked (a model-less Anthropic config resolves to Claude Haiku,
	// llmbuild.DefaultVoiceModel); it is priced so a Tenant that NAMES it is
	// metered at its real rate, which is above the unpriced fallback's output
	// rate. $5 / $25, platform.claude.com pricing, retrieved 2026-11-22 (#702).
	// ESTIMATE.
	{observe.ProviderAnthropic, "claude-opus-4-8"}: {inputPerMTok: 5.00, outputPerMTok: 25.00},
	// Claude Sonnet 5.5 / Opus 5.5 — the Planning chat's quality models (ADR-0062
	// amendment 2026-11-22): Sonnet 5.5 is the chat default on Anthropic
	// (llmbuild.DefaultChatModel), Opus 5.5 the step up. Priced so a chat choice
	// on the PLATFORM key is metered at its real rate and passes the priced-only
	// rule. $2 / $10 and $4 / $20, platform.claude.com/docs/en/about-claude/pricing,
	// retrieved 2026-11-22. Cache reads have no rate here (see Haiku above).
	// Thinking tokens are billed as output. ESTIMATES.
	{observe.ProviderAnthropic, "claude-sonnet-5-5"}: {inputPerMTok: 2.00, outputPerMTok: 10.00},
	{observe.ProviderAnthropic, "claude-opus-5-5"}:   {inputPerMTok: 4.00, outputPerMTok: 20.00},
	// Claude Sonnet 5 / Opus 5 — the previous generation, still served. Priced
	// for attribution: a Tenant that names one is metered at its real rate
	// instead of the flat fallback. $2 / $10 and $5 / $25, same page, same day.
	// ESTIMATES.
	{observe.ProviderAnthropic, "claude-sonnet-5"}: {inputPerMTok: 2.00, outputPerMTok: 10.00},
	{observe.ProviderAnthropic, "claude-opus-5"}:   {inputPerMTok: 5.00, outputPerMTok: 25.00},
	// Gemini 2.5 Flash — the Gemini LLM adapter's default model, priced for the
	// same reason. $0.30 / $2.50 (text input / output including thinking
	// tokens), the paid-tier figure as recalled — UNVERIFIED against
	// ai.google.dev at the time of writing (#702). ESTIMATE; re-check before
	// making Gemini a deployment default.
	{observe.ProviderGemini, "gemini-2.5-flash"}: {inputPerMTok: 0.30, outputPerMTok: 2.50},
	// Gemini 2.5 Flash Image — image generation billed as tokens (#311, ADR-0004
	// amendment). ESTIMATE captured 2026-07-11; 1 image ≈ 1290 output tokens ≈
	// $0.039. Priced through the LLM sink because Gemini meters a generated image
	// as output tokens (no image-specific usage kind, ADR-0045).
	{observe.ProviderGemini, "gemini-2.5-flash-image"}: {inputPerMTok: 0.30, outputPerMTok: 30.00},
}

// ttsPricePer1kChars are the known per-1000-character TTS rates (USD) by
// provider: the rate for a model with no row of its own in [ttsModelPricePer1kChars].
// ElevenLabs bills submitted characters. ESTIMATES.
var ttsPricePer1kChars = map[observe.Provider]float64{
	// ElevenLabs standard multilingual voices — plan-dependent credit cost mapped
	// to USD, captured 2026-07-07: ~$0.30 / 1000 characters. ESTIMATE. This is the
	// full-credit tier (eleven_v3, eleven_v4, eleven_multilingual_v2: ~0.44
	// credits/char on the character-cost header, 2026-11-22).
	observe.ProviderElevenLabs: 0.30,
}

// ttsModelKey identifies a model-specific TTS price row (#702), the (provider,
// model) shape of [soundKey]: one ElevenLabs key spans models billed at
// different credit rates per character.
type ttsModelKey struct {
	provider observe.Provider
	model    string
}

// ttsModelPricePer1kChars are the TTS models priced OFF their provider's rate.
// ESTIMATES, scaled from the provider rate by the vendor's own per-character
// credit ratio.
var ttsModelPricePer1kChars = map[ttsModelKey]float64{
	// ElevenLabs half-credit tier: the character-cost response header showed
	// ~0.22 credits/char for eleven_v4_turbo and eleven_flash_v2_5 against ~0.44
	// for eleven_v3 (73 chars = 16 vs 32 credits, captured 2026-11-22), so these
	// bill half the provider rate. eleven_turbo_v2_5 sits in the same published
	// half-credit tier as flash. ESTIMATE (plan-dependent).
	{observe.ProviderElevenLabs, "eleven_v4_turbo"}:   0.15,
	{observe.ProviderElevenLabs, "eleven_flash_v2_5"}: 0.15,
	{observe.ProviderElevenLabs, "eleven_turbo_v2_5"}: 0.15,
}

// sttPricePerHour are the known per-audio-hour STT rates (USD). ESTIMATES.
var sttPricePerHour = map[observe.Provider]float64{
	// ElevenLabs Scribe v2 — API pricing captured 2026-11-22, per audio-hour:
	// batch scribe_v2 $0.20 (+$0.05 with keyterms), scribe_v2_realtime $0.22
	// (+$0.08 with keyterms). The usage capture point carries neither the
	// transport nor whether keyterms were sent, so this is the UPPER BOUND of what
	// the binary sends: realtime with keyterms, the default since #701/#702. A
	// batch or keyterm-less utterance is over-estimated by at most $0.10/h, which
	// keeps the meter conservative. Replaces the stale $0.40 (2026-07-07).
	// ESTIMATE.
	observe.ProviderElevenLabs: 0.30,
}

// soundKey identifies a sound-generation price row (#312, ADR-0004 amendment):
// SFX stings and Music tracks meter under their own model ids — the model is
// the attribution discriminator since both ride the tts Provider Config.
type soundKey struct {
	provider observe.Provider
	model    string
}

// soundPricePerMinute are the known per-audio-minute sound-generation rates
// (USD), keyed (provider, model). ESTIMATES: ElevenLabs bills both endpoints in
// plan-dependent credits, mapped here at the same ~$0.30/1k-credit ratio the
// TTS estimate uses, priced on the REQUESTED duration.
var soundPricePerMinute = map[soundKey]float64{
	// ElevenLabs sound-effects (sting) — vendor credit table captured
	// 2026-08-18: ~100 credits per generation (≲22s) ≈ $0.03; normalized to
	// ~$0.10 / audio-minute. ESTIMATE (plan-dependent).
	{observe.ProviderElevenLabs, "eleven_text_to_sound_v2"}: 0.10,
	// ElevenLabs Music — vendor credit table captured 2026-08-18: ~2000
	// credits per track-minute ≈ $0.60 / audio-minute. ESTIMATE
	// (plan-dependent).
	{observe.ProviderElevenLabs, "music_v1"}: 0.60,
}

// defaultSoundPerMinute is the conservative fallback for an unknown
// sound-generation (provider, model): above the known Music estimate so an
// unpriced model over-estimates, matching the LLM/TTS/STT fallback posture.
// ESTIMATE.
const defaultSoundPerMinute = 1.00

// embeddingPricePerMTok are the known per-1M-token embeddings rates (USD),
// keyed by provider alone — v1.0's only embeddings provider is local Ollama
// (ADR-0011), which costs nothing per call; the OpenAI slot gets its own entry
// when that adapter lands (ADR-0004). ESTIMATES, consumed by the campaign
// search query-embed pricing (#591).
var embeddingPricePerMTok = map[observe.Provider]float64{
	// Ollama runs the deployment's own hardware — no per-token vendor charge.
	observe.ProviderOllama: 0.0,
}

// defaultEmbeddingPerMTok is the conservative fallback for an unknown embeddings
// provider: above OpenAI's text-embedding-3-large (~$0.13 / 1M tokens, captured
// 2026-08-13) so an unpriced provider over-estimates, matching the LLM/TTS/STT
// fallback posture. ESTIMATE.
const defaultEmbeddingPerMTok = 0.25

// Conservative fallbacks for an unknown price key: high enough that an unpriced
// provider/model over-estimates rather than running unbounded under a cap. All
// ESTIMATES.
const (
	// defaultLLMInputPerMTok / defaultLLMOutputPerMTok: a frontier-model-class
	// ceiling ($5 in / $15 out per 1M) so an unrecognised LLM is never cheap.
	defaultLLMInputPerMTok  = 5.00
	defaultLLMOutputPerMTok = 15.00
	// defaultTTSPer1kChars: above the known ElevenLabs estimate.
	defaultTTSPer1kChars = 0.50
	// defaultSTTPerHour: above the known Scribe estimate.
	defaultSTTPerHour = 1.00
)

// EstimateLLMUSD estimates the USD cost of one completion's tokens from the
// static price map, exported for the Usage Ledger (ADR-0054). Same figures the
// Meter accumulates; an unknown (provider, model) silently uses the conservative
// default — the live Meter owns the warn-once, the ledger just prices.
func EstimateLLMUSD(provider observe.Provider, model string, inputTokens, outputTokens int) float64 {
	usd, _ := llmCostUSD(provider, model, inputTokens, outputTokens)
	return usd
}

// LLMPriced reports whether (provider, model) has its own row in the price map.
// A model without one is charged at one flat default ($5 / $15 per MTok): far
// above a small model's real rate, but BELOW a frontier model's (Claude Opus
// output is $25). It over-meters the one and under-meters the other, so:
// whatever offers a model unasked — a provider default, a deployment default,
// a fallback catalog — checks it here, and a model that would run on the
// PLATFORM key must be priced or is refused (#702).
func LLMPriced(provider observe.Provider, model string) bool {
	_, ok := llmPrices[llmKey{provider, model}]
	return ok
}

// EstimateTTSUSD estimates the USD cost of chars submitted to a TTS synthesizer
// running model (exported for the Usage Ledger, ADR-0054). A model with no row of
// its own is priced at its provider's rate.
func EstimateTTSUSD(provider observe.Provider, model string, chars int) float64 {
	usd, _ := ttsCostUSD(provider, model, chars)
	return usd
}

// EstimateSTTUSD estimates the USD cost of audio submitted to an STT recognizer
// (exported for the Usage Ledger, ADR-0054).
func EstimateSTTUSD(provider observe.Provider, d time.Duration) float64 {
	usd, _ := sttCostUSD(provider, d)
	return usd
}

// EstimateSoundUSD estimates the USD cost of one sound generation (#312) from
// the requested audio duration — the Highlight sting/Music enrichment pricing.
// Like EstimateEmbeddingUSD it is a direct estimate rather than a Meter capture
// point: the ADR-0045 usage trio carries no sound kind, ADR-0046 caps are
// live-session-only (an off-session enrichment is never cap-gated), and the
// enrichment job flushes its own per-generation Usage Ledger rows (Tenant
// attribution under the SFX/Music model id, ADR-0004 amendment). An unknown
// (provider, model) uses the conservative default (over-estimates, mirroring
// the other price fallbacks).
func EstimateSoundUSD(provider observe.Provider, model string, d time.Duration) float64 {
	p, ok := soundPricePerMinute[soundKey{provider, model}]
	if !ok {
		p = defaultSoundPerMinute
	}
	return d.Minutes() * p
}

// EstimateEmbeddingUSD estimates the USD cost of tokens submitted to an
// embeddings provider — the campaign search query-embed pricing (#591). It is a
// direct estimate rather than a Meter capture point: the ADR-0045 usage trio
// (LLM/TTS/STT) carries no embeddings kind, ADR-0046 caps are live-session-only
// (a palette query is never cap-gated), and embeddings run on the deployment's
// local Ollama rather than a paid key — so the caller logs this figure and
// nothing else consumes it; it is deliberately outside the off-session
// metering seam (billing.OffSession), which exists for paid provider calls. An unknown provider uses the conservative
// default (over-estimates, mirroring the other price fallbacks).
func EstimateEmbeddingUSD(provider observe.Provider, tokens int) float64 {
	p, ok := embeddingPricePerMTok[provider]
	if !ok {
		p = defaultEmbeddingPerMTok
	}
	return float64(tokens) / 1e6 * p
}

// llmCostUSD estimates the USD cost of one completion's tokens. known is false
// when the (provider, model) key had no entry and the conservative default was
// used (the caller warns once).
func llmCostUSD(provider observe.Provider, model string, inputTokens, outputTokens int) (usd float64, known bool) {
	r, ok := llmPrices[llmKey{provider, model}]
	if !ok {
		r = llmRate{inputPerMTok: defaultLLMInputPerMTok, outputPerMTok: defaultLLMOutputPerMTok}
	}
	usd = float64(inputTokens)/1e6*r.inputPerMTok + float64(outputTokens)/1e6*r.outputPerMTok
	return usd, ok
}

// ttsCostUSD estimates the USD cost of chars submitted to a TTS synthesizer
// running model: the (provider, model) row when one exists, else the provider
// rate, else the conservative default. known is false only for an unpriced
// PROVIDER (an unlisted model of a priced provider is the provider rate by design).
func ttsCostUSD(provider observe.Provider, model string, chars int) (usd float64, known bool) {
	p, ok := ttsModelPricePer1kChars[ttsModelKey{provider, model}]
	if !ok {
		p, ok = ttsPricePer1kChars[provider]
	}
	if !ok {
		p = defaultTTSPer1kChars
	}
	return float64(chars) / 1000 * p, ok
}

// sttCostUSD estimates the USD cost of audio submitted to an STT recognizer.
func sttCostUSD(provider observe.Provider, d time.Duration) (usd float64, known bool) {
	p, ok := sttPricePerHour[provider]
	if !ok {
		p = defaultSTTPerHour
	}
	return d.Hours() * p, ok
}
