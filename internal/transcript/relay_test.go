package transcript

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// fakeSessions is a settable Snapshot source: the relay attributes events and
// derives status from whatever active session it reports.
type fakeSessions struct {
	id     uuid.UUID
	active bool
}

func (f *fakeSessions) Snapshot() (storage.VoiceSession, bool) {
	return storage.VoiceSession{ID: f.id}, f.active
}

func at(sec int) time.Time {
	return time.Date(2026, 6, 27, 18, 0, sec, 0, time.UTC)
}

// liveRelay returns a relay wired to a bus with one active session.
func liveRelay(t *testing.T) (*voiceevent.Bus, *Relay, *fakeSessions, string) {
	t.Helper()
	bus := voiceevent.NewBus()
	fs := &fakeSessions{id: uuid.New(), active: true}
	r := NewRelay(bus, fs, nil)
	return bus, r, fs, fs.id.String()
}

func TestKindFor(t *testing.T) {
	cases := map[string]Kind{
		"gm":        KindGM,
		"player":    KindPlayer,
		"butler":    KindButler,
		"character": KindNPC,
		"":          KindNPC, // any other Agent role
	}
	for role, want := range cases {
		if got := kindFor(role); got != want {
			t.Errorf("kindFor(%q) = %q, want %q", role, got, want)
		}
	}
}

// TestProjection_LinesAndOrder feeds one turn (human → routed → two reply
// sentences → end) and asserts the projected lines: an anonymous human lane and
// a coalesced NPC reply, in order.
func TestProjection_LinesAndOrder(t *testing.T) {
	bus, r, _, id := liveRelay(t)

	bus.Publish(voiceevent.STTFinal{At: at(1), Text: "Hello Bart", TurnID: "t1"})
	bus.Publish(voiceevent.AddressRouted{
		At: at(2), Text: "Hello Bart", TurnID: "t1",
		Target: voiceevent.AddressTarget{AgentID: "bart", AgentRole: "character", Name: "Bart"},
	})
	bus.Publish(voiceevent.TTSInvoked{At: at(3), Sentence: "Well met.", Index: 0, TurnID: "t1"})
	bus.Publish(voiceevent.TTSInvoked{At: at(4), Sentence: "What'll it be?", Index: 1, TurnID: "t1"})
	bus.Publish(voiceevent.TurnEnded{At: at(5), TurnID: "t1", Reason: voiceevent.TurnEndBarge})

	v := r.View(id)
	if len(v.Lines) != 2 {
		t.Fatalf("got %d lines, want 2: %+v", len(v.Lines), v.Lines)
	}
	human := v.Lines[0]
	if human.Who != "Player / DM" || human.Kind != KindPlayer || human.Tag != "" || human.Text != "Hello Bart" {
		t.Errorf("human line = %+v", human)
	}
	npc := v.Lines[1]
	if npc.Who != "Bart" || npc.Kind != KindNPC || npc.Tag != "NPC" {
		t.Errorf("npc line meta = %+v", npc)
	}
	if npc.Text != "Well met. What'll it be?" {
		t.Errorf("npc coalesced text = %q, want %q", npc.Text, "Well met. What'll it be?")
	}
}

// TestProjection_ButlerKind checks the butler role maps to the butler kind + tag.
func TestProjection_ButlerKind(t *testing.T) {
	bus, r, _, id := liveRelay(t)
	bus.Publish(voiceevent.AddressRouted{
		At: at(1), TurnID: "t1",
		Target: voiceevent.AddressTarget{AgentID: "butler", AgentRole: "butler", Name: "Butler"},
	})
	bus.Publish(voiceevent.TTSInvoked{At: at(2), Sentence: "At your service.", TurnID: "t1"})

	v := r.View(id)
	if len(v.Lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(v.Lines))
	}
	if l := v.Lines[0]; l.Kind != KindButler || l.Tag != "Butler" || l.Who != "Butler" {
		t.Errorf("butler line = %+v", l)
	}
}

