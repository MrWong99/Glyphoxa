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

// TestBargeIn_AttributesCutAgent pins the ADR-0027 amendment's carrier: a
// confirmed barge names the Agent it cut, not just the speaker who cut it.
// Without the attribution the routing layer cannot tell WHICH Agent the human
// wanted to stop.
func TestBargeIn_AttributesCutAgent(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	parent := voiceevent.WithTurnID(context.Background(), "T1")
	_, release, _ := floor.Take(parent, "npc-bart")
	defer release()

	t.Cleanup(orchestrator.NewBargeIn(floor, 0).Bind(t.Context(), h.Bus))
	h.Bus.Publish(voiceevent.FirstOpus{TurnID: "T1"})
	h.Bus.Publish(voiceevent.VADSpeechStart{SpeakerID: "player-1"})

	voicetest.AssertEvent(t, h,
		func(e voiceevent.BargeDetected) bool { return e.AgentID == "npc-bart" && e.SpeakerID == "player-1" },
		"barge.detected naming both the barging speaker and the cut Agent",
	)
}

// TestBargeIn_AttributesRetargetedHolder pins that the attribution is read by
// the yield ITSELF, not captured when the confirm window armed: an Ensemble Lead
// election ([orchestrator.Floor.SetHolderAgent]) may retarget the holder under an
// unchanged TurnID mid-window, and naming the pre-election Agent would revoke the
// continuation of an Agent nobody interrupted.
func TestBargeIn_AttributesRetargetedHolder(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	parent := voiceevent.WithTurnID(context.Background(), "T1")
	turnCtx, release, _ := floor.Take(parent, "npc-anchor")
	defer release()

	t.Cleanup(orchestrator.NewBargeIn(floor, 20*time.Millisecond).Bind(t.Context(), h.Bus))
	h.Bus.Publish(voiceevent.FirstOpus{TurnID: "T1"})
	h.Bus.Publish(voiceevent.VADSpeechStart{SpeakerID: "player-1"}) // arms against npc-anchor

	floor.SetHolderAgent("T1", "npc-lead") // the Lead election lands inside the window

	select {
	case <-turnCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("speech crossing the confirm window must cancel the turn")
	}
	voicetest.AssertEvent(t, h,
		func(e voiceevent.BargeDetected) bool { return e.AgentID == "npc-lead" },
		"barge.detected naming the Agent that actually held the floor at cut time",
	)
}

// clearSpyMatcher is a [orchestrator.TargetMatcher] that also implements
// [orchestrator.InterruptibleMatcher], recording which Agents were cleared.
type clearSpyMatcher struct {
	mu      sync.Mutex
	cleared []string
	routes  []voiceevent.AddressRouted
}

func (m *clearSpyMatcher) TargetMatch(string) []voiceevent.AddressRouted { return m.routes }

func (m *clearSpyMatcher) ClearContinuation(agentID string) {
	m.mu.Lock()
	m.cleared = append(m.cleared, agentID)
	m.mu.Unlock()
}

func (m *clearSpyMatcher) clearedIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.cleared...)
}

// TestAddressDetector_BargeClearsContinuation pins the wiring that closes the
// live "the NPCs answer far too often" loop: an interruption revokes the cut
// Agent's continuation claim, so the interrupting utterance — itself a routable
// final moments later — does not land straight back on the Agent the human just
// cut off.
func TestAddressDetector_BargeClearsContinuation(t *testing.T) {
	h := voicetest.New(t)
	m := &clearSpyMatcher{}
	t.Cleanup(orchestrator.NewAddressDetector(m).Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.BargeDetected{SpeakerID: "player-1", AgentID: "npc-bart"})

	if got := m.clearedIDs(); len(got) != 1 || got[0] != "npc-bart" {
		t.Fatalf("cleared = %v, want [npc-bart]", got)
	}
}

// TestAddressDetector_UnattributedBargeClearsNothing pins the fail-safe: a barge
// that could not name its Agent (empty AgentID — e.g. a holder taken with no
// target) must revoke nothing rather than guess, since guessing would silence an
// Agent nobody interrupted.
func TestAddressDetector_UnattributedBargeClearsNothing(t *testing.T) {
	h := voicetest.New(t)
	m := &clearSpyMatcher{}
	t.Cleanup(orchestrator.NewAddressDetector(m).Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.BargeDetected{SpeakerID: "player-1"})

	if got := m.clearedIDs(); len(got) != 0 {
		t.Fatalf("cleared = %v, want nothing", got)
	}
}

// TestAddressDetector_BargeUnsubscribesOnCancel pins teardown: the barge
// subscription is removed with the rest of the detector's, so a torn-down Voice
// Session cannot keep mutating a matcher it no longer routes for.
func TestAddressDetector_BargeUnsubscribesOnCancel(t *testing.T) {
	h := voicetest.New(t)
	m := &clearSpyMatcher{}
	cancel := orchestrator.NewAddressDetector(m).Bind(t.Context(), h.Bus)
	cancel()

	h.Bus.Publish(voiceevent.BargeDetected{SpeakerID: "player-1", AgentID: "npc-bart"})

	if got := m.clearedIDs(); len(got) != 0 {
		t.Fatalf("cleared = %v after teardown, want nothing", got)
	}
}
