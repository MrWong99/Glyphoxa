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

// TestSayAs_ButlerRejected pins the Address-Only Butler exclusion (ADR-0009/0024):
// the Butler is never voiced, so /say cannot puppet it (the Butler on-ramp is the
// #299-blocked follow-up); it is refused ErrAgentNotInCampaign.
func TestSayAs_ButlerRejected(t *testing.T) {
	store := newFakeStore()
	butler := storage.Agent{ID: uuid.New(), Role: storage.AgentRoleButler, Name: "Butler"}
	store.agents = []storage.Agent{butler}
	mgr, _ := muteManager(t, store)
	startMuteSession(t, mgr)

	if err := mgr.SayAs(context.Background(), butler.ID.String(), "hello"); err != session.ErrAgentNotInCampaign {
		t.Fatalf("SayAs with the Butler = %v, want ErrAgentNotInCampaign", err)
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
