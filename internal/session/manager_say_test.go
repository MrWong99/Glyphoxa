package session_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// TestSayAs_IdleReturnsNoActiveSession pins the active-session requirement (#295,
// ADR-0010): a /say with no live Voice Session is refused before any roster lookup
// and publishes nothing.
func TestSayAs_IdleReturnsNoActiveSession(t *testing.T) {
	mgr, bus := muteManager(t, newFakeStore())
	var got []voiceevent.SpeakRequested
	t.Cleanup(voiceevent.On(bus, func(e voiceevent.SpeakRequested) { got = append(got, e) }))

	if err := mgr.SayAs(context.Background(), uuid.NewString(), "hello"); err != session.ErrNoActiveSession {
		t.Fatalf("SayAs while idle = %v, want ErrNoActiveSession", err)
	}
	if len(got) != 0 {
		t.Fatalf("idle SayAs published %d SpeakRequested, want none", len(got))
	}
}

// TestSayAs_ForeignAgentRejected pins the campaign-membership guard (#295): an
// agent not in the active session's voiced roster is refused ErrAgentNotInCampaign
// and publishes nothing.
func TestSayAs_ForeignAgentRejected(t *testing.T) {
	store := newFakeStore()
	seedAgents(store, 1)
	mgr, bus := muteManager(t, store)
	startMuteSession(t, mgr)
	var got []voiceevent.SpeakRequested
	t.Cleanup(voiceevent.On(bus, func(e voiceevent.SpeakRequested) { got = append(got, e) }))

	if err := mgr.SayAs(context.Background(), uuid.NewString(), "hello"); err != session.ErrAgentNotInCampaign {
		t.Fatalf("SayAs with a foreign agent = %v, want ErrAgentNotInCampaign", err)
	}
	if len(got) != 0 {
		t.Fatalf("foreign SayAs published %d SpeakRequested, want none", len(got))
	}
}

