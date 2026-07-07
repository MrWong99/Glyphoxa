package orchestrator_test

import (
	"context"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicetest"
)

// gate is a fixed [orchestrator.TurnGate] for the spend-cap gate tests.
type gate bool

func (g gate) AllowTurn() bool { return bool(g) }

// TestReplier_SpendCapRefusesNewTurn pins the spend-cap pre-check (#130, AC3): a
// gate that refuses discards the route BEFORE Floor.Take, so it opens no turn (its
// producer never runs) and never disturbs whoever holds the floor — a
// TurnEnded(spend_cap) for the routed TurnID is announced.
func TestReplier_SpendCapRefusesNewTurn(t *testing.T) {
	h := voicetest.New(t)
	ttsStage := orchestrator.NewTTS(h.Bus, selectiveSynth{})

	ran := make(chan string, 2)
	reply := func(_ context.Context, e voiceevent.AddressRouted, _ func(orchestrator.Reply) error) error {
		ran <- e.TurnID
		return nil
	}

	floor := orchestrator.NewFloor()
	// Greta is already speaking (holds the floor) — the refused route must not touch it.
	gretaParent := voiceevent.WithTurnID(context.Background(), "Tgreta")
	gretaCtx, gretaRelease, _ := floor.Take(gretaParent, "greta")
	defer gretaRelease()

	replier := orchestrator.NewStreamReplier(ttsStage, reply, nil)
	replier.SetFloor(floor)
	replier.SetGate(gate(false)) // over budget: refuse new turns
	t.Cleanup(replier.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.AddressRouted{
		TurnID: "Tbart", Text: "Bart, a room",
		Target: voiceevent.AddressTarget{AgentID: "bart", AgentRole: "character", Name: "Bart"},
	})

	select {
	case id := <-ran:
		t.Fatalf("a spend-capped turn's producer ran for %q — the route must be discarded before Floor.Take", id)
	case <-time.After(100 * time.Millisecond):
		// Correct: no producer for the refused route.
	}
	if gretaCtx.Err() != nil {
		t.Fatal("a spend-cap refusal must not disturb the Agent holding the floor")
	}
	if !floor.Active() {
		t.Fatal("Greta must keep the floor after a spend-capped route was discarded")
	}
	voicetest.AssertEvent(t, h,
		func(e voiceevent.TurnEnded) bool {
			return e.TurnID == "Tbart" && e.Reason == voiceevent.TurnEndSpendCap
		},
		"turn.ended (spend_cap) for the discarded route",
	)
}

// TestReplier_SpendCapAllowsUnderBudget proves a gate that allows lets the turn
// proceed: its producer runs.
func TestReplier_SpendCapAllowsUnderBudget(t *testing.T) {
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
	replier.SetGate(gate(true)) // under budget
	t.Cleanup(replier.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.AddressRouted{
		TurnID: "Tok", Text: "Bart, a room",
		Target: voiceevent.AddressTarget{AgentID: "bart", AgentRole: "character", Name: "Bart"},
	})

	select {
	case id := <-ran:
		if id != "Tok" {
			t.Fatalf("producer ran for %q, want Tok", id)
		}
	case <-time.After(time.Second):
		t.Fatal("an under-budget turn's producer must run")
	}
}

// TestReplier_NilGateUntouched proves a nil gate is the feature-off default: the
// turn proceeds exactly as before, byte-for-byte.
func TestReplier_NilGateUntouched(t *testing.T) {
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
	// No gate set (nil) — feature off.
	t.Cleanup(replier.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.AddressRouted{
		TurnID: "Tnil", Text: "Bart, a room",
		Target: voiceevent.AddressTarget{AgentID: "bart", AgentRole: "character", Name: "Bart"},
	})

	select {
	case <-ran:
		// Correct: proceeds with no gate.
	case <-time.After(time.Second):
		t.Fatal("with no gate the turn must proceed")
	}
}
