package wirenpc

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// TestRosterDepsForLive_PersonaNameThreaded pins the wiring the self-label strip
// depends on: the NPC's display name reaches its [agent.Persona], so the Replier
// can recognize its own "Bart: " label in the model's output and drop it before
// the sentence is spoken. Unwired, the strip is silently inert in production
// while every unit test of it still passes.
func TestRosterDepsForLive_PersonaNameThreaded(t *testing.T) {
	synth := &recordingSynth{}
	engineFor := func(npcSpec) agent.Engine { return scriptEngine{line: "Bart: Aye, two rooms."} }
	log := slog.New(slog.DiscardHandler)

	deps := rosterDepsForLive(conversationDeps{log: log}, engineFor, synth, 16)
	r := deps.replierFor(specFor("npc-bart", "Bart", ""))

	route := voiceevent.AddressRouted{At: time.Now(), Text: "any rooms?",
		Target: voiceevent.AddressTarget{AgentID: "npc-bart", AgentRole: voiceevent.AgentRoleCharacter, Name: "Bart"}}
	var spoken []string
	if err := r.ReplyStream()(context.Background(), route, func(rep orchestrator.Reply) error {
		spoken = append(spoken, rep.Sentence)
		return nil
	}); err != nil {
		t.Fatalf("ReplyStream: %v", err)
	}

	if len(spoken) != 1 || spoken[0] != "Aye, two rooms." {
		t.Fatalf("spoken = %q, want [\"Aye, two rooms.\"] — the NPC spoke its own name label", spoken)
	}
}

// TestRoster_BargeClearsContinuation is the end-to-end pin for the live "the
// NPCs answer far too often" report, over the REAL Matcher and the REAL detector
// the production pipeline composes: after a human interrupts Aldra, the next
// unnamed utterance — the interrupting speech itself, in a live session — no
// longer routes back to her. Naming her still does.
func TestRoster_BargeClearsContinuation(t *testing.T) {
	bus := voiceevent.NewBus()
	synth := &recordingSynth{}
	specs := []npcSpec{specFor("aldra", "Aldra", ""), specFor("bram", "Bram", "")}
	roster := newRosterFor(specs, []*agent.Replier{
		replierFor(specs[0], "Aldra here.", synth),
		replierFor(specs[1], "Bram here.", synth),
	}, synth)

	detector := orchestrator.NewAddressDetector(roster.matcher)
	t.Cleanup(detector.Bind(context.Background(), bus))

	if got := routedTo(roster, "Aldra, what do you see?"); got != "aldra" {
		t.Fatalf("named utterance routed to %q, want aldra", got)
	}
	if got := routedTo(roster, "and beyond that?"); got != "aldra" {
		t.Fatalf("follow-up routed to %q, want aldra (continuation inside its window)", got)
	}

	// The human cuts Aldra off mid-sentence.
	bus.Publish(voiceevent.BargeDetected{At: time.Now(), SpeakerID: "player-1", AgentID: "aldra"})

	if got := routedTo(roster, "no wait, stop"); got != "" {
		t.Fatalf("post-barge utterance routed to %q, want nowhere — an interruption means stop, not continue", got)
	}
	if got := routedTo(roster, "Aldra, go on"); got != "aldra" {
		t.Fatalf("naming Aldra after the barge routed to %q, want aldra — the clear is not a mute", got)
	}
}

// TestRoster_BackchannelDoesNotOpenATurn pins the substance gate end-to-end: an
// acknowledgement noise reaches the Transcript but never opens an LLM+TTS turn.
// In a live four-player session these were multiplying the reply rate.
func TestRoster_BackchannelDoesNotOpenATurn(t *testing.T) {
	bus := voiceevent.NewBus()
	synth := &recordingSynth{}
	specs := []npcSpec{specFor("aldra", "Aldra", ""), specFor("bram", "Bram", "")}
	_, publish := testRoster(t, bus, synth, specs, map[string]string{"aldra": "Aldra here.", "bram": "Bram here."})

	publish("Aldra, what do you see?")
	if got := synth.spokenBy("aldra"); len(got) != 1 {
		t.Fatalf("named utterance produced %d turns, want 1", len(got))
	}

	// English vocalizations: this roster has no Campaign Language set, so the
	// Matcher runs the "en" vocabulary (the German list is covered by the
	// address-package tests, which construct a "de" Matcher directly). Note these
	// are noises, not one-word answers — "yes"/"okay" deliberately still route.
	for _, fill := range []string{"mhm", "hmm", "uh huh"} {
		publish(fill)
	}
	if got := synth.spokenBy("aldra"); len(got) != 1 {
		t.Fatalf("backchannels opened %d extra turns (%q), want none", len(got)-1, got)
	}

	// A substantive follow-up still continues the exchange.
	publish("and beyond the ridge?")
	if got := synth.spokenBy("aldra"); len(got) != 2 {
		t.Fatalf("substantive follow-up produced %d turns total, want 2", len(got))
	}
}
