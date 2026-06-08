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
// the bench's read of the SAME bus timestamps observe's A3 subscriber uses, so a
// bench number equals a Prometheus series — the boundaries below are reconciled
// 1:1 with internal/observe (#4/#10). Events are assumed in publish order (the
// Bus delivers them so; [voicetest.Harness.Events] preserves it).
//
// FROM BUS EVENTS:
//   - response_latency (HEADLINE) = (first FirstAudio for the turn's TurnID).At −
//     STTFinal.SpeechEndAt. This is observe's exact derivation (#4 LOCKED seam):
//     keyed off STTFinal.SpeechEndAt, NOT a VADSpeechEnd lookup, and off the
//     FIRST FirstAudio per TurnID. The turn's TurnID comes from its STTFinal.
//   - address_detect = AddressRouted.At − STTFinal.At
//   - llm_turn       = first TTSInvoked.At − AddressRouted.At (route → first
//     sentence dispatched; the whole LLM turn incl. tool rounds)
//
// NOT FROM BUS EVENTS — captured via the StageRecorder tap instead (see
// recorderTap), because A3 emits them as recorder calls, not bus events:
//   - llm_round (observe.StageRecorder.LLMRound, per Provider.Complete)
//   - vad_hangover, stt_request, tts_ttfb/total, codec_* (recorder-only).
//     vad_hangover specifically is the fixed minSilenceFrames×32ms trailing
//     wait, NOT VADSpeechEnd−VADSpeechStart (that's utterance duration) — only
//     the recorder knows it.
func extractTurn(events []voiceevent.Event) turnSpans {
	spans := turnSpans{}

	var sttFinal, addrRouted, firstTTS, speechEnd time.Time
	var turnID string
	var firstAudio time.Time
	var haveSTT, haveAddr, haveTTS, haveSpeechEnd, haveFirstAudio bool

	for _, e := range events {
		switch ev := e.(type) {
		case voiceevent.STTFinal:
			sttFinal, haveSTT = ev.At, true
			turnID = ev.TurnID
			if !ev.SpeechEndAt.IsZero() {
				speechEnd, haveSpeechEnd = ev.SpeechEndAt, true
			}
		case voiceevent.AddressRouted:
			addrRouted, haveAddr = ev.At, true
		case voiceevent.TTSInvoked:
			if !haveTTS { // first sentence dispatched this turn
				firstTTS, haveTTS = ev.At, true
			}
		case voiceevent.FirstAudio:
			// First FirstAudio matching this turn's TurnID closes the headline
			// span. Guard on TurnID so a stray cross-turn event can't bleed in.
			if !haveFirstAudio && (turnID == "" || ev.TurnID == turnID) {
				firstAudio, haveFirstAudio = ev.At, true
			}
		}
	}

	if haveFirstAudio && haveSpeechEnd {
		spans[StageResponseLatency] = firstAudio.Sub(speechEnd)
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
