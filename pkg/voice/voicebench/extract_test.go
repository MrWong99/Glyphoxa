package voicebench_test

import (
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/voicebench"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// at returns a base time offset by ms, so a synthetic event log has realistic
// monotonically-increasing At: stamps.
func at(ms int) time.Time {
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(ms) * time.Millisecond)
}

// TestAccumulator_BusDerivedStages pins the stages the bench derives from the
// bus event log, reconciled 1:1 with observe's A3 subscriber:
//   - response_latency (HEADLINE) = FirstAudio.At − STTFinal.SpeechEndAt, keyed
//     by TurnID off the FIRST FirstAudio (observe's exact derivation).
//   - address_detect = AddressRouted.At − STTFinal.At
//   - llm_turn       = first TTSInvoked.At − AddressRouted.At
//
// llm_round and vad_hangover are recorder-only (not bus events) so they must be
// ABSENT from the bus-derived report — they arrive via the recorderTap, and
// vad_hangover specifically is NOT VADSpeechEnd−VADSpeechStart (that's utterance
// duration).
func TestAccumulator_BusDerivedStages(t *testing.T) {
	// SpeechEndAt@500, first FirstAudio@1400 (response_latency 900). stt.final@700,
	// address.routed@760 (address_detect 60), first tts.invoked@1300 (llm_turn 540).
	events := []voiceevent.Event{
		voiceevent.VADSpeechStart{At: at(100)},
		voiceevent.VADSpeechEnd{At: at(600)},
		voiceevent.STTFinal{At: at(700), TurnID: "turn-1", SpeechEndAt: at(500)},
		voiceevent.AddressRouted{At: at(760), TurnID: "turn-1"},
		voiceevent.TTSInvoked{At: at(1300), Index: 0, TurnID: "turn-1"},
		voiceevent.FirstAudio{At: at(1400), TurnID: "turn-1"},
		voiceevent.FirstAudio{At: at(1600), TurnID: "turn-1"}, // later chunks ignored
		voiceevent.TTSInvoked{At: at(1500), Index: 1},         // later sentences ignored for llm_turn
	}

	acc := voicebench.NewAccumulator("cassette", []string{"trivial"})
	acc.AddTurn(events)
	r := acc.Build()

	want := map[voicebench.Stage]float64{
		voicebench.StageResponseLatency: 900, // 1400 − 500
		voicebench.StageAddressDetect:   60,
		voicebench.StageLLMTurn:         540,
	}
	for stage, ms := range want {
		d, ok := r.Stages[stage]
		if !ok {
			t.Errorf("stage %q missing from report", stage)
			continue
		}
		if d.P50 != ms {
			t.Errorf("stage %q p50 = %v ms, want %v ms", stage, d.P50, ms)
		}
	}
	// Recorder-only stages must be absent from the bus-derived report.
	for _, recorderOnly := range []voicebench.Stage{
		voicebench.StageLLMRound,
		voicebench.StageVADHangover,
	} {
		if _, ok := r.Stages[recorderOnly]; ok {
			t.Errorf("stage %q present in bus-derived report; it is recorder-only", recorderOnly)
		}
	}
	if r.N != 1 || r.Tier != "cassette" {
		t.Errorf("report header = N %d tier %q, want 1/cassette", r.N, r.Tier)
	}
}

// TestAccumulator_ResponseLatency_NeedsBothEnds pins the headline span's guard:
// a turn missing SpeechEndAt (STT didn't stamp it) or missing FirstAudio (no
// audio reached the pump) yields NO response_latency sample — never a wrong
// number from a half-present span.
func TestAccumulator_ResponseLatency_NeedsBothEnds(t *testing.T) {
	// FirstAudio present but STTFinal has no SpeechEndAt → no span.
	noSpeechEnd := voicebench.NewAccumulator("cassette", nil)
	noSpeechEnd.AddTurn([]voiceevent.Event{
		voiceevent.STTFinal{At: at(700), TurnID: "t"},
		voiceevent.FirstAudio{At: at(1400), TurnID: "t"},
	})
	if _, ok := noSpeechEnd.Build().Stages[voicebench.StageResponseLatency]; ok {
		t.Error("response_latency present with no SpeechEndAt; want absent")
	}
	// SpeechEndAt present but no FirstAudio → no span.
	noAudio := voicebench.NewAccumulator("cassette", nil)
	noAudio.AddTurn([]voiceevent.Event{
		voiceevent.STTFinal{At: at(700), TurnID: "t", SpeechEndAt: at(500)},
	})
	if _, ok := noAudio.Build().Stages[voicebench.StageResponseLatency]; ok {
		t.Error("response_latency present with no FirstAudio; want absent")
	}
}

// TestAccumulator_AggregatesAcrossTurns pins that the accumulator pools samples
// across replays before reducing — the distribution is over N turns, not one.
func TestAccumulator_AggregatesAcrossTurns(t *testing.T) {
	acc := voicebench.NewAccumulator("cassette", nil)
	// Three turns with address_detect = 10, 20, 30 ms.
	for _, d := range []int{10, 20, 30} {
		acc.AddTurn([]voiceevent.Event{
			voiceevent.STTFinal{At: at(0)},
			voiceevent.AddressRouted{At: at(d)},
		})
	}
	r := acc.Build()
	got := r.Stages[voicebench.StageAddressDetect]
	if got.N != 3 || got.P50 != 20 || got.Max != 30 {
		t.Errorf("address_detect over 3 turns = %+v, want N3 p50 20 max 30", got)
	}
	if r.N != 3 {
		t.Errorf("report N = %d, want 3 turns", r.N)
	}
}