// TestSayAs_ButlerPublishesButlerRole pins the Butler voicer on-ramp (#365,
// ADR-0009 Butler-in-Cast amendment): the now-voiced Butler CAN be voiced verbatim,
// and its SpeakRequested carries the BUTLER role (so the relay projects a KindButler
// line) plus the Butler's display name — the role is derived from the Agent, not
// hardcoded to Character. The Discord /say roster still excludes the Butler (say.go's
// voiced filter); this is the programmatic SpeakAsButler path.
func TestSayAs_ButlerPublishesButlerRole(t *testing.T) {
	store := newFakeStore()
	butler := storage.Agent{ID: uuid.New(), Role: storage.AgentRoleButler, Name: "Glyphoxa"}
	store.agents = []storage.Agent{butler}
	mgr, bus := muteManager(t, store)
	startMuteSession(t, mgr)

	var got []voiceevent.SpeakRequested
	t.Cleanup(voiceevent.On(bus, func(e voiceevent.SpeakRequested) { got = append(got, e) }))

	if err := mgr.SayAs(context.Background(), butler.ID.String(), "At your service."); err != nil {
		t.Fatalf("SayAs with the Butler: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("SayAs published %d SpeakRequested, want 1", len(got))
	}
	e := got[0]
	if e.Target.AgentID != butler.ID.String() {
		t.Errorf("Target.AgentID = %q, want %q", e.Target.AgentID, butler.ID.String())
	}
	if e.Target.AgentRole != voiceevent.AgentRoleButler {
		t.Errorf("Target.AgentRole = %q, want %q (KindButler line)", e.Target.AgentRole, voiceevent.AgentRoleButler)
	}
	if e.Target.Name != "Glyphoxa" {
		t.Errorf("Target.Name = %q, want Glyphoxa", e.Target.Name)
	}
	if e.Text != "At your service." {
		t.Errorf("Text = %q, want the verbatim /say text", e.Text)
	}
}

// TestSpeakAsButler_PublishesButlerLine pins the Butler voicer on-ramp (#365): with
// a live session whose Campaign has a voiced Butler, SpeakAsButler resolves the Butler
// and publishes exactly one SpeakRequested carrying the Butler's butler-role Target
// (→ KindButler line) and the verbatim text — the recap decision-6a voiced path.
func TestSpeakAsButler_PublishesButlerLine(t *testing.T) {
	store := newFakeStore()
	butler := storage.Agent{ID: uuid.New(), Role: storage.AgentRoleButler, Name: "Glyphoxa"}
	bart := storage.Agent{ID: uuid.New(), Role: storage.AgentRoleCharacter, Name: "Bart"}
	store.agents = []storage.Agent{bart, butler}
	mgr, bus := muteManager(t, store)
	startMuteSession(t, mgr)

	var got []voiceevent.SpeakRequested
	t.Cleanup(voiceevent.On(bus, func(e voiceevent.SpeakRequested) { got = append(got, e) }))

	if err := mgr.SpeakAsButler(context.Background(), "Here is your recap."); err != nil {
		t.Fatalf("SpeakAsButler: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("SpeakAsButler published %d SpeakRequested, want 1", len(got))
	}
	e := got[0]
	if e.Target.AgentID != butler.ID.String() {
		t.Errorf("Target.AgentID = %q, want the Butler's %q", e.Target.AgentID, butler.ID.String())
	}
	if e.Target.AgentRole != voiceevent.AgentRoleButler {
		t.Errorf("Target.AgentRole = %q, want %q (KindButler line)", e.Target.AgentRole, voiceevent.AgentRoleButler)
	}
	if e.Target.Name != "Glyphoxa" {
		t.Errorf("Target.Name = %q, want Glyphoxa", e.Target.Name)
	}
	if e.Text != "Here is your recap." {
		t.Errorf("Text = %q, want the verbatim recap text", e.Text)
	}
}

// TestSpeakAsButler_IdleReturnsNoActiveSession pins the active-session guard (#365):
// SpeakAsButler with no live Voice Session is refused before any roster lookup and
// publishes nothing.
func TestSpeakAsButler_IdleReturnsNoActiveSession(t *testing.T) {
	store := newFakeStore()
	store.agents = []storage.Agent{{ID: uuid.New(), Role: storage.AgentRoleButler, Name: "Glyphoxa"}}
	mgr, bus := muteManager(t, store)
	var got []voiceevent.SpeakRequested
	t.Cleanup(voiceevent.On(bus, func(e voiceevent.SpeakRequested) { got = append(got, e) }))

	if err := mgr.SpeakAsButler(context.Background(), "hello"); err != session.ErrNoActiveSession {
		t.Fatalf("SpeakAsButler while idle = %v, want ErrNoActiveSession", err)
	}
	if len(got) != 0 {
		t.Fatalf("idle SpeakAsButler published %d SpeakRequested, want none", len(got))
	}
}

// TestSpeakAsButler_NoButlerRejected pins the missing-Butler guard (#365): a live
// Campaign with no Butler in its roster yields ErrAgentNotInCampaign and publishes
// nothing.
func TestSpeakAsButler_NoButlerRejected(t *testing.T) {
	store := newFakeStore()
	store.agents = []storage.Agent{{ID: uuid.New(), Role: storage.AgentRoleCharacter, Name: "Bart"}}
	mgr, bus := muteManager(t, store)
	startMuteSession(t, mgr)
	var got []voiceevent.SpeakRequested
	t.Cleanup(voiceevent.On(bus, func(e voiceevent.SpeakRequested) { got = append(got, e) }))

	if err := mgr.SpeakAsButler(context.Background(), "hello"); err != session.ErrAgentNotInCampaign {
		t.Fatalf("SpeakAsButler with no Butler = %v, want ErrAgentNotInCampaign", err)
	}
	if len(got) != 0 {
		t.Fatalf("butler-less SpeakAsButler published %d SpeakRequested, want none", len(got))
	}
}

// TestSayAs_HappyPublishesSpeakRequested pins the success path (#295): a voiced
// Character NPC of the active Campaign yields exactly one SpeakRequested carrying
// the agent's Target (id + character role + display name), a fresh TurnID, and the
// verbatim text.
func TestSayAs_HappyPublishesSpeakRequested(t *testing.T) {
	store := newFakeStore()
	bart := storage.Agent{ID: uuid.New(), Role: storage.AgentRoleCharacter, Name: "Bart"}
	store.agents = []storage.Agent{bart}
	mgr, bus := muteManager(t, store)
	startMuteSession(t, mgr)

	var got []voiceevent.SpeakRequested
	t.Cleanup(voiceevent.On(bus, func(e voiceevent.SpeakRequested) { got = append(got, e) }))

	if err := mgr.SayAs(context.Background(), bart.ID.String(), "Welcome, travelers."); err != nil {
		t.Fatalf("SayAs happy path: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("SayAs published %d SpeakRequested, want 1", len(got))
	}
	e := got[0]
	if e.Target.AgentID != bart.ID.String() {
		t.Errorf("Target.AgentID = %q, want %q", e.Target.AgentID, bart.ID.String())
	}
	if e.Target.AgentRole != voiceevent.AgentRoleCharacter {
		t.Errorf("Target.AgentRole = %q, want %q", e.Target.AgentRole, voiceevent.AgentRoleCharacter)
	}
	if e.Target.Name != "Bart" {
		t.Errorf("Target.Name = %q, want Bart", e.Target.Name)
	}
	if e.Text != "Welcome, travelers." {
		t.Errorf("Text = %q, want the verbatim /say text", e.Text)
	}
	if e.TurnID == "" {
		t.Error("SpeakRequested carries no TurnID, want a fresh one")
	}
}
