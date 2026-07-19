package transcript

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/speaker"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// spyResolver is a settable SpeakerResolver: it records Warm calls and answers
// Lookup from an injected func, so the relay/chunker projection is tested without
// the real async cache.
type spyResolver struct {
	mu     sync.Mutex
	warmed []warmCall
	lookup func(campaignID uuid.UUID, speakerID string) speaker.Resolution
}

type warmCall struct {
	campaign uuid.UUID
	speaker  string
}

func (s *spyResolver) Warm(campaignID uuid.UUID, speakerID string) {
	s.mu.Lock()
	s.warmed = append(s.warmed, warmCall{campaign: campaignID, speaker: speakerID})
	s.mu.Unlock()
}

func (s *spyResolver) Lookup(campaignID uuid.UUID, speakerID string) speaker.Resolution {
	if s.lookup != nil {
		return s.lookup(campaignID, speakerID)
	}
	return speaker.Resolution{}
}

func (s *spyResolver) warmCalls() []warmCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]warmCall(nil), s.warmed...)
}

const (
	kiraSpeaker  = "111111111111111111"
	gmSpeaker    = "222222222222222222"
	guestSpeaker = "333333333333333333"
)

// resolvingRelay wires a relay with a resolver and an active campaign-scoped session.
func resolvingRelay(t *testing.T, res SpeakerResolver) (*voiceevent.Bus, *Relay, *fakeSessions) {
	t.Helper()
	bus := voiceevent.NewBus()
	fs := &fakeSessions{id: uuid.New(), campaign: uuid.New(), active: true}
	r := NewRelay(fwd(t, bus, fs), fs, nil, nil)
	r.SetResolver(res)
	return bus, r, fs
}

// TestResolve_MappedSpeaker: a mapped SpeakerID renders as its Character on the
// live feed, KindPlayer.
func TestResolve_MappedSpeaker(t *testing.T) {
	spy := &spyResolver{lookup: func(_ uuid.UUID, sp string) speaker.Resolution {
		if sp == kiraSpeaker {
			return speaker.Resolution{Name: "Kira"}
		}
		return speaker.Resolution{}
	}}
	bus, r, fs := resolvingRelay(t, spy)

	bus.Publish(voiceevent.STTFinal{At: at(1), Text: "Hello", TurnID: "t1", SpeakerID: kiraSpeaker})

	l := r.View(fs.id.String()).Lines[0]
	if l.Who != "Kira" || l.Kind != KindPlayer {
		t.Fatalf("mapped line = who %q kind %q, want Kira/player", l.Who, l.Kind)
	}
}

// TestResolve_GMLane: a GM-allowlisted snowflake lands in the KindGM lane
// regardless of Character mapping (mapped GM keeps its Character name; unmapped GM
// keeps the generic label but still routes to KindGM).
func TestResolve_GMLane(t *testing.T) {
	spy := &spyResolver{lookup: func(_ uuid.UUID, sp string) speaker.Resolution {
		switch sp {
		case gmSpeaker: // mapped GM
			return speaker.Resolution{Name: "Dungeon Master", GM: true}
		case "444444444444444444": // unmapped GM (no name)
			return speaker.Resolution{GM: true}
		}
		return speaker.Resolution{}
	}}
	bus, r, fs := resolvingRelay(t, spy)

	bus.Publish(voiceevent.STTFinal{At: at(1), Text: "As the GM speaks", TurnID: "t1", SpeakerID: gmSpeaker})
	bus.Publish(voiceevent.STTFinal{At: at(2), Text: "Unmapped GM", TurnID: "t2", SpeakerID: "444444444444444444"})

	lines := r.View(fs.id.String()).Lines
	mapped := lines[0]
	if mapped.Kind != KindGM || mapped.Who != "Dungeon Master" {
		t.Fatalf("mapped GM line = who %q kind %q, want Dungeon Master/gm", mapped.Who, mapped.Kind)
	}
	unmapped := lines[1]
	if unmapped.Kind != KindGM || unmapped.Who != "Player / DM" {
		t.Fatalf("unmapped GM line = who %q kind %q, want Player / DM/gm", unmapped.Who, unmapped.Kind)
	}
}

// TestResolve_UnmappedGuestName: an unmapped speaker whose guild display name
// resolved renders as that name, KindPlayer.
func TestResolve_UnmappedGuestName(t *testing.T) {
	spy := &spyResolver{lookup: func(_ uuid.UUID, sp string) speaker.Resolution {
		if sp == guestSpeaker {
			return speaker.Resolution{Name: "GuildGuest"}
		}
		return speaker.Resolution{}
	}}
	bus, r, fs := resolvingRelay(t, spy)

	bus.Publish(voiceevent.STTFinal{At: at(1), Text: "Hi", TurnID: "t1", SpeakerID: guestSpeaker})

	l := r.View(fs.id.String()).Lines[0]
	if l.Who != "GuildGuest" || l.Kind != KindPlayer {
		t.Fatalf("unmapped-resolved line = who %q kind %q, want GuildGuest/player", l.Who, l.Kind)
	}
}

