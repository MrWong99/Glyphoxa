package observe

import "time"

// UsageSink is the narrow provider-usage surface a second consumer taps off the
// [StageRecorder] (#130, ADR-0046): the three usage capture points #127 already
// records (ADR-0045). The per-session spend meter (internal/spend) implements it;
// [TeeUsage] fans the recorder's usage calls out to it alongside the Prometheus
// adapter, so metering the spend meter costs the pipeline nothing new.
//
// The methods mirror [StageRecorder]'s usage trio EXACTLY (including the LLM model
// param the Prometheus adapter drops but the spend meter prices on), so a
// StageRecorder value satisfies UsageSink for the fan-out.
type UsageSink interface {
	LLMTokens(provider Provider, model string, inputTokens, outputTokens int)
	TTSCharacters(provider Provider, chars int)
	STTAudioSeconds(provider Provider, d time.Duration)
}

// TeeUsage returns a [StageRecorder] that forwards every call to base and ALSO
// fans the three [UsageSink] usage methods out to sink. Non-usage methods (the
// latency histograms, provider counters, turn outcomes) reach base ONLY — sink
// sees usage and nothing else. base stays the authoritative production recorder;
// the tee wraps it, never replaces it, so wiring the meter cannot drop a metric.
//
// It embeds base so every StageRecorder method not overridden below passes straight
// through; only the usage trio is intercepted.
func TeeUsage(base StageRecorder, sink UsageSink) StageRecorder {
	return teeRecorder{StageRecorder: base, sink: sink}
}

// teeRecorder is [TeeUsage]'s value: base handles everything, and the three
// overrides additionally forward usage to sink.
type teeRecorder struct {
	StageRecorder
	sink UsageSink
}

func (t teeRecorder) LLMTokens(provider Provider, model string, inputTokens, outputTokens int) {
	t.StageRecorder.LLMTokens(provider, model, inputTokens, outputTokens)
	t.sink.LLMTokens(provider, model, inputTokens, outputTokens)
}

func (t teeRecorder) TTSCharacters(provider Provider, chars int) {
	t.StageRecorder.TTSCharacters(provider, chars)
	t.sink.TTSCharacters(provider, chars)
}

func (t teeRecorder) STTAudioSeconds(provider Provider, d time.Duration) {
	t.StageRecorder.STTAudioSeconds(provider, d)
	t.sink.STTAudioSeconds(provider, d)
}

// HighlightClassify forwards the Session-Highlights outcome count to the wrapped
// base when it implements the sink (#428). The tee embeds StageRecorder only, so
// without this override a capability the detector recovers by type-assertion —
// HighlightClassify is NOT on StageRecorder — is silently lost behind the spend tee,
// and the counter never increments for capped sessions. Re-asserting on base keeps
// the recovery working through any tee depth (base may itself be a tee).
func (t teeRecorder) HighlightClassify(o HighlightOutcome) {
	if hc, ok := t.StageRecorder.(interface{ HighlightClassify(HighlightOutcome) }); ok {
		hc.HighlightClassify(o)
	}
}
