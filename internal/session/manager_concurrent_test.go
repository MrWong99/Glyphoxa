package session_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// concurrentManager builds a voice-enabled Manager with the given cap over a fresh
// store, using the re-runnable blocking runner so N sessions can be live at once.
func concurrentManager(t *testing.T, store session.Store, maxSessions int) *session.Manager {
	t.Helper()
	mgr := newManagerDeps(t, store, reRunnableRunner, true, session.Deps{MaxSessions: maxSessions})
	t.Cleanup(mgr.Shutdown)
	return mgr
}

// TestTwoTenants_TwoLiveSessions is #488 test-sequence (1): with the cap raised,
// two different Tenants run concurrent live Voice Sessions; each is Active for its
// OWN Tenant only and both Lookup by id.
func TestTwoTenants_TwoLiveSessions(t *testing.T) {
	mgr := concurrentManager(t, newFakeStore(), 2)
	t1, t2 := uuid.New(), uuid.New()
	c1, c2 := uuid.New(), uuid.New()

	vs1, err := mgr.Start(context.Background(), t1, c1)
	if err != nil {
		t.Fatalf("Start t1: %v", err)
	}
	vs2, err := mgr.Start(context.Background(), t2, c2)
	if err != nil {
		t.Fatalf("Start t2: %v", err)
	}
	if vs1.ID == vs2.ID {
		t.Fatal("two sessions share an id")
	}

	// Each Tenant sees ONLY its own session.
	if got, ok, _ := mgr.Active(context.Background(), t1); !ok || got.ID != vs1.ID {
		t.Errorf("Active(t1) = %v/%v, want %s", got.ID, ok, vs1.ID)
	}
	if got, ok, _ := mgr.Active(context.Background(), t2); !ok || got.ID != vs2.ID {
		t.Errorf("Active(t2) = %v/%v, want %s", got.ID, ok, vs2.ID)
	}
	// Both resolve by id across the whole map.
	if got, ok := mgr.Lookup(vs1.ID); !ok || got.ID != vs1.ID {
		t.Errorf("Lookup(vs1) failed: %v/%v", got.ID, ok)
	}
	if got, ok := mgr.Lookup(vs2.ID); !ok || got.ID != vs2.ID {
		t.Errorf("Lookup(vs2) failed: %v/%v", got.ID, ok)
	}
}

// TestSameTenantSecondStart_ErrSessionActive is #488 test-sequence (2): a second
// Start for the SAME Tenant — even for a different Campaign — still collides with
// ErrSessionActive, the per-Tenant single-active guard (AC1).
func TestSameTenantSecondStart_ErrSessionActive(t *testing.T) {
	mgr := concurrentManager(t, newFakeStore(), 4) // cap is not the limiter here
	tenant := uuid.New()

	if _, err := mgr.Start(context.Background(), tenant, uuid.New()); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	// A different Campaign, same Tenant → ErrSessionActive (not ErrSessionLimit).
	_, err := mgr.Start(context.Background(), tenant, uuid.New())
	if !errors.Is(err, session.ErrSessionActive) {
		t.Fatalf("second Start (same tenant, other campaign) = %v, want ErrSessionActive", err)
	}
}

// TestProcessCap_ThirdTenantErrSessionLimit is #488 test-sequence (3): with
// MaxSessions=2, a start for a THIRD Tenant is rejected with the distinct,
// user-visible ErrSessionLimit (AC2), while the two live sessions keep running.
func TestProcessCap_ThirdTenantErrSessionLimit(t *testing.T) {
	mgr := concurrentManager(t, newFakeStore(), 2)
	t1, t2, t3 := uuid.New(), uuid.New(), uuid.New()

	if _, err := mgr.Start(context.Background(), t1, uuid.New()); err != nil {
		t.Fatalf("Start t1: %v", err)
	}
	if _, err := mgr.Start(context.Background(), t2, uuid.New()); err != nil {
		t.Fatalf("Start t2: %v", err)
	}
	_, err := mgr.Start(context.Background(), t3, uuid.New())
	if !errors.Is(err, session.ErrSessionLimit) {
		t.Fatalf("Start over cap = %v, want ErrSessionLimit", err)
	}
	// The two live sessions are undisturbed.
	if _, ok, _ := mgr.Active(context.Background(), t1); !ok {
		t.Error("t1 session died after an over-cap start attempt")
	}
	if _, ok, _ := mgr.Active(context.Background(), t2); !ok {
		t.Error("t2 session died after an over-cap start attempt")
	}
	// A slot freed by a Stop admits the third Tenant.
	if _, err := mgr.Stop(context.Background(), t1); err != nil {
		t.Fatalf("Stop t1: %v", err)
	}
	if _, err := mgr.Start(context.Background(), t3, uuid.New()); err != nil {
		t.Fatalf("Start t3 after a slot freed: %v", err)
	}
}

