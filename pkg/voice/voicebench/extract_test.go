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

// TestAccumulator_DerivableStages pins the stages the bench computes TODAY from
// existing bus timestamps — the boundaries that must stay reconciled with A3's
// subscriber (#4): address_detect and llm_turn. One synthetic turn with known
// offsets yields exact spans. vad_hangover, response_latency and llm_round are
// NOT derivable from today's events (vad_hangover would be utterance duration,
// not the trailing-silence wait; the other two need #4's hooks) so they must be
// absent — Check skips them rather than asserting a wrong number.
func TestAccumulator_DerivableStages(t *testing.T) {
	// stt.final@700, address.routed@760 (address_detect 60), first
	// tts.invoked@1300 (llm_turn 540 from route). VAD events are present but do
	// NOT produce a hangover span — that's the whole point of this assertion.
	events := []voiceevent.Event{
		voiceevent.VADSpeechStart{At: at(100)},
		voiceevent.VADSpeechEnd{At: at(600)},
		voiceevent.STTFinal{At: at(700)},
		voiceevent.AddressRouted{At: at(760)},
		voiceevent.TTSInvoked{At: at(1300), Index: 0},
		voiceevent.TTSInvoked{At: at(1500), Index: 1}, // later sentences ignored for llm_turn
	}

	acc := voicebench.NewAccumulator("cassette", []string{"trivial"})
	acc.AddTurn(events)
	r := acc.Build()

	want := map[voicebench.Stage]float64{
		voicebench.StageAddressDetect: 60,
		voicebench.StageLLMTurn:       540,
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
	// Stages that need #4's hooks (or a hangover derivation we don't have) must
	// be absent, not a misleading zero/duration.
	for _, notYet := range []voicebench.Stage{
		voicebench.StageResponseLatency,
		voicebench.StageLLMRound,
		voicebench.StageVADHangover,
	} {
		if _, ok := r.Stages[notYet]; ok {
			t.Errorf("stage %q present before its A3 derivation landed; want absent", notYet)
		}
	}
	if r.N != 1 || r.Tier != "cassette" {
		t.Errorf("report header = N %d tier %q, want 1/cassette", r.N, r.Tier)
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
