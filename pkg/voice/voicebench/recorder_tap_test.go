package voicebench

import (
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/observe"
)

// TestRecorderTap_CapturesWiredStages pins what the tap, driven through the REAL
// observe.StageRecorder interface, captures. Two emit paths record onto it with
// no overlap: the agenttool adapter (llm_round — the B2 signal) and observe's
// StageSubscriber (the bus-derived response_latency/address_detect/tts_ttfb,
// installed on the bus by the rig). Both are the single emitter for their span,
// so a captured sample is the same value the Prometheus adapter observes — no
// double-count. The genuinely-unwired stages (vad_hangover/stt_request/…,
// carry-over #11) have no caller yet and remain no-ops.
func TestRecorderTap_CapturesWiredStages(t *testing.T) {
	var rec observe.StageRecorder = newRecorderTap()
	tap := rec.(*recorderTap)

	// Adapter span (owned by the tap): two LLM rounds in one turn (H2 tool-loop).
	rec.LLMRound(observe.ProviderGemini, 0, false, 420*time.Millisecond)
	rec.LLMRound(observe.ProviderGemini, 1, true, 380*time.Millisecond)
	// Bus-derived spans the subscriber records onto this tap (single emitter each).
	rec.ResponseLatency(observe.RoleCharacter, 900*time.Millisecond)
	rec.AddressDetect(60 * time.Millisecond)
	rec.TTSTimeToFirstByte(observe.ProviderElevenLabs, 120*time.Millisecond)
	// Unwired stages: still no-ops (no emitter records them yet, #11).
	rec.VADHangover(256 * time.Millisecond)
	rec.STTRequest(observe.ProviderElevenLabs, 300*time.Millisecond)

	for stage, want := range map[Stage]int{
		StageLLMRound:        2,
		StageResponseLatency: 1,
		StageAddressDetect:   1,
		StageTTSTTFB:         1,
	} {
		if got := len(tap.samples(stage)); got != want {
			t.Errorf("tap %q samples = %d, want %d", stage, got, want)
		}
	}
	for _, unwired := range []Stage{StageVADHangover, StageSTTRequest} {
		if got := tap.samples(unwired); len(got) != 0 {
			t.Errorf("tap captured unwired %q (%d samples); it has no emitter yet (#11)", unwired, len(got))
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
