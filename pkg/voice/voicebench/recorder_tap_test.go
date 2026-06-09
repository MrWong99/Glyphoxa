package voicebench

import (
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/observe"
)

// TestRecorderTap_CapturesRecorderOnlyStages pins that the tap, driven through
// the REAL observe.StageRecorder interface the orchestrator emits on, captures
// ONLY the agenttool-adapter span the tap owns (llm_round — the B2 signal), and
// does NOT capture the bus-owned stages (response_latency/address_detect/tts_ttfb
// come from observe's bus subscriber, not the injected recorder) so they can't
// be double-counted. A captured sample is the same value the Prometheus adapter
// would observe.
func TestRecorderTap_OwnsOnlyAdapterSpans(t *testing.T) {
	var rec observe.StageRecorder = newRecorderTap()
	tap := rec.(*recorderTap)

	// Two LLM rounds in one turn (the H2 tool-loop shape) — owned by the tap.
	rec.LLMRound(observe.ProviderGemini, 0, false, 420*time.Millisecond)
	rec.LLMRound(observe.ProviderGemini, 1, true, 380*time.Millisecond)
	// Bus-owned / unwired stages: calling them must be no-ops here (no double-count).
	rec.ResponseLatency(observe.RoleCharacter, 900*time.Millisecond)
	rec.AddressDetect(60 * time.Millisecond)
	rec.TTSTimeToFirstByte(observe.ProviderElevenLabs, 120*time.Millisecond)
	rec.VADHangover(256 * time.Millisecond)
	rec.STTRequest(observe.ProviderElevenLabs, 300*time.Millisecond)

	if got := tap.samples(StageLLMRound); len(got) != 2 {
		t.Errorf("llm_round samples = %d, want 2", len(got))
	}
	for _, notOwned := range []Stage{
		StageResponseLatency, StageAddressDetect, StageTTSTTFB,
		StageVADHangover, StageSTTRequest,
	} {
		if got := tap.samples(notOwned); len(got) != 0 {
			t.Errorf("tap captured %q (%d samples); it must leave that to the bus/unwired path", notOwned, len(got))
		}
	}

	d := Summarize(tap.samples(StageLLMRound))
	if d.N != 2 || d.Max != 420 {
		t.Errorf("llm_round dist = %+v, want N2 max 420ms", d)
	}
}

// TestRecorderTap_ProviderTally pins the provider call/error counters feed.
func TestRecorderTap_ProviderTally(t *testing.T) {
	tap := newRecorderTap()
	tap.ProviderCall(observe.StageLLM, observe.ProviderGemini, observe.OutcomeOK)
	tap.ProviderCall(observe.StageLLM, observe.ProviderGemini, observe.OutcomeError)
	tap.ProviderError(observe.StageLLM, observe.ProviderGemini)

	if tap.calls[observe.OutcomeOK] != 1 || tap.calls[observe.OutcomeError] != 1 {
		t.Errorf("call tally = %v, want one ok + one error", tap.calls)
	}
	if tap.errors != 1 {
		t.Errorf("error tally = %d, want 1", tap.errors)
	}
}
