package transcript

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// TestProjection_SayLineMatchesLLMTurn pins #295 (ADR-0012/0040): a GM /say lands in
// the transcript through the NORMAL SpeakRequested → TTSInvoked projection — no
// hand-crafted row — producing a Line byte-identical to an LLM turn's reply (ID
// "a:<turn>", NPC kind + pill, the Agent's name, the coalesced text).
func TestProjection_SayLineMatchesLLMTurn(t *testing.T) {
	bus, r, _, id := liveRelay(t)

	bus.Publish(voiceevent.SpeakRequested{
		At: at(1), TurnID: "s1", Text: "Welcome, travelers.",
		Target: voiceevent.AddressTarget{AgentID: "bart", AgentRole: "character", Name: "Bart"},
	})
	bus.Publish(voiceevent.TTSInvoked{At: at(2), Sentence: "Welcome, travelers.", Index: 0, TurnID: "s1"})

	v := r.View(id)
	if len(v.Lines) != 1 {
		t.Fatalf("got %d lines, want 1: %+v", len(v.Lines), v.Lines)
	}
	l := v.Lines[0]
	if l.ID != "a:s1" {
		t.Errorf("Line.ID = %q, want a:s1 (the same a:<turn> shape an LLM turn uses)", l.ID)
	}
	if l.Who != "Bart" || l.Kind != KindNPC || l.Tag != "NPC" {
		t.Errorf("say line meta = {who %q kind %q tag %q}, want {Bart npc NPC}", l.Who, l.Kind, l.Tag)
	}
	if l.Text != "Welcome, travelers." {
		t.Errorf("say line text = %q, want the verbatim /say text", l.Text)
	}
}

// TestProjection_SayPersistenceTeeFires pins that a /say line is teed onto the
// durable writer exactly like an LLM reply (ADR-0040) — no special persistence path.
func TestProjection_SayPersistenceTeeFires(t *testing.T) {
	bus := voiceevent.NewBus()
	fs := &fakeSessions{id: uuid.New(), active: true}
	store := newFakeLineStore()
	r := NewRelay(bus, fs, store, nil)

	bus.Publish(voiceevent.SpeakRequested{
		At: at(1), TurnID: "s1", Text: "Aye.",
		Target: voiceevent.AddressTarget{AgentID: "bart", AgentRole: "character", Name: "Bart"},
	})
	bus.Publish(voiceevent.TTSInvoked{At: at(2), Sentence: "Aye.", Index: 0, TurnID: "s1"})

	if _, err := r.Finalize(context.Background(), fs.id); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	got, _ := store.ListTranscriptLines(context.Background(), fs.id)
	if len(got) != 1 {
		t.Fatalf("persisted %d lines, want 1", len(got))
	}
	if got[0].LineID != "a:s1" || got[0].Who != "Bart" || got[0].Text != "Aye." {
		t.Errorf("persisted say line = {id %q who %q text %q}, want {a:s1 Bart Aye.}", got[0].LineID, got[0].Who, got[0].Text)
	}
}

