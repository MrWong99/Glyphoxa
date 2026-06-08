package voicebench

import (
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// turnSpans holds the per-stage durations extracted from ONE turn's event log.
// A nil/absent entry means the stage did not fire this turn (e.g. no tool round,
// or — until A3 lands — the not-yet-emitted first-audio/per-round hooks).
type turnSpans map[Stage]time.Duration

// extractTurn reduces one turn's ordered event slice to its stage spans. It is
// the bench's read of the SAME bus timestamps task A3's metric subscriber uses,
// so a bench number equals a Prometheus series — the boundaries below MUST stay
// reconciled with A3 (#4). Events are assumed in publish order (the Bus delivers
// them so; [voicetest.Harness.Events] preserves it).
//
// DERIVABLE TODAY (from existing bus At: timestamps):
//   - address_detect = AddressRouted.At − STTFinal.At
//   - llm_turn       = first TTSInvoked.At − AddressRouted.At   (route → first
//     sentence dispatched; the whole LLM turn incl. tool rounds)
//   - vad_hangover   = VADSpeechEnd.At − last VADSpeechStart.At (the fixed
//     end-of-speech cost B3 tunes)
//
// SEAMED — needs A3 (#4) hooks, intentionally left unset so Check() skips them
// rather than asserting a wrong number:
//   - response_latency (HEADLINE) = first-audio-out.At − VADSpeechEnd.At
//   - llm_round (per Provider.Complete, round_index/had_tool_call)
//   - tts_ttfb / tts_total / stt_request / codec_* (some need the codec/TTS taps)
//
// When #4 publishes the first-audio and per-round events, add their cases here
// keyed on pipeline's concrete event types and the stage map fills in — no
// change to the reducer/report/SLO layers.
func extractTurn(events []voiceevent.Event) turnSpans {
	spans := turnSpans{}

	var lastSpeechStart, speechEnd, sttFinal, addrRouted, firstTTS time.Time
	var haveSpeechEnd, haveSTT, haveAddr, haveTTS bool

	for _, e := range events {
		switch ev := e.(type) {
		case voiceevent.VADSpeechStart:
			lastSpeechStart = ev.At
		case voiceevent.VADSpeechEnd:
			speechEnd, haveSpeechEnd = ev.At, true
		case voiceevent.STTFinal:
			sttFinal, haveSTT = ev.At, true
		case voiceevent.AddressRouted:
			addrRouted, haveAddr = ev.At, true
		case voiceevent.TTSInvoked:
			if !haveTTS { // first sentence dispatched this turn
				firstTTS, haveTTS = ev.At, true
			}
		}
		// NOTE(#4 seam): first-audio-out and per-LLM-round events land here once
		// pipeline defines them; wire response_latency + llm_round at that point.
	}

	if haveSpeechEnd && !lastSpeechStart.IsZero() {
		spans[StageVADHangover] = speechEnd.Sub(lastSpeechStart)
	}
	if haveAddr && haveSTT {
		spans[StageAddressDetect] = addrRouted.Sub(sttFinal)
	}
	if haveTTS && haveAddr {
		spans[StageLLMTurn] = firstTTS.Sub(addrRouted)
	}
	return spans
}

// Accumulator collects per-turn spans across replays and reduces them to a
// [Report]. The harness calls Add once per replayed turn, then Build to produce
// the JSON artifact. Splitting collection from reduction keeps the driving loop
// (clip → Conversation → Harness.Events) independent of the percentile math.
type Accumulator struct {
	tier    string
	corpus  []string
	byStage map[Stage][]time.Duration
	turns   int
}

// NewAccumulator starts a collector tagged with the tier ("cassette"/"live") and
// the corpus tiers that fed the run (for the report header).
func NewAccumulator(tier string, corpus []string) *Accumulator {
	return &Accumulator{tier: tier, corpus: corpus, byStage: map[Stage][]time.Duration{}}
}

// AddTurn folds one turn's event log into the accumulator.
func (a *Accumulator) AddTurn(events []voiceevent.Event) {
	a.turns++
	for stage, d := range extractTurn(events) {
		a.byStage[stage] = append(a.byStage[stage], d)
	}
}

// Build reduces every collected stage to its distribution and returns the
// report. Stages with no samples are simply absent from the map (Check skips
// them), so a tier that doesn't exercise a stage — or a pre-#4 run missing the
// headline hook — produces a clean report rather than a false zero.
func (a *Accumulator) Build() Report {
	stages := make(map[Stage]Distribution, len(a.byStage))
	for stage, samples := range a.byStage {
		stages[stage] = Summarize(samples)
	}
	return Report{Tier: a.tier, Corpus: a.corpus, N: a.turns, Stages: stages}
}
