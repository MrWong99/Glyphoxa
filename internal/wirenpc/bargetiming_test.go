package wirenpc

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// These tests relate the production barge and VAD constants (#431): the
// Silero end-of-speech hangover (vadMinSilenceFrames × vadFrameMs = 384 ms)
// is LONGER than the Barge-in Confirm Window (bargeConfirmWindow = 250 ms),
// so the segment-final VADSpeechEnd for ANY burst arrives after the window's
// timer has fired — a reactor that disarms only on speech_end can never save
// a backchannel, and Soft-overlap (CONTEXT.md) is unreachable. The disarm
// must ride the provisional VADVoicingStopped, which the VAD emits one frame
// after voicing actually stops. The tests replay that production event timing
// in real time against a real BargeIn, so a regression back to
// "disarm only on speech_end" — or a constants change that silently
// re-creates the trap — fails here.

// prodHangover is the end-of-speech detection lag wirenpc configures Silero
// with; prodFrame is one VAD frame, the provisional stop's detection lag.
const (
	prodHangover = vadMinSilenceFrames * vadFrameMs * time.Millisecond
	prodFrame    = vadFrameMs * time.Millisecond
)

// TestBargeTiming_SoftOverlapReachableUnderProductionHangover replays a short
// backchannel ("mhm", ~100 ms voiced) over an audibly-speaking Agent with the
// event timing production produces: speech_start at onset, voicing_stopped
// one frame after the voicing ends, speech_end only after the full hangover —
// i.e. well AFTER the confirm window would have fired. The turn must survive
// and no BargeDetected may be announced.
func TestBargeTiming_SoftOverlapReachableUnderProductionHangover(t *testing.T) {
	// The premises that make this scenario THE production trap. If a constants
	// change breaks either, this test must be revisited deliberately rather
	// than silently keep passing a weaker scenario.
	const burst = 100 * time.Millisecond // voiced duration of the backchannel
	if prodHangover <= bargeConfirmWindow {
		t.Fatalf("premise changed: hangover %v ≤ confirm window %v — speech_end can now disarm the window itself; revisit this test and the VADVoicingStopped disarm (#431)",
			prodHangover, bargeConfirmWindow)
	}
	if burst+prodFrame >= bargeConfirmWindow {
		t.Fatalf("premise broken: the provisional stop (%v) would land after the window (%v) — pick a shorter burst",
			burst+prodFrame, bargeConfirmWindow)
	}
	if burst+prodHangover <= bargeConfirmWindow {
		t.Fatalf("premise broken: the speech_end (%v) would land inside the window (%v), so the test would not exercise the hangover trap",
			burst+prodHangover, bargeConfirmWindow)
	}

	bus := voiceevent.NewBus()
	var barges atomic.Int32
	voiceevent.On(bus, func(voiceevent.BargeDetected) { barges.Add(1) })

	floor := orchestrator.NewFloor()
	parent := voiceevent.WithTurnID(context.Background(), "T1")
	turnCtx, release, _ := floor.Take(parent, "bart")
	defer release()
	t.Cleanup(orchestrator.NewBargeIn(floor, bargeConfirmWindow).Bind(context.Background(), bus))

	bus.Publish(voiceevent.FirstOpus{TurnID: "T1"}) // the Agent is audibly speaking

	bus.Publish(voiceevent.VADSpeechStart{At: time.Now()}) // backchannel onset: window arms
	time.Sleep(burst + prodFrame)                          // Silero flags the pause one frame after voicing stops…
	bus.Publish(voiceevent.VADVoicingStopped{At: time.Now()})
	time.Sleep(prodHangover - prodFrame) // …and leaves the speaking state only after the full hangover.
	bus.Publish(voiceevent.VADSpeechEnd{At: time.Now()})

	time.Sleep(bargeConfirmWindow + 100*time.Millisecond) // well past any still-armed window
	if turnCtx.Err() != nil {
		t.Fatal("a backchannel shorter than the confirm window cancelled the Agent under production timing: Soft-overlap is unreachable again (#431)")
	}
	if n := barges.Load(); n != 0 {
		t.Fatalf("BargeDetected fired %d time(s) for a sub-window backchannel, want 0", n)
	}
}

// TestBargeTiming_SustainedInterruptionFiresNearWindow is the counterpart: a
// participant who keeps voicing past the confirm window still barges, with
// latency close to the window (ADR-0027) — the soft-overlap fix must not have
// deferred the decision to the hangover.
func TestBargeTiming_SustainedInterruptionFiresNearWindow(t *testing.T) {
	bus := voiceevent.NewBus()
	floor := orchestrator.NewFloor()
	parent := voiceevent.WithTurnID(context.Background(), "T1")
	turnCtx, release, _ := floor.Take(parent, "bart")
	defer release()
	t.Cleanup(orchestrator.NewBargeIn(floor, bargeConfirmWindow).Bind(context.Background(), bus))

	bus.Publish(voiceevent.FirstOpus{TurnID: "T1"})
	start := time.Now()
	bus.Publish(voiceevent.VADSpeechStart{At: start}) // continuous voicing: no stop, no end

	select {
	case <-turnCtx.Done():
	case <-time.After(bargeConfirmWindow + 2*time.Second):
		t.Fatal("continuous voiced speech past the confirm window must cancel the Agent")
	}
	latency := time.Since(start)
	if latency < bargeConfirmWindow-20*time.Millisecond {
		t.Errorf("barge fired after %v, before the %v confirm window elapsed", latency, bargeConfirmWindow)
	}
	// Generous scheduling slack: the property is "≈ window", i.e. NOT deferred
	// by anything like the 384 ms hangover on top.
	if latency > bargeConfirmWindow+prodHangover/2 {
		t.Errorf("barge latency %v is far past the %v confirm window — the decision looks deferred to the hangover", latency, bargeConfirmWindow)
	}
}
