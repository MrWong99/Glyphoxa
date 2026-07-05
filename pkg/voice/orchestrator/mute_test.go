package orchestrator_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicetest"
)

// muteSet is a fixed-membership [orchestrator.MuteView] for the gate tests.
type muteSet map[string]bool

func (m muteSet) Muted(agentID string) bool { return m[agentID] }

// flipMute is a [orchestrator.MuteView] that reports target NOT muted until the
// nth Muted call, then muted — modelling the mute view flipping between the
// replier's pre-Take check and its post-Take double-check (the race the airtight
// closure must catch).
type flipMute struct {
	mu          sync.Mutex
	calls       int
	target      string
	mutedAtCall int
}

func (f *flipMute) Muted(agentID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return agentID == f.target && f.calls >= f.mutedAtCall
}

// TestMuteCut_CutsHolderWithMuteReason pins the mute cut (#211, AC2): a
// MuteChanged{Muted:true} for the Agent currently holding the floor cancels its
// turn and announces TurnEnded with the distinct mute reason — and NEVER a
// BargeDetected (Barge-in is strictly the human-interrupts-Agent case).
func TestMuteCut_CutsHolderWithMuteReason(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	parent := voiceevent.WithTurnID(context.Background(), "Tm")
	turnCtx, release, _ := floor.Take(parent, "bart")
	defer release()

	t.Cleanup(orchestrator.NewMuteCut(floor).Bind(t.Context(), h.Bus))
	h.Bus.Publish(voiceevent.MuteChanged{AgentID: "bart", Muted: true})

	if turnCtx.Err() == nil {
		t.Fatal("muting the speaking Agent must cancel its turn ctx")
	}
	if floor.Active() {
		t.Fatal("muting the speaking Agent must free the floor")
	}
	voicetest.AssertEvent(t, h,
		func(e voiceevent.TurnEnded) bool { return e.TurnID == "Tm" && e.Reason == voiceevent.TurnEndMute },
		"turn.ended (mute) carrying the cut turn's TurnID",
	)
	voicetest.AssertNoEvent[voiceevent.BargeDetected](t, h)
}

// TestMuteCut_IgnoresNonHolder proves muting an Agent that is NOT speaking never
// disturbs whoever holds the floor (AC3): no cut, no TurnEnded.
func TestMuteCut_IgnoresNonHolder(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	parent := voiceevent.WithTurnID(context.Background(), "Tb")
	turnCtx, release, _ := floor.Take(parent, "bart")
	defer release()

	t.Cleanup(orchestrator.NewMuteCut(floor).Bind(t.Context(), h.Bus))
	h.Bus.Publish(voiceevent.MuteChanged{AgentID: "greta", Muted: true})

	if turnCtx.Err() != nil {
		t.Fatal("muting a non-holder must not cancel the current holder's turn")
	}
	if !floor.Active() {
		t.Fatal("muting a non-holder must leave the current holder on the floor")
	}
	voicetest.AssertNoEvent[voiceevent.TurnEnded](t, h)
}

// TestMuteCut_UnmuteIsNoOp proves an unmute (Muted:false) does nothing in the
// cut reactor — restoring an Agent is the matcher/route path's job, not a floor
// cut. The speaking Agent keeps its floor.
func TestMuteCut_UnmuteIsNoOp(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	parent := voiceevent.WithTurnID(context.Background(), "Tu")
	turnCtx, release, _ := floor.Take(parent, "bart")
	defer release()

	t.Cleanup(orchestrator.NewMuteCut(floor).Bind(t.Context(), h.Bus))
	h.Bus.Publish(voiceevent.MuteChanged{AgentID: "bart", Muted: false})

	if turnCtx.Err() != nil {
		t.Fatal("an unmute must not cancel any turn")
	}
	if !floor.Active() {
		t.Fatal("an unmute must leave the holder on the floor")
	}
	voicetest.AssertNoEvent[voiceevent.TurnEnded](t, h)
}

