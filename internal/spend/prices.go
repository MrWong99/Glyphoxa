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
// price (LLM); TTS and STT are keyed by provider alone because their usage capture
// points (ADR-0045) carry no model. An unknown key falls back to a deliberately
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
	// Gemini 2.5 Flash Image — image generation billed as tokens (#311, ADR-0004
	// amendment). ESTIMATE captured 2026-07-11; 1 image ≈ 1290 output tokens ≈
	// $0.039. Priced through the LLM sink because Gemini meters a generated image
	// as output tokens (no image-specific usage kind, ADR-0045).
	{observe.ProviderGemini, "gemini-2.5-flash-image"}: {inputPerMTok: 0.30, outputPerMTok: 30.00},
}

// ttsPricePer1kChars are the known per-1000-character TTS rates (USD). ElevenLabs
// bills submitted characters. ESTIMATES.
var ttsPricePer1kChars = map[observe.Provider]float64{
	// ElevenLabs standard multilingual voices — plan-dependent credit cost mapped
	// to USD, captured 2026-07-07: ~$0.30 / 1000 characters. ESTIMATE.
	observe.ProviderElevenLabs: 0.30,
}

// sttPricePerHour are the known per-audio-hour STT rates (USD). ESTIMATES.
var sttPricePerHour = map[observe.Provider]float64{
	// ElevenLabs Scribe speech-to-text — published pricing, captured 2026-07-07:
	// ~$0.40 / audio-hour. ESTIMATE.
	observe.ProviderElevenLabs: 0.40,
}

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

// EstimateTTSUSD estimates the USD cost of chars submitted to a TTS synthesizer
// (exported for the Usage Ledger, ADR-0054).
func EstimateTTSUSD(provider observe.Provider, chars int) float64 {
	usd, _ := ttsCostUSD(provider, chars)
	return usd
}

// EstimateSTTUSD estimates the USD cost of audio submitted to an STT recognizer
// (exported for the Usage Ledger, ADR-0054).
func EstimateSTTUSD(provider observe.Provider, d time.Duration) float64 {
	usd, _ := sttCostUSD(provider, d)
	return usd
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

// ttsCostUSD estimates the USD cost of chars submitted to a TTS synthesizer.
func ttsCostUSD(provider observe.Provider, chars int) (usd float64, known bool) {
	p, ok := ttsPricePer1kChars[provider]
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
