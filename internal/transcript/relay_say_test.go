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
	bus.Publish(voiceevent.TTSInvoked{At: at(2), Sentence: "Welcome, travelers.", TurnID: "s1"})

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
	r := NewRelay(fwd(t, bus, fs), fs, store, nil)

	bus.Publish(voiceevent.SpeakRequested{
		At: at(1), TurnID: "s1", Text: "Aye.",
		Target: voiceevent.AddressTarget{AgentID: "bart", AgentRole: "character", Name: "Bart"},
	})
	bus.Publish(voiceevent.TTSInvoked{At: at(2), Sentence: "Aye.", TurnID: "s1"})
	// The sentence reached the room: the delivery signal that keeps the row past the
	// #437 reconcile (ADR-0040 amendment — a zero-delivery turn is retracted).
	bus.Publish(voiceevent.FirstAudio{At: at(3), TurnID: "s1"})

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
	r := NewRelay(fwd(t, bus, fs), fs, store, nil)

	bus.Publish(voiceevent.SpeakRequested{
		At: at(1), TurnID: "s1", Text: "At your service.",
		Target: voiceevent.AddressTarget{AgentID: "butler-id", AgentRole: "butler", Name: "Glyphoxa"},
	})
	bus.Publish(voiceevent.TTSInvoked{At: at(2), Sentence: "At your service.", TurnID: "s1"})
	// Delivered (#437): without a FirstAudio the row would be reconciled away.
	bus.Publish(voiceevent.FirstAudio{At: at(3), TurnID: "s1"})

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