// TestReplier_MutedAddresseeNeverTakesFloor pins the pre-Take gate (#211, AC3): a
// route to a muted Agent is discarded before Floor.Take, so it opens no turn (its
// producer never runs) and never disturbs whoever holds the floor — a
// TurnEnded(mute) for the routed TurnID is announced.
func TestReplier_MutedAddresseeNeverTakesFloor(t *testing.T) {
	h := voicetest.New(t)
	ttsStage := orchestrator.NewTTS(h.Bus, selectiveSynth{})

	ran := make(chan string, 2)
	reply := func(_ context.Context, e voiceevent.AddressRouted, _ func(orchestrator.Reply) error) error {
		ran <- e.TurnID
		return nil
	}

	floor := orchestrator.NewFloor()
	// Greta is already speaking (holds the floor) — the muted route must not touch it.
	gretaParent := voiceevent.WithTurnID(context.Background(), "Tgreta")
	gretaCtx, gretaRelease, _ := floor.Take(gretaParent, "greta")
	defer gretaRelease()

	replier := orchestrator.NewStreamReplier(ttsStage, reply, nil)
	replier.SetFloor(floor)
	replier.SetMutes(muteSet{"bart": true})
	t.Cleanup(replier.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.AddressRouted{
		TurnID: "Tbart", Text: "Bart, a room",
		Target: voiceevent.AddressTarget{AgentID: "bart", AgentRole: "character", Name: "Bart"},
	})

	select {
	case id := <-ran:
		t.Fatalf("a muted addressee's producer ran for %q — the route must be discarded before Floor.Take", id)
	case <-time.After(100 * time.Millisecond):
		// Correct: no producer for the muted route.
	}
	if gretaCtx.Err() != nil {
		t.Fatal("a muted addressee must not disturb the Agent holding the floor (AC3)")
	}
	if !floor.Active() {
		t.Fatal("Greta must keep the floor after a muted route was discarded")
	}
	voicetest.AssertEvent(t, h,
		func(e voiceevent.TurnEnded) bool { return e.TurnID == "Tbart" && e.Reason == voiceevent.TurnEndMute },
		"turn.ended (mute) for the discarded muted route",
	)
}

// TestReplier_MutePostTakeDoubleCheckReleases pins the race closure (#211): if the
// mute view flips to muted AFTER the pre-Take check but the Take already
// succeeded, the post-Take double-check releases the floor and ends the turn with
// the mute reason — no goroutine, no TTS.
func TestReplier_MutePostTakeDoubleCheckReleases(t *testing.T) {
	h := voicetest.New(t)
	ttsStage := orchestrator.NewTTS(h.Bus, selectiveSynth{})

	ran := make(chan string, 2)
	reply := func(_ context.Context, e voiceevent.AddressRouted, _ func(orchestrator.Reply) error) error {
		ran <- e.TurnID
		return nil
	}

	floor := orchestrator.NewFloor()
	replier := orchestrator.NewStreamReplier(ttsStage, reply, nil)
	replier.SetFloor(floor)
	// Not muted on the pre-Take check (call 1), muted on the post-Take check (call 2).
	replier.SetMutes(&flipMute{target: "bart", mutedAtCall: 2})
	t.Cleanup(replier.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.AddressRouted{
		TurnID: "Trace", Text: "Bart, a room",
		Target: voiceevent.AddressTarget{AgentID: "bart", AgentRole: "character", Name: "Bart"},
	})

	select {
	case id := <-ran:
		t.Fatalf("the turn's producer ran for %q — a mute flipping after Take must still stop it (no TTS)", id)
	case <-time.After(100 * time.Millisecond):
		// Correct: the double-check released the floor before any producer ran.
	}
	if floor.Active() {
		t.Fatal("the post-Take double-check must release the floor when the Agent is muted")
	}
	voicetest.AssertEvent(t, h,
		func(e voiceevent.TurnEnded) bool { return e.TurnID == "Trace" && e.Reason == voiceevent.TurnEndMute },
		"turn.ended (mute) for the post-Take-muted turn",
	)
}