// TestResolve_UnresolvedAndEmptyByteIdentical: an unresolved SpeakerID (empty
// Resolution.Name) and an empty SpeakerID both keep the pre-#281 generic
// "Player / DM" / KindPlayer label — byte-identical to the anonymous path.
func TestResolve_UnresolvedAndEmptyByteIdentical(t *testing.T) {
	spy := &spyResolver{lookup: func(_ uuid.UUID, _ string) speaker.Resolution {
		return speaker.Resolution{} // never resolves
	}}
	bus, r, fs := resolvingRelay(t, spy)

	bus.Publish(voiceevent.STTFinal{At: at(1), Text: "attributed but unresolved", TurnID: "t1", SpeakerID: kiraSpeaker})
	bus.Publish(voiceevent.STTFinal{At: at(2), Text: "unattributed", TurnID: "t2"})

	lines := r.View(fs.id.String()).Lines
	for i, l := range lines {
		if l.Who != "Player / DM" || l.Kind != KindPlayer || l.Tag != "" {
			t.Fatalf("line %d = who %q kind %q tag %q, want Player / DM/player/\"\"", i, l.Who, l.Kind, l.Tag)
		}
	}
	// The attributed line still carries its SpeakerID for persistence (#278).
	if lines[0].SpeakerID != kiraSpeaker {
		t.Fatalf("line 0 SpeakerID = %q, want %q", lines[0].SpeakerID, kiraSpeaker)
	}
}

// TestResolve_WarmOnSpeechStart: a VADSpeechStart carrying a SpeakerID triggers a
// resolver Warm (~1.7s before STTFinal) so the name is cached by lookup time.
func TestResolve_WarmOnSpeechStart(t *testing.T) {
	spy := &spyResolver{}
	bus, _, fs := resolvingRelay(t, spy)

	bus.Publish(voiceevent.VADSpeechStart{At: at(1), SpeakerID: kiraSpeaker})
	// An unattributed onset must NOT warm (empty speaker).
	bus.Publish(voiceevent.VADSpeechStart{At: at(2)})

	got := spy.warmCalls()
	if len(got) != 1 {
		t.Fatalf("warm calls = %d, want 1: %+v", len(got), got)
	}
	if got[0].speaker != kiraSpeaker || got[0].campaign != fs.campaign {
		t.Fatalf("warm call = %+v, want speaker %q campaign %s", got[0], kiraSpeaker, fs.campaign)
	}
}

// TestResolve_RebindPropagatesToNextLine is the AC mapping-change propagation: a
// live session's mid-session rebind (Character change → InvalidateCampaign) makes
// the NEXT line render the new name while prior lines are untouched. Driven
// end-to-end through the real resolver: warm, line, rebind+invalidate, warm, line.
func TestResolve_RebindPropagatesToNextLine(t *testing.T) {
	campaign := uuid.New()
	chars := &fakeCharLookup{byKey: map[string]storage.Character{
		campaign.String() + "/" + kiraSpeaker: {Name: "Kira", CampaignID: campaign, DiscordUserID: kiraSpeaker},
	}}
	res := speaker.NewResolver(chars, nil, nil, nil) // nil GMChecker: nobody is GM here

	bus := voiceevent.NewBus()
	fs := &fakeSessions{id: uuid.New(), campaign: campaign, active: true}
	r := NewRelay(fwd(t, bus, fs), fs, nil, nil)
	r.SetResolver(res)

	// First utterance: warm (async), wait for the name, then the line renders "Kira".
	bus.Publish(voiceevent.VADSpeechStart{At: at(1), SpeakerID: kiraSpeaker})
	waitResolved(t, res, campaign, kiraSpeaker, "Kira")
	bus.Publish(voiceevent.STTFinal{At: at(2), Text: "first", TurnID: "t1", SpeakerID: kiraSpeaker})

	// Operator rebinds the Discord User to a new Character and invalidates the campaign.
	chars.set(campaign, kiraSpeaker, storage.Character{Name: "Kira Reborn", CampaignID: campaign, DiscordUserID: kiraSpeaker})
	res.InvalidateCampaign(campaign)

	// Next utterance: re-warm, wait for the new name, line renders "Kira Reborn".
	bus.Publish(voiceevent.VADSpeechStart{At: at(3), SpeakerID: kiraSpeaker})
	waitResolved(t, res, campaign, kiraSpeaker, "Kira Reborn")
	bus.Publish(voiceevent.STTFinal{At: at(4), Text: "second", TurnID: "t2", SpeakerID: kiraSpeaker})

	lines := r.View(fs.id.String()).Lines
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	if lines[0].Who != "Kira" {
		t.Errorf("prior line who = %q, want Kira (untouched)", lines[0].Who)
	}
	if lines[1].Who != "Kira Reborn" {
		t.Errorf("next line who = %q, want Kira Reborn (rebound)", lines[1].Who)
	}
}

// waitResolved polls the resolver's cache-only Lookup until it returns want, so a
// test can await an async Warm fill without reaching into resolver internals.
func waitResolved(t *testing.T, res *speaker.Resolver, campaign uuid.UUID, speakerID, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if res.Lookup(campaign, speakerID).Name == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("resolver never resolved %q to %q", speakerID, want)
}

// fakeCharLookup is a mutable CharacterLookup for the end-to-end rebind test.
type fakeCharLookup struct {
	mu    sync.Mutex
	byKey map[string]storage.Character
}

func (f *fakeCharLookup) GetCharacterByDiscordUser(_ context.Context, campaignID uuid.UUID, discordUserID string) (storage.Character, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.byKey[campaignID.String()+"/"+discordUserID]; ok {
		return c, nil
	}
	return storage.Character{}, storage.ErrNotFound
}

func (f *fakeCharLookup) set(campaignID uuid.UUID, discordUserID string, c storage.Character) {
	f.mu.Lock()
	f.byKey[campaignID.String()+"/"+discordUserID] = c
	f.mu.Unlock()
}
