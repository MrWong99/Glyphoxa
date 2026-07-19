package transcript

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/speaker"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// TestChunker_ResolvedNamePrefix: a chunk's human line uses the resolved Character
// (or guild) name as its prefix; an unresolved speaker keeps the generic
// "Player / DM:" prefix — NEW chunk content only.
func TestChunker_ResolvedNamePrefix(t *testing.T) {
	spy := &spyResolver{lookup: func(_ uuid.UUID, sp string) speaker.Resolution {
		if sp == kiraSpeaker {
			return speaker.Resolution{Name: "Kira"}
		}
		return speaker.Resolution{} // guestSpeaker unresolved
	}}

	bus := voiceevent.NewBus()
	fs := &fakeSessions{id: uuid.New(), campaign: uuid.New(), active: true}
	store := &fakeChunkStore{}
	c := NewChunker(fwd(t, bus, fs), fs, store, nil, slog.New(slog.DiscardHandler), ChunkerConfig{MaxUtterances: 2})
	c.SetResolver(spy)

	bus.Publish(voiceevent.STTFinal{At: at(1), Text: "mapped line", TurnID: "t1", SpeakerID: kiraSpeaker})
	bus.Publish(voiceevent.STTFinal{At: at(2), Text: "unresolved line", TurnID: "t2", SpeakerID: guestSpeaker})

	if err := c.FlushSession(context.Background(), fs.id); err != nil {
		t.Fatalf("FlushSession: %v", err)
	}
	got := store.all()
	if len(got) != 1 {
		t.Fatalf("chunks = %d, want 1", len(got))
	}
	want := "Kira: mapped line\nPlayer / DM: unresolved line"
	if got[0].Content != want {
		t.Fatalf("content = %q,\nwant %q", got[0].Content, want)
	}
}