// TestDefaultCapIsOne pins the release gate: an unset MaxSessions (Deps zero value)
// keeps today's single-session behaviour byte-identical — a second Tenant is
// refused ErrSessionLimit, so raising the cap is the ONLY thing that enables
// concurrency (#488 rollout note).
func TestDefaultCapIsOne(t *testing.T) {
	mgr := concurrentManager(t, newFakeStore(), 0) // 0 → clamped to 1
	if _, err := mgr.Start(context.Background(), uuid.New(), uuid.New()); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if _, err := mgr.Start(context.Background(), uuid.New(), uuid.New()); !errors.Is(err, session.ErrSessionLimit) {
		t.Fatalf("second Tenant under default cap = %v, want ErrSessionLimit", err)
	}
}

// TestInterleavedStop_LeavesOtherSessionUntouched is #488 test-sequence (4):
// Stop(t1) tears down ONLY t1 — t2 stays live and Lookup-able, and t1's row is
// terminal.
func TestInterleavedStop_LeavesOtherSessionUntouched(t *testing.T) {
	store := newFakeStore()
	mgr := concurrentManager(t, store, 2)
	t1, t2 := uuid.New(), uuid.New()

	vs1, err := mgr.Start(context.Background(), t1, uuid.New())
	if err != nil {
		t.Fatalf("Start t1: %v", err)
	}
	vs2, err := mgr.Start(context.Background(), t2, uuid.New())
	if err != nil {
		t.Fatalf("Start t2: %v", err)
	}

	ended, err := mgr.Stop(context.Background(), t1)
	if err != nil {
		t.Fatalf("Stop t1: %v", err)
	}
	if ended.Status != storage.VoiceSessionEnded || ended.EndedAt == nil {
		t.Errorf("t1 row = %+v, want ended with ended_at", ended)
	}
	// t1 is gone; t2 is untouched.
	if _, ok, _ := mgr.Active(context.Background(), t1); ok {
		t.Error("t1 still Active after its Stop")
	}
	if _, ok := mgr.Lookup(vs1.ID); ok {
		t.Error("t1 still Lookup-able after its Stop")
	}
	if got, ok, _ := mgr.Active(context.Background(), t2); !ok || got.ID != vs2.ID {
		t.Errorf("t2 disturbed by t1's Stop: %v/%v", got.ID, ok)
	}
	if _, ok := mgr.Lookup(vs2.ID); !ok {
		t.Error("t2 no longer Lookup-able after t1's Stop")
	}
}

// TestShutdownEndsAllSessions is #488 test-sequence (5): Shutdown cancels EVERY
// live session and waits for each terminal row, leaving none active and refusing
// new Starts (ErrManagerClosed).
func TestShutdownEndsAllSessions(t *testing.T) {
	store := newFakeStore()
	mgr := newManagerDeps(t, store, reRunnableRunner, true, session.Deps{MaxSessions: 3})
	t1, t2, t3 := uuid.New(), uuid.New(), uuid.New()
	for _, tn := range []uuid.UUID{t1, t2, t3} {
		if _, err := mgr.Start(context.Background(), tn, uuid.New()); err != nil {
			t.Fatalf("Start %s: %v", tn, err)
		}
	}

	mgr.Shutdown()

	if _, ended := store.counts(); ended != 3 {
		t.Fatalf("ended rows after Shutdown = %d, want 3 (all sessions terminal)", ended)
	}
	for _, tn := range []uuid.UUID{t1, t2, t3} {
		if _, ok, _ := mgr.Active(context.Background(), tn); ok {
			t.Errorf("session %s still active after Shutdown", tn)
		}
	}
	if _, err := mgr.Start(context.Background(), uuid.New(), uuid.New()); !errors.Is(err, session.ErrManagerClosed) {
		t.Fatalf("Start after Shutdown = %v, want ErrManagerClosed", err)
	}
}

