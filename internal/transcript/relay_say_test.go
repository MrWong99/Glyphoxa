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
