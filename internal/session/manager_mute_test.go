package session_test

import (
	"context"
	"log/slog"
	"sort"
	"sync"
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
		wirenpc.Config{Token: "test-token", Bus: bus}, nil, slog.New(slog.DiscardHandler), true, session.Deps{})
	return mgr, bus
}

// startMuteSession starts a session and returns its campaign id.
func startMuteSession(t *testing.T, mgr *session.Manager) (tenantID, campaignID uuid.UUID) {
	t.Helper()
	tenantID, campaignID = uuid.New(), uuid.New()
	if _, err := mgr.Start(context.Background(), tenantID, campaignID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(mgr.Shutdown)
	return tenantID, campaignID
}

// seedAgents makes n agents and puts them on the store's ListAgents roster (the
// membership SetAgentMute now validates against).
func seedAgents(store *fakeStore, n int) []storage.Agent {
	agents := make([]storage.Agent, n)
	for i := range agents {
		agents[i] = storage.Agent{ID: uuid.New(), Role: storage.AgentRoleCharacter}
	}
	store.mu.Lock()
	store.agents = agents
	store.mu.Unlock()
	return agents
}

// TestSetAgentMute_IdleReturnsNoActiveSession pins the active-session requirement
// (AC4): muting with no live session is refused before any roster lookup.
func TestSetAgentMute_IdleReturnsNoActiveSession(t *testing.T) {
	mgr, _ := muteManager(t, newFakeStore())
	if _, err := mgr.SetAgentMute(context.Background(), uuid.New(), uuid.NewString(), true); err != session.ErrNoActiveSession {
		t.Fatalf("SetAgentMute while idle = %v, want ErrNoActiveSession", err)
	}
}

// TestSetAgentMute_ForeignAgentRejected pins the campaign-membership guard: an
// agent not in the active session's roster is refused ErrAgentNotInCampaign.
func TestSetAgentMute_ForeignAgentRejected(t *testing.T) {
	store := newFakeStore()
	seedAgents(store, 1)
	mgr, _ := muteManager(t, store)
	tenantID, _ := startMuteSession(t, mgr)
	if _, err := mgr.SetAgentMute(context.Background(), tenantID, uuid.NewString(), true); err != session.ErrAgentNotInCampaign {
		t.Fatalf("muting a foreign agent = %v, want ErrAgentNotInCampaign", err)
	}
}

// TestSetAgentMute_ButlerRejected pins the Address-Only Butler exclusion
// (ADR-0009/ADR-0024): the auto-created Butler is a real Agent of the Active
// Campaign (present in ListAgents) but is never voiced, so muting it is refused
// ErrAgentNotInCampaign and records NO phantom id — MutedAgentIDs stays empty, so
// GetSession's reload truth never shows the Butler as muted. The voiced Character
// NPC alongside it stays muteable.
func TestSetAgentMute_ButlerRejected(t *testing.T) {
	store := newFakeStore()
	butler := storage.Agent{ID: uuid.New(), Role: storage.AgentRoleButler, Name: "Butler"}
	bart := storage.Agent{ID: uuid.New(), Role: storage.AgentRoleCharacter, Name: "Bart"}
	store.agents = []storage.Agent{butler, bart}
	mgr, _ := muteManager(t, store)
	tenantID, _ := startMuteSession(t, mgr)

	if _, err := mgr.SetAgentMute(context.Background(), tenantID, butler.ID.String(), true); err != session.ErrAgentNotInCampaign {
		t.Fatalf("muting the Butler = %v, want ErrAgentNotInCampaign", err)
	}
	if mgr.Muted(butler.ID.String()) {
		t.Fatal("a rejected Butler mute must not enter the mute set")
	}
	if got := mgr.MutedAgentIDs(tenantID); len(got) != 0 {
		t.Fatalf("MutedAgentIDs after rejected Butler mute = %v, want none (no phantom id)", got)
	}
	// The voiced Character NPC is still muteable.
	if _, err := mgr.SetAgentMute(context.Background(), tenantID, bart.ID.String(), true); err != nil {
		t.Fatalf("muting a voiced Character = %v, want success", err)
	}
}

// TestSetAgentMute_PublishesOncePerActualChange pins the idempotent publish (test
// 8): a mute publishes exactly one MuteChanged; a redundant re-mute publishes
// none; an unmute publishes one. The set + the returned sorted ids track each.
func TestSetAgentMute_PublishesOncePerActualChange(t *testing.T) {
	store := newFakeStore()
	bart := seedAgents(store, 1)[0]
	bartID := bart.ID.String()
	mgr, bus := muteManager(t, store)
	tenantID, _ := startMuteSession(t, mgr)

	var mu sync.Mutex
	var events []voiceevent.MuteChanged
	t.Cleanup(voiceevent.On(bus, func(e voiceevent.MuteChanged) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	}))
	eventCount := func() int { mu.Lock(); defer mu.Unlock(); return len(events) }

	ids, err := mgr.SetAgentMute(context.Background(), tenantID, bartID, true)
	if err != nil {
		t.Fatalf("SetAgentMute: %v", err)
	}
	if len(ids) != 1 || ids[0] != bartID {
		t.Fatalf("muted ids = %v, want [%s]", ids, bartID)
	}
	if !mgr.Muted(bartID) {
		t.Fatal("Muted(bart) = false after mute, want true")
	}
	if eventCount() != 1 {
		t.Fatalf("events after mute = %d, want 1", eventCount())
	}

	// Idempotent re-mute publishes nothing and stays muted.
	if _, err := mgr.SetAgentMute(context.Background(), tenantID, bartID, true); err != nil {
		t.Fatalf("re-mute: %v", err)
	}
	if eventCount() != 1 {
		t.Fatalf("re-mute published %d events, want 0 more (idempotent)", eventCount()-1)
	}

	// Unmute publishes one and clears the set.
	ids, err = mgr.SetAgentMute(context.Background(), tenantID, bartID, false)
	if err != nil {
		t.Fatalf("unmute: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("muted ids after unmute = %v, want empty", ids)
	}
	if mgr.Muted(bartID) {
		t.Fatal("Muted(bart) = true after unmute")
	}
	if eventCount() != 2 {
		t.Fatalf("events after unmute = %d, want 2", eventCount())
	}
}

// TestMutedAgentIDs_SortedSnapshot pins the reload truth (AC5): MutedAgentIDs is a
// sorted snapshot of the current muted set.
func TestMutedAgentIDs_SortedSnapshot(t *testing.T) {
	store := newFakeStore()
	agents := seedAgents(store, 2)
	mgr, _ := muteManager(t, store)
	tenantID, _ := startMuteSession(t, mgr)
	if got := mgr.MutedAgentIDs(tenantID); len(got) != 0 {
		t.Fatalf("a fresh session has muted ids %v, want none (AC5 all-unmuted at start)", got)
	}
	for _, a := range agents {
		if _, err := mgr.SetAgentMute(context.Background(), tenantID, a.ID.String(), true); err != nil {
			t.Fatalf("mute %s: %v", a.ID, err)
		}
	}
	got := mgr.MutedAgentIDs(tenantID)
	if len(got) != 2 {
		t.Fatalf("MutedAgentIDs = %v, want 2", got)
	}
	if !sort.StringsAreSorted(got) {
		t.Fatalf("MutedAgentIDs = %v, want sorted", got)
	}
}

// TestStartResetsMuteSet pins AC5: every new Voice Session starts with all Agents
// unmuted — the volatile set is fresh per Start, so a mute from a prior session
// does not leak.
func TestStartResetsMuteSet(t *testing.T) {
	tenantID := uuid.New()
	store := newFakeStore()
	bart := seedAgents(store, 1)[0]
	bartID := bart.ID.String()
	mgr, _ := muteManager(t, store)
	if _, err := mgr.Start(context.Background(), tenantID, uuid.New()); err != nil {
		t.Fatalf("Start 1: %v", err)
	}
	if _, err := mgr.SetAgentMute(context.Background(), tenantID, bartID, true); err != nil {
		t.Fatalf("mute: %v", err)
	}
	if _, err := mgr.Stop(context.Background(), tenantID); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if _, err := mgr.Start(context.Background(), tenantID, uuid.New()); err != nil {
		t.Fatalf("Start 2: %v", err)
	}
	t.Cleanup(mgr.Shutdown)
	if mgr.Muted(bartID) {
		t.Fatal("a new Voice Session must start with Bart unmuted (AC5)")
	}
	if got := mgr.MutedAgentIDs(tenantID); len(got) != 0 {
		t.Fatalf("a new session has muted ids %v, want none", got)
	}
}

// TestSetAllMute_MutesEveryVoicedAgent pins muteall (test 8): SetAllMute(true)
// mutes every VOICED Agent of the Active Campaign — the Character NPCs — with one
// MuteChanged each, and EXCLUDES the Address-Only Butler (never voiced,
// ADR-0009/ADR-0024), which therefore never enters the mute set. SetAllMute(false)
// clears the voiced Agents.
func TestSetAllMute_MutesEveryVoicedAgent(t *testing.T) {
	store := newFakeStore()
	butler := storage.Agent{ID: uuid.New(), Role: storage.AgentRoleButler, Name: "Butler"}
	bart := storage.Agent{ID: uuid.New(), Role: storage.AgentRoleCharacter, Name: "Bart"}
	store.agents = []storage.Agent{butler, bart}

	mgr, bus := muteManager(t, store)
	tenantID, _ := startMuteSession(t, mgr)

	var mu sync.Mutex
	var events []voiceevent.MuteChanged
	t.Cleanup(voiceevent.On(bus, func(e voiceevent.MuteChanged) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	}))
	eventCount := func() int { mu.Lock(); defer mu.Unlock(); return len(events) }

	ids, err := mgr.SetAllMute(context.Background(), tenantID, true)
	if err != nil {
		t.Fatalf("SetAllMute(true): %v", err)
	}
	if len(ids) != 1 || ids[0] != bart.ID.String() {
		t.Fatalf("muted ids after mute-all = %v, want [%s] (Bart only; Butler excluded)", ids, bart.ID)
	}
	if mgr.Muted(butler.ID.String()) {
		t.Fatal("mute-all must NOT mute the Address-Only Butler")
	}
	if !mgr.Muted(bart.ID.String()) {
		t.Fatal("mute-all must mute the voiced Character NPC Bart")
	}
	if eventCount() != 1 {
		t.Fatalf("mute-all published %d events, want 1 (one per voiced Agent)", eventCount())
	}

	mu.Lock()
	events = nil
	mu.Unlock()
	ids, err = mgr.SetAllMute(context.Background(), tenantID, false)
	if err != nil {
		t.Fatalf("SetAllMute(false): %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("muted ids after unmute-all = %v, want none", ids)
	}
	if eventCount() != 1 {
		t.Fatalf("unmute-all published %d events, want 1", eventCount())
	}
}

// TestSetAllMute_IdleReturnsNoActiveSession pins the active-session requirement.
func TestSetAllMute_IdleReturnsNoActiveSession(t *testing.T) {
	mgr, _ := muteManager(t, newFakeStore())
	if _, err := mgr.SetAllMute(context.Background(), uuid.New(), true); err != session.ErrNoActiveSession {
		t.Fatalf("SetAllMute while idle = %v, want ErrNoActiveSession", err)
	}
}

// TestMute_ConcurrentOpsConvergeSubscriberToManager pins the cross-op publish
// ordering fix (#211): SetAllMute racing a per-Agent unmute must never leave a
// re-reading subscriber (the wirenpc matcher) out of sync with the Manager's
// authoritative set. A subscriber that re-reads Muted() on every event ends
// EXACTLY matching Manager.Muted for every Agent, regardless of interleave. Run
// with -race.
func TestMute_ConcurrentOpsConvergeSubscriberToManager(t *testing.T) {
	store := newFakeStore()
	agents := seedAgents(store, 6)
	mgr, bus := muteManager(t, store)
	tenantID, _ := startMuteSession(t, mgr)

	// The subscriber mimics wirenpc.wireMutes: on each event it re-reads the
	// authoritative view (Manager.Muted), NOT the event payload.
	var mu sync.Mutex
	applied := map[string]bool{}
	t.Cleanup(voiceevent.On(bus, func(e voiceevent.MuteChanged) {
		v := mgr.Muted(e.AgentID)
		mu.Lock()
		applied[e.AgentID] = v
		mu.Unlock()
	}))

	// Interleave a mute-all with per-Agent unmutes of half the roster.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = mgr.SetAllMute(context.Background(), tenantID, true)
	}()
	for i := 0; i < len(agents); i += 2 {
		a := agents[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = mgr.SetAgentMute(context.Background(), tenantID, a.ID.String(), false)
		}()
	}
	wg.Wait()

	// After the dust settles, the subscriber's applied view must match the
	// Manager's authoritative Muted for every Agent that saw an event.
	mu.Lock()
	defer mu.Unlock()
	for id, gotMuted := range applied {
		if want := mgr.Muted(id); gotMuted != want {
			t.Fatalf("subscriber diverged for %s: applied muted=%v, Manager.Muted=%v", id, gotMuted, want)
		}
	}
}
