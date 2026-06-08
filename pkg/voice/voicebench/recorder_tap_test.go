package voicebench

import (
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/observe"
)

// TestRecorderTap_CapturesRecorderOnlyStages pins that the tap, driven through
// the REAL observe.StageRecorder interface the orchestrator emits on, captures
// the recorder-only stages (llm_round is the B2 signal; vad_hangover/stt_request
// are not bus events). Each recorded duration must land under its locked Stage,
// so a sample read here is the same value the Prometheus adapter would observe.
func TestRecorderTap_CapturesRecorderOnlyStages(t *testing.T) {
	var rec observe.StageRecorder = newRecorderTap()
	tap := rec.(*recorderTap)

	// Two LLM rounds in one turn (the H2 tool-loop shape) + the fixed hangover.
	// Use observe's bounded enum constants, not bare strings (ADR-0032 §2.1).
	rec.LLMRound(observe.ProviderGemini, 0, false, 420*time.Millisecond)
	rec.LLMRound(observe.ProviderGemini, 1, true, 380*time.Millisecond)
	rec.VADHangover(256 * time.Millisecond)
	rec.STTRequest(observe.ProviderElevenLabs, 300*time.Millisecond)

	if got := tap.samples(StageLLMRound); len(got) != 2 {
		t.Errorf("llm_round samples = %d, want 2", len(got))
	}
	if got := tap.samples(StageVADHangover); len(got) != 1 || got[0] != 256*time.Millisecond {
		t.Errorf("vad_hangover samples = %v, want one 256ms", got)
	}
	if got := tap.samples(StageSTTRequest); len(got) != 1 {
		t.Errorf("stt_request samples = %d, want 1", len(got))
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