// TestProjection_ButlerSayLine pins #365 AC2: a Butler /say (SpeakAsButler → SayAs)
// lands through the SAME SpeakRequested → TTSInvoked projection a Character /say uses —
// no hand-crafted row — but the butler-role Target makes the line render with the
// KindButler kind + "Butler" pill, and it persists via the normal relay tee.
func TestProjection_ButlerSayLine(t *testing.T) {
	bus := voiceevent.NewBus()
	fs := &fakeSessions{id: uuid.New(), active: true}
	store := newFakeLineStore()
	r := NewRelay(bus, fs, store, nil)

	bus.Publish(voiceevent.SpeakRequested{
		At: at(1), TurnID: "s1", Text: "At your service.",
		Target: voiceevent.AddressTarget{AgentID: "butler-id", AgentRole: "butler", Name: "Glyphoxa"},
	})
	bus.Publish(voiceevent.TTSInvoked{At: at(2), Sentence: "At your service.", Index: 0, TurnID: "s1"})

	v := r.View(fs.id.String())
	if len(v.Lines) != 1 {
		t.Fatalf("got %d lines, want 1: %+v", len(v.Lines), v.Lines)
	}
	l := v.Lines[0]
	if l.ID != "a:s1" || l.Who != "Glyphoxa" || l.Kind != KindButler || l.Tag != "Butler" {
		t.Errorf("butler say line meta = {id %q who %q kind %q tag %q}, want {a:s1 Glyphoxa butler Butler}", l.ID, l.Who, l.Kind, l.Tag)
	}
	if l.Text != "At your service." {
		t.Errorf("butler say line text = %q, want the verbatim /say text", l.Text)
	}

	if _, err := r.Finalize(context.Background(), fs.id); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	got, _ := store.ListTranscriptLines(context.Background(), fs.id)
	if len(got) != 1 || got[0].LineID != "a:s1" || got[0].Who != "Glyphoxa" || got[0].Text != "At your service." {
		t.Errorf("persisted butler say line = %+v, want one {a:s1 Glyphoxa At your service.}", got)
	}
}

// TestProjection_EnsembleLeadAttributesLine pins #301: the Ensemble Turn's Lead
// speaks under the ensemble's original TurnID, and its EnsembleLead attribution
// makes the coalesced reply land as the Lead's line (a:<turn>, the Lead's name +
// NPC pill) — exactly like an AddressRouted turn. A losing candidate publishes no
// EnsembleLead and no TTSInvoked, so it leaves no line.
func TestProjection_EnsembleLeadAttributesLine(t *testing.T) {
	bus, r, _, id := liveRelay(t)

	bus.Publish(voiceevent.EnsembleLead{
		At: at(1), TurnID: "e1",
		Target: voiceevent.AddressTarget{AgentID: "bart", AgentRole: "character", Name: "Bart"},
	})
	bus.Publish(voiceevent.TTSInvoked{At: at(2), Sentence: "I speak first.", Index: 0, TurnID: "e1"})

	v := r.View(id)
	if len(v.Lines) != 1 {
		t.Fatalf("got %d lines, want 1: %+v", len(v.Lines), v.Lines)
	}
	l := v.Lines[0]
	if l.ID != "a:e1" || l.Who != "Bart" || l.Kind != KindNPC || l.Tag != "NPC" {
		t.Errorf("ensemble lead line meta = {id %q who %q kind %q tag %q}, want {a:e1 Bart npc NPC}", l.ID, l.Who, l.Kind, l.Tag)
	}
	if l.Text != "I speak first." {
		t.Errorf("ensemble lead line text = %q, want the Lead's reply", l.Text)
	}
}

// TestProjection_EnsembleReactionSeparateLine pins #302: the Cross-talk Reaction is a
// distinct sub-turn under its OWN fresh TurnID, so its EnsembleReaction attribution
// lands the reaction as a SEPARATE line (a:<rID>, the reactor's own name + NPC pill)
// beneath the Lead's — never coalesced into the Lead's line.
func TestProjection_EnsembleReactionSeparateLine(t *testing.T) {
	bus, r, _, id := liveRelay(t)

	bus.Publish(voiceevent.EnsembleLead{
		At: at(1), TurnID: "e1",
		Target: voiceevent.AddressTarget{AgentID: "bart", AgentRole: "character", Name: "Bart"},
	})
	bus.Publish(voiceevent.TTSInvoked{At: at(2), Sentence: "I speak first.", Index: 0, TurnID: "e1"})
	bus.Publish(voiceevent.EnsembleReaction{
		At: at(3), TurnID: "r1", LeadTurnID: "e1",
		Target: voiceevent.AddressTarget{AgentID: "goblin", AgentRole: "character", Name: "Goblin"},
	})
	bus.Publish(voiceevent.TTSInvoked{At: at(4), Sentence: "I disagree.", Index: 0, TurnID: "r1"})

	v := r.View(id)
	if len(v.Lines) != 2 {
		t.Fatalf("got %d lines, want 2 (Lead + reaction): %+v", len(v.Lines), v.Lines)
	}
	lead, reaction := v.Lines[0], v.Lines[1]
	if lead.ID != "a:e1" || lead.Who != "Bart" || lead.Text != "I speak first." {
		t.Errorf("lead line = {id %q who %q text %q}, want {a:e1 Bart I speak first.}", lead.ID, lead.Who, lead.Text)
	}
	if reaction.ID != "a:r1" || reaction.Who != "Goblin" || reaction.Kind != KindNPC || reaction.Tag != "NPC" {
		t.Errorf("reaction line meta = {id %q who %q kind %q tag %q}, want {a:r1 Goblin npc NPC}", reaction.ID, reaction.Who, reaction.Kind, reaction.Tag)
	}
	if reaction.Text != "I disagree." {
		t.Errorf("reaction line text = %q, want the reactor's reply", reaction.Text)
	}
}

