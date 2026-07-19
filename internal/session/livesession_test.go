package session_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/wirenpc"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// TestLive_IdleReturnsNil pins the handle's idle contract (#448): with no active
// Voice Session there is nothing to hand out, so Live returns nil and the caller
// maps that to the same ErrNoActiveSession the Manager pass-throughs report.
func TestLive_IdleReturnsNil(t *testing.T) {
	mgr, _ := muteManager(t, newFakeStore())
	if l := mgr.Live(uuid.New()); l != nil {
		t.Fatalf("Live() while idle = %v, want nil", l)
	}
}

// TestLiveSession_StaleAfterStopRefused pins the stale-handle error contract
// (#448): a handle obtained from a session that has since ENDED fails every
// operation with the existing ErrNoActiveSession semantics — nothing is
// published, nothing is mutated.
func TestLiveSession_StaleAfterStopRefused(t *testing.T) {
	tenantID := uuid.New()
	store := newFakeStore()
	bart := seedAgents(store, 1)[0]
	mgr, bus := muteManager(t, store)
	if _, err := mgr.Start(context.Background(), tenantID, uuid.New()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	live := mgr.Live(tenantID)
	if live == nil {
		t.Fatal("Live() during an active session = nil, want a handle")
	}

	var spoke []voiceevent.SpeakRequested
	t.Cleanup(voiceevent.On(bus, func(e voiceevent.SpeakRequested) { spoke = append(spoke, e) }))
	var replayed []voiceevent.ReplayRequested
	t.Cleanup(voiceevent.On(bus, func(e voiceevent.ReplayRequested) { replayed = append(replayed, e) }))
	var muteEvents []voiceevent.MuteChanged
	t.Cleanup(voiceevent.On(bus, func(e voiceevent.MuteChanged) { muteEvents = append(muteEvents, e) }))

	if _, err := mgr.Stop(context.Background(), tenantID); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if _, err := live.SetAgentMute(context.Background(), bart.ID.String(), true); err != session.ErrNoActiveSession {
		t.Errorf("stale SetAgentMute = %v, want ErrNoActiveSession", err)
	}
	if _, err := live.SetAllMute(context.Background(), true); err != session.ErrNoActiveSession {
		t.Errorf("stale SetAllMute = %v, want ErrNoActiveSession", err)
	}
	if err := live.SayAs(context.Background(), bart.ID.String(), "hello"); err != session.ErrNoActiveSession {
		t.Errorf("stale SayAs = %v, want ErrNoActiveSession", err)
	}
	if err := live.SpeakAsButler(context.Background(), "hello"); err != session.ErrNoActiveSession {
		t.Errorf("stale SpeakAsButler = %v, want ErrNoActiveSession", err)
	}
	if err := live.ReplayHighlight(context.Background(), "clip/abc"); err != session.ErrNoActiveSession {
		t.Errorf("stale ReplayHighlight = %v, want ErrNoActiveSession", err)
	}
	if got := live.MutedAgentIDs(); got != nil {
		t.Errorf("stale MutedAgentIDs = %v, want nil", got)
	}
	if len(spoke) != 0 || len(replayed) != 0 || len(muteEvents) != 0 {
		t.Errorf("a stale handle published %d SpeakRequested / %d ReplayRequested / %d MuteChanged, want none",
			len(spoke), len(replayed), len(muteEvents))
	}
}

// TestLiveSession_MidOpRolloverRefused pins the AUTHORITATIVE revalidation — the
// one under pubMu, after the roster read (#448): the handle passes its stale-fast
// entry check while the session is live, then the session ends DURING ListAgents
// (the fake store's mid-op hook), so only the second check can catch it. The op
// must refuse ErrNoActiveSession and publish nothing — deleting the inner
// revalidate would fail this test.
func TestLiveSession_MidOpRolloverRefused(t *testing.T) {
	tenantID := uuid.New()
	store := newFakeStore()
	bart := seedAgents(store, 1)[0]
	mgr, bus := muteManager(t, store)
	if _, err := mgr.Start(context.Background(), tenantID, uuid.New()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	live := mgr.Live(tenantID)

	var muteEvents []voiceevent.MuteChanged
	t.Cleanup(voiceevent.On(bus, func(e voiceevent.MuteChanged) { muteEvents = append(muteEvents, e) }))
	var spoke []voiceevent.SpeakRequested
	t.Cleanup(voiceevent.On(bus, func(e voiceevent.SpeakRequested) { spoke = append(spoke, e) }))

	// End the session from inside the roster read: after the entry check, before
	// the write/publish. Disarm after one shot so the Stop itself can't recurse.
	store.mu.Lock()
	store.onListAgents = func() {
		store.mu.Lock()
		store.onListAgents = nil
		store.mu.Unlock()
		if _, err := mgr.Stop(context.Background(), tenantID); err != nil {
			t.Errorf("mid-op Stop: %v", err)
		}
	}
	store.mu.Unlock()

	if _, err := live.SetAgentMute(context.Background(), bart.ID.String(), true); err != session.ErrNoActiveSession {
		t.Fatalf("mid-op-rollover SetAgentMute = %v, want ErrNoActiveSession", err)
	}
	if len(muteEvents) != 0 {
		t.Fatalf("mid-op-rollover SetAgentMute published %d MuteChanged, want none", len(muteEvents))
	}

	// Same window for SayAs: entry check passes, the session ends during the
	// roster read, the publish-guarding revalidate must refuse.
	if _, err := mgr.Start(context.Background(), tenantID, uuid.New()); err != nil {
		t.Fatalf("Start 2: %v", err)
	}
	t.Cleanup(mgr.Shutdown)
	live = mgr.Live(tenantID)
	store.mu.Lock()
	store.onListAgents = func() {
		store.mu.Lock()
		store.onListAgents = nil
		store.mu.Unlock()
		if _, err := mgr.Stop(context.Background(), tenantID); err != nil {
			t.Errorf("mid-op Stop 2: %v", err)
		}
	}
	store.mu.Unlock()
	if err := live.SayAs(context.Background(), bart.ID.String(), "hello"); err != session.ErrNoActiveSession {
		t.Fatalf("mid-op-rollover SayAs = %v, want ErrNoActiveSession", err)
	}
	if len(spoke) != 0 {
		t.Fatalf("mid-op-rollover SayAs published %d SpeakRequested, want none", len(spoke))
	}
}

// TestLiveSession_StaleAfterRolloverRefused pins the sharper half of the stale
// contract (#448): a handle from session 1 stays stale even while session 2 IS
// active — the revalidation is same-session, not merely any-session — so a
// racing op can never mutate or voice into a successor session's state.
func TestLiveSession_StaleAfterRolloverRefused(t *testing.T) {
	tenantID := uuid.New()
	store := newFakeStore()
	bart := seedAgents(store, 1)[0]
	mgr, _ := muteManager(t, store)
	if _, err := mgr.Start(context.Background(), tenantID, uuid.New()); err != nil {
		t.Fatalf("Start 1: %v", err)
	}
	stale := mgr.Live(tenantID)
	if _, err := mgr.Stop(context.Background(), tenantID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := mgr.Start(context.Background(), tenantID, uuid.New()); err != nil {
		t.Fatalf("Start 2: %v", err)
	}
	t.Cleanup(mgr.Shutdown)

	if _, err := stale.SetAgentMute(context.Background(), bart.ID.String(), true); err != session.ErrNoActiveSession {
		t.Errorf("rolled-over SetAgentMute = %v, want ErrNoActiveSession", err)
	}
	if got := mgr.MutedAgentIDs(tenantID); len(got) != 0 {
		t.Errorf("session 2 mute set = %v after a stale handle's write, want empty", got)
	}
	if err := stale.SayAs(context.Background(), bart.ID.String(), "hi"); err != session.ErrNoActiveSession {
		t.Errorf("rolled-over SayAs = %v, want ErrNoActiveSession", err)
	}

	// A FRESH handle to session 2 works: the staleness is per-handle, not sticky.
	fresh := mgr.Live(tenantID)
	if fresh == nil {
		t.Fatal("Live() during session 2 = nil, want a handle")
	}
	ids, err := fresh.SetAgentMute(context.Background(), bart.ID.String(), true)
	if err != nil {
		t.Fatalf("fresh SetAgentMute: %v", err)
	}
	if len(ids) != 1 || ids[0] != bart.ID.String() {
		t.Fatalf("fresh handle muted ids = %v, want [%s]", ids, bart.ID)
	}
}

// TestRegistry_ResolveTracksManagerLifecycle pins the Registry seam (#487,
// replacing the View): an idle Manager Resolves no session; a live one Resolves
// its started session by id through the lifecycle; after Stop the id no longer
// Resolves. (The old View's Snapshot-tracking, re-expressed against the keyed
// Registry — the double-bind panic it also guarded is gone by design, covered by
// TestRegistry_TwoManagersNoPanic.)
func TestRegistry_ResolveTracksManagerLifecycle(t *testing.T) {
	tenantID := uuid.New()
	reg := session.NewRegistry()
	store := newFakeStore()
	mgr := session.NewManager(store, reRunnableRunner,
		wirenpc.Config{Token: "test-token", Bus: voiceevent.NewBus()}, nil, slog.New(slog.DiscardHandler), true,
		session.Deps{Registry: reg})

	started, err := mgr.Start(context.Background(), tenantID, uuid.New())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(mgr.Shutdown)
	vs, ok := reg.Resolve(started.ID)
	if !ok || vs.ID != started.ID {
		t.Fatalf("Resolve(started) = (%v, %v), want the started session %s", vs.ID, ok, started.ID)
	}

	if _, err := mgr.Stop(context.Background(), tenantID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, ok := reg.Resolve(started.ID); ok {
		t.Fatal("Registry still Resolves the session after Stop, want gone")
	}
}