// TestMuteIsolationPerTenant is #488 test-sequence (7): each Tenant's mute set is
// independent, and Muted(agentID) resolves across the whole map (agent ids are
// globally unique) — muting t1's agent never mutes t2's, and MutedAgentIDs is
// scoped per Tenant.
func TestMuteIsolationPerTenant(t *testing.T) {
	store := newFakeStore()
	// Two campaigns' rosters, one agent each; ListAgents returns the shared roster
	// (the fake is campaign-agnostic), so both agents validate — sufficient to prove
	// the mute SETS are per-session.
	a1 := storage.Agent{ID: uuid.New(), Role: storage.AgentRoleCharacter, Name: "A1"}
	a2 := storage.Agent{ID: uuid.New(), Role: storage.AgentRoleCharacter, Name: "A2"}
	store.mu.Lock()
	store.agents = []storage.Agent{a1, a2}
	store.mu.Unlock()

	mgr := concurrentManager(t, store, 2)
	t1, t2 := uuid.New(), uuid.New()
	if _, err := mgr.Start(context.Background(), t1, uuid.New()); err != nil {
		t.Fatalf("Start t1: %v", err)
	}
	if _, err := mgr.Start(context.Background(), t2, uuid.New()); err != nil {
		t.Fatalf("Start t2: %v", err)
	}

	if _, err := mgr.SetAgentMute(context.Background(), t1, a1.ID.String(), true); err != nil {
		t.Fatalf("mute a1 in t1: %v", err)
	}

	// t1's set has a1; t2's set is empty.
	if got := mgr.MutedAgentIDs(t1); len(got) != 1 || got[0] != a1.ID.String() {
		t.Errorf("MutedAgentIDs(t1) = %v, want [%s]", got, a1.ID)
	}
	if got := mgr.MutedAgentIDs(t2); len(got) != 0 {
		t.Errorf("MutedAgentIDs(t2) = %v, want empty (isolation)", got)
	}
	// Muted(agentID) scans all sessions: a1 muted (in t1), a2 not.
	if !mgr.Muted(a1.ID.String()) {
		t.Error("Muted(a1) = false, want true (muted in t1)")
	}
	if mgr.Muted(a2.ID.String()) {
		t.Error("Muted(a2) = true, want false (never muted)")
	}
}

// TestSpendMetersIndependentPerTenant is #488 test-sequence (6, meter half): each
// Tenant's session snapshots its OWN spend caps and reports its OWN Spend — one
// Tenant's meter never reads another's. (The concurrent-Start allowance caveat is
// re-documented, not fixed — see the allowance block in manager.go.)
func TestSpendMetersIndependentPerTenant(t *testing.T) {
	store := newFakeStore()
	hard := 10.0
	store.caps = storage.SpendCaps{HardUSD: &hard}
	mgr := concurrentManager(t, store, 2)
	t1, t2 := uuid.New(), uuid.New()
	if _, err := mgr.Start(context.Background(), t1, uuid.New()); err != nil {
		t.Fatalf("Start t1: %v", err)
	}
	if _, err := mgr.Start(context.Background(), t2, uuid.New()); err != nil {
		t.Fatalf("Start t2: %v", err)
	}

	// Each Tenant has its own meter (caps configured), reporting zero spend so far;
	// an idle/unknown Tenant reports the feature-off zero Status.
	if s1 := mgr.Spend(t1); s1.EstimatedUSD != 0 {
		t.Errorf("Spend(t1) estimated = %v, want 0 at start", s1.EstimatedUSD)
	}
	if s := mgr.Spend(uuid.New()); s.EstimatedUSD != 0 || s.State != "" {
		t.Errorf("Spend(unknown tenant) = %+v, want the zero Status", s)
	}
	// Stopping t1 leaves t2's meter intact.
	if _, err := mgr.Stop(context.Background(), t1); err != nil {
		t.Fatalf("Stop t1: %v", err)
	}
	if _, ok, _ := mgr.Active(context.Background(), t2); !ok {
		t.Error("t2 meter/session disturbed by t1 Stop")
	}
}