// TestTypingAndStatus asserts the server-side typing/status derivation: live +
// listening while idle-between-turns, "<Name> is speaking…" mid-reply, back to
// listening on turn end, and idle once the session stops.
func TestTypingAndStatus(t *testing.T) {
	bus, r, fs, id := liveRelay(t)

	// First event opens the session → live + listening.
	bus.Publish(voiceevent.STTFinal{At: at(1), Text: "Hi", TurnID: "t1"})
	if v := r.View(id); v.Status != "live" || !v.Typing.Active || v.Typing.Label != listenLabel {
		t.Fatalf("after STT: status=%q typing=%+v", v.Status, v.Typing)
	}

	// Mid-reply → "<Name> is speaking…".
	bus.Publish(voiceevent.AddressRouted{
		At: at(2), TurnID: "t1",
		Target: voiceevent.AddressTarget{AgentRole: "character", Name: "Bart"},
	})
	bus.Publish(voiceevent.TTSInvoked{At: at(3), Sentence: "Aye.", TurnID: "t1"})
	if v := r.View(id); !v.Typing.Active || v.Typing.Label != "Bart is speaking…" {
		t.Fatalf("mid-reply typing=%+v", v.Typing)
	}

	// Turn end → back to listening.
	bus.Publish(voiceevent.TurnEnded{At: at(4), TurnID: "t1", Reason: voiceevent.TurnEndBarge})
	if v := r.View(id); v.Typing.Label != listenLabel {
		t.Fatalf("post-turn typing=%+v", v.Typing)
	}

	// Session stops → idle, inactive typing, no lines.
	fs.active = false
	if v := r.View(id); v.Status != "idle" || v.Typing.Active || len(v.Lines) != 0 {
		t.Fatalf("idle view=%+v", v)
	}
}

// TestReplayAfterLastEventID checks the ring buffer replays only frames after a
// cursor, in monotonic seq order — the Last-Event-ID reconnect contract.
func TestReplayAfterLastEventID(t *testing.T) {
	bus, r, _, id := liveRelay(t)
	bus.Publish(voiceevent.STTFinal{At: at(1), Text: "one", TurnID: "t1"})
	bus.Publish(voiceevent.STTFinal{At: at(2), Text: "two", TurnID: "t2"})

	all := r.Frames(id, 0)
	if len(all) < 3 { // status(open) + 2 lines
		t.Fatalf("got %d frames, want >=3: %+v", len(all), all)
	}
	for i := 1; i < len(all); i++ {
		if all[i].Seq <= all[i-1].Seq {
			t.Fatalf("frames not monotonic: %d then %d", all[i-1].Seq, all[i].Seq)
		}
	}

	// Replay after the first frame returns the strict suffix.
	cursor := all[0].Seq
	after := r.Frames(id, cursor)
	if len(after) != len(all)-1 {
		t.Fatalf("replay after %d returned %d frames, want %d", cursor, len(after), len(all)-1)
	}
	for _, f := range after {
		if f.Seq <= cursor {
			t.Errorf("replay leaked frame seq %d <= cursor %d", f.Seq, cursor)
		}
	}

	// Last frame is the "two" line.
	last := all[len(all)-1]
	if last.Event != "line" {
		t.Fatalf("last frame event = %q", last.Event)
	}
	var l Line
	if err := json.Unmarshal(last.Data, &l); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if l.Text != "two" {
		t.Errorf("last line text = %q, want two", l.Text)
	}
}

// TestRolloverOnSessionChange checks a new active session id starts a fresh
// buffer (old frames gone, seq reset).
func TestRolloverOnSessionChange(t *testing.T) {
	bus, r, fs, id1 := liveRelay(t)
	bus.Publish(voiceevent.STTFinal{At: at(1), Text: "old", TurnID: "t1"})
	if len(r.Frames(id1, 0)) == 0 {
		t.Fatal("session 1 buffered nothing")
	}

	fs.id = uuid.New()
	id2 := fs.id.String()
	bus.Publish(voiceevent.STTFinal{At: at(2), Text: "new", TurnID: "t2"})

	if got := r.Frames(id1, 0); got != nil {
		t.Errorf("old session still buffered %d frames", len(got))
	}
	v := r.View(id2)
	if len(v.Lines) != 1 || v.Lines[0].Text != "new" {
		t.Errorf("session 2 view = %+v", v)
	}
}

// TestDropWhenIdle checks events with no active session are dropped.
func TestDropWhenIdle(t *testing.T) {
	bus := voiceevent.NewBus()
	fs := &fakeSessions{id: uuid.New(), active: false}
	r := NewRelay(bus, fs, nil)
	bus.Publish(voiceevent.STTFinal{At: at(1), Text: "ignored", TurnID: "t1"})
	if got := r.Frames(fs.id.String(), 0); got != nil {
		t.Errorf("idle relay buffered %d frames", len(got))
	}
}
