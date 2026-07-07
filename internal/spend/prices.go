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
	{observe.ProviderGroq, "llama-3.3-70b-versatile"}: {inputPerMTok: 0.59, outputPerMTok: 0.79},
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