// TestProjection_EnsembleReactionDeclineNoLine pins the decline path (#302, ADR-0012):
// a Reaction that never played publishes no EnsembleReaction and no TTSInvoked, so it
// leaves no line — only the Lead's.
func TestProjection_EnsembleReactionDeclineNoLine(t *testing.T) {
	bus, r, _, id := liveRelay(t)

	bus.Publish(voiceevent.EnsembleLead{
		At: at(1), TurnID: "e1",
		Target: voiceevent.AddressTarget{AgentID: "bart", AgentRole: "character", Name: "Bart"},
	})
	bus.Publish(voiceevent.TTSInvoked{At: at(2), Sentence: "I speak first.", Index: 0, TurnID: "e1"})

	v := r.View(id)
	if len(v.Lines) != 1 {
		t.Fatalf("got %d lines, want only the Lead's (a decline leaves no line): %+v", len(v.Lines), v.Lines)
	}
}

// TestProjection_EnsembleReactionBargeMarksLineEnded pins #302 (plan test 11, third
// clause): a barge during the reaction's playback ends its sub-turn (TurnEnded{rID,
// barge}); the relay marks that turn ended so a LATE reaction sentence — which a barge
// can deliver after the end — does not clobber the finalized reaction line.
func TestProjection_EnsembleReactionBargeMarksLineEnded(t *testing.T) {
	bus, r, _, id := liveRelay(t)

	bus.Publish(voiceevent.EnsembleLead{
		At: at(1), TurnID: "e1",
		Target: voiceevent.AddressTarget{AgentID: "bart", AgentRole: "character", Name: "Bart"},
	})
	bus.Publish(voiceevent.TTSInvoked{At: at(2), Sentence: "I speak first.", Index: 0, TurnID: "e1"})
	bus.Publish(voiceevent.EnsembleReaction{
		At: at(3), TurnID: "r1", LeadTurnID: "e1",
		Target: voiceevent.AddressTarget{AgentID: "goblin", AgentRole: "character", Name: "Goblin"},
	})
	bus.Publish(voiceevent.TTSInvoked{At: at(4), Sentence: "I disagree.", Index: 0, TurnID: "r1"})
	// A barge cuts the reaction: its sub-turn ends.
	bus.Publish(voiceevent.TurnEnded{At: at(5), TurnID: "r1", Reason: voiceevent.TurnEndBarge})
	// A late reaction sentence a barge delivered after the end must be dropped.
	bus.Publish(voiceevent.TTSInvoked{At: at(6), Sentence: " and clobber!", Index: 1, TurnID: "r1"})

	v := r.View(id)
	if len(v.Lines) != 2 {
		t.Fatalf("got %d lines, want 2 (Lead + reaction): %+v", len(v.Lines), v.Lines)
	}
	if got := v.Lines[1].Text; got != "I disagree." {
		t.Errorf("reaction line text = %q, want %q (the post-end sentence must not clobber the finalized line)", got, "I disagree.")
	}
}
