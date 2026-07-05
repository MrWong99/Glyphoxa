package session_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/wirenpc"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// reRunnableRunner blocks until ctx is cancelled and is safe to run more than
// once (the shared blockingRunner closes a one-shot channel), so a test can Start
// the same Manager twice (the AC5 reset).
func reRunnableRunner(ctx context.Context, _ wirenpc.Config) error {
	<-ctx.Done()
	return ctx.Err()
}

// muteManager builds a Manager wired to a real voiceevent.Bus (so MuteChanged
// publications are observable) over store, with a re-runnable blocking runner.
func muteManager(t *testing.T, store session.Store) (*session.Manager, *voiceevent.Bus) {
	t.Helper()
	bus := voiceevent.NewBus()
	mgr := session.NewManager(store, reRunnableRunner,
		wirenpc.Config{Token: "test-token", Bus: bus}, nil, slog.New(slog.DiscardHandler), true)
	return mgr, bus
}

// startMuteSession starts a session and returns its campaign id.
func startMuteSession(t *testing.T, mgr *session.Manager) uuid.UUID {
	t.Helper()
	campaignID := uuid.New()
	if _, err := mgr.Start(context.Background(), uuid.New(), campaignID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(mgr.Shutdown)
	return campaignID
}

// TestSetAgentMute_IdleReturnsNoActiveSession pins the active-session requirement
// (AC4): muting with no live session is refused.
func TestSetAgentMute_IdleReturnsNoActiveSession(t *testing.T) {
	mgr, _ := muteManager(t, newFakeStore())
	if _, err := mgr.SetAgentMute("agent-1", true); err != session.ErrNoActiveSession {
		t.Fatalf("SetAgentMute while idle = %v, want ErrNoActiveSession", err)
	}
}

// TestSetAgentMute_PublishesOncePerActualChange pins the idempotent publish (test
// 8): a mute publishes exactly one MuteChanged; a redundant re-mute publishes
// none; an unmute publishes one. The set + the returned sorted ids track each.
func TestSetAgentMute_PublishesOncePerActualChange(t *testing.T) {
	mgr, bus := muteManager(t, newFakeStore())
	startMuteSession(t, mgr)

	var events []voiceevent.MuteChanged
	t.Cleanup(voiceevent.On(bus, func(e voiceevent.MuteChanged) { events = append(events, e) }))

	ids, err := mgr.SetAgentMute("bart", true)
	if err != nil {
		t.Fatalf("SetAgentMute: %v", err)
	}
	if len(ids) != 1 || ids[0] != "bart" {
		t.Fatalf("muted ids = %v, want [bart]", ids)
	}
	if !mgr.Muted("bart") {
		t.Fatal("Muted(bart) = false after mute, want true")
	}
	if len(events) != 1 || events[0].AgentID != "bart" || !events[0].Muted {
		t.Fatalf("events after mute = %+v, want one {bart true}", events)
	}

	// Idempotent re-mute publishes nothing and stays muted.
	if _, err := mgr.SetAgentMute("bart", true); err != nil {
		t.Fatalf("re-mute: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("re-mute published %d events, want 0 more (idempotent)", len(events)-1)
	}

	// Unmute publishes one and clears the set.
	ids, err = mgr.SetAgentMute("bart", false)
	if err != nil {
		t.Fatalf("unmute: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("muted ids after unmute = %v, want empty", ids)
	}
	if mgr.Muted("bart") {
		t.Fatal("Muted(bart) = true after unmute")
	}
	if len(events) != 2 || events[1].Muted {
		t.Fatalf("events after unmute = %+v, want a second {bart false}", events)
	}
}

// TestMutedAgentIDs_SortedSnapshot pins the reload truth (AC5): MutedAgentIDs is a
// sorted snapshot of the current muted set.
func TestMutedAgentIDs_SortedSnapshot(t *testing.T) {
	mgr, _ := muteManager(t, newFakeStore())
	startMuteSession(t, mgr)
	if got := mgr.MutedAgentIDs(); len(got) != 0 {
		t.Fatalf("a fresh session has muted ids %v, want none (AC5 all-unmuted at start)", got)
	}
	_, _ = mgr.SetAgentMute("greta", true)
	_, _ = mgr.SetAgentMute("bart", true)
	got := mgr.MutedAgentIDs()
	if len(got) != 2 || got[0] != "bart" || got[1] != "greta" {
		t.Fatalf("MutedAgentIDs = %v, want sorted [bart greta]", got)
	}
}

// TestStartResetsMuteSet pins AC5: every new Voice Session starts with all Agents
// unmuted — the volatile set is fresh per Start, so a mute from a prior session
// does not leak.
func TestStartResetsMuteSet(t *testing.T) {
	mgr, _ := muteManager(t, newFakeStore())
	campaignID := uuid.New()
	if _, err := mgr.Start(context.Background(), uuid.New(), campaignID); err != nil {
		t.Fatalf("Start 1: %v", err)
	}
	if _, err := mgr.SetAgentMute("bart", true); err != nil {
		t.Fatalf("mute: %v", err)
	}
	if _, err := mgr.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if _, err := mgr.Start(context.Background(), uuid.New(), campaignID); err != nil {
		t.Fatalf("Start 2: %v", err)
	}
	t.Cleanup(mgr.Shutdown)
	if mgr.Muted("bart") {
		t.Fatal("a new Voice Session must start with Bart unmuted (AC5)")
	}
	if got := mgr.MutedAgentIDs(); len(got) != 0 {
		t.Fatalf("a new session has muted ids %v, want none", got)
	}
}

// TestSetAllMute_MutesAndClearsEveryAgent pins muteall (test 8): SetAllMute(true)
// mutes every Agent of the Active Campaign (Butler + NPCs) with one MuteChanged
// each; SetAllMute(false) clears them.
func TestSetAllMute_MutesAndClearsEveryAgent(t *testing.T) {
	store := newFakeStore()
	butler := storage.Agent{ID: uuid.New(), Role: storage.AgentRoleButler, Name: "Butler"}
	bart := storage.Agent{ID: uuid.New(), Role: storage.AgentRoleCharacter, Name: "Bart"}
	store.agents = []storage.Agent{butler, bart}

	mgr, bus := muteManager(t, store)
	startMuteSession(t, mgr)

	var events []voiceevent.MuteChanged
	t.Cleanup(voiceevent.On(bus, func(e voiceevent.MuteChanged) { events = append(events, e) }))

	ids, err := mgr.SetAllMute(context.Background(), true)
	if err != nil {
		t.Fatalf("SetAllMute(true): %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("muted ids after mute-all = %v, want 2 (Butler + Bart)", ids)
	}
	if !mgr.Muted(butler.ID.String()) || !mgr.Muted(bart.ID.String()) {
		t.Fatal("mute-all must mute every Agent including the Butler")
	}
	if len(events) != 2 {
		t.Fatalf("mute-all published %d events, want 2 (one per Agent)", len(events))
	}

	events = nil
	ids, err = mgr.SetAllMute(context.Background(), false)
	if err != nil {
		t.Fatalf("SetAllMute(false): %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("muted ids after unmute-all = %v, want none", ids)
	}
	if len(events) != 2 {
		t.Fatalf("unmute-all published %d events, want 2", len(events))
	}
}

// TestSetAllMute_IdleReturnsNoActiveSession pins the active-session requirement.
func TestSetAllMute_IdleReturnsNoActiveSession(t *testing.T) {
	mgr, _ := muteManager(t, newFakeStore())
	if _, err := mgr.SetAllMute(context.Background(), true); err != session.ErrNoActiveSession {
		t.Fatalf("SetAllMute while idle = %v, want ErrNoActiveSession", err)
	}
}
