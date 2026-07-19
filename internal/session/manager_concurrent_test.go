package session_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/spend"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/wirenpc"
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

// TestIsCampaignLive_ScansAllSessions is #488 review item 2: the archive/delete
// live-guard truth must span ALL sessions, so a campaign live in ANOTHER Tenant's
// session reads live — at cap >1 a GM must not archive/DELETE it. Only the exact
// bound campaign of a live session reads true; a Stop clears just that one.
func TestIsCampaignLive_ScansAllSessions(t *testing.T) {
	mgr := concurrentManager(t, newFakeStore(), 2)
	t1, t2 := uuid.New(), uuid.New()
	c1, c2, foreign := uuid.New(), uuid.New(), uuid.New()

	if _, err := mgr.Start(context.Background(), t1, c1); err != nil {
		t.Fatalf("Start t1: %v", err)
	}
	if _, err := mgr.Start(context.Background(), t2, c2); err != nil {
		t.Fatalf("Start t2: %v", err)
	}

	// BOTH campaigns read live — the guard is process-wide, not one Tenant's.
	if !mgr.IsCampaignLive(c1) || !mgr.IsCampaignLive(c2) {
		t.Errorf("IsCampaignLive c1=%v c2=%v, want both true", mgr.IsCampaignLive(c1), mgr.IsCampaignLive(c2))
	}
	if mgr.IsCampaignLive(foreign) {
		t.Error("IsCampaignLive(foreign) = true, want false (no session runs it)")
	}

	// Stopping t1 clears only c1's liveness; c2 stays live.
	if _, err := mgr.Stop(context.Background(), t1); err != nil {
		t.Fatalf("Stop t1: %v", err)
	}
	if mgr.IsCampaignLive(c1) {
		t.Error("IsCampaignLive(c1) = true after Stop(t1), want false")
	}
	if !mgr.IsCampaignLive(c2) {
		t.Error("IsCampaignLive(c2) = false after Stop(t1), want still true")
	}
}

// TestSlowStart_DoesNotBlockOtherTenants is #488 review item 3: Start holds mu only
// to reserve a slot, then releases it for the store I/O — so one Tenant's slow Start
// (parked in GetDeploymentConfig) never freezes Active/Muted/Lookup for the others.
func TestSlowStart_DoesNotBlockOtherTenants(t *testing.T) {
	store := newFakeStore()
	gate := make(chan struct{})
	entered := make(chan struct{}, 1)
	store.depGate = gate // t1's Start will park inside GetDeploymentConfig
	store.depEntered = entered
	mgr := concurrentManager(t, store, 3)

	t1, t2 := uuid.New(), uuid.New()

	// t1's Start blocks in its I/O phase (holding only a reservation, not mu).
	startErr := make(chan error, 1)
	go func() { _, err := mgr.Start(context.Background(), t1, uuid.New()); startErr <- err }()
	<-entered // t1 is now parked in GetDeploymentConfig; the one-shot gate is cleared

	// While t1 is parked, the map-reading ops must NOT block. Run them on a deadline;
	// if Start held mu they would hang.
	done := make(chan struct{})
	go func() {
		mgr.Muted(uuid.NewString())   // voice hot path
		_, _ = mgr.Lookup(uuid.New()) // every bus event
		_, _, _ = mgr.Active(context.Background(), t2)
		_ = mgr.AnyLive()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("map reads blocked behind a slow Start holding mu (#488 item 3 regression)")
	}

	// A second Tenant can even Start fully while t1 is still parked.
	if _, err := mgr.Start(context.Background(), t2, uuid.New()); err != nil {
		t.Fatalf("Start t2 while t1 parked: %v", err)
	}

	// Release t1; it completes.
	close(gate)
	if err := <-startErr; err != nil {
		t.Fatalf("Start t1 after release: %v", err)
	}
	if _, ok, _ := mgr.Active(context.Background(), t1); !ok {
		t.Error("t1 not live after its slow Start completed")
	}
}

// TestReservationHoldsGuardsDuringSlowStart pins that the reservation counts toward
// both guards while the I/O runs unlocked (#488 item 3): a same-Tenant second Start
// collides ErrSessionActive, and the cap counts the reserving Tenant.
func TestReservationHoldsGuardsDuringSlowStart(t *testing.T) {
	store := newFakeStore()
	gate := make(chan struct{})
	entered := make(chan struct{}, 1)
	store.depGate = gate
	store.depEntered = entered
	mgr := concurrentManager(t, store, 1) // cap 1: the reservation fills the only slot

	tenant := uuid.New()
	startErr := make(chan error, 1)
	go func() { _, err := mgr.Start(context.Background(), tenant, uuid.New()); startErr <- err }()
	<-entered // the reservation is taken and the Start is parked in the store I/O

	// Same Tenant → ErrSessionActive (the reservation blocks it).
	if _, err := mgr.Start(context.Background(), tenant, uuid.New()); !errors.Is(err, session.ErrSessionActive) {
		t.Fatalf("same-tenant Start during reservation = %v, want ErrSessionActive", err)
	}
	// A different Tenant → ErrSessionLimit (the reservation fills the cap-1 slot).
	if _, err := mgr.Start(context.Background(), uuid.New(), uuid.New()); !errors.Is(err, session.ErrSessionLimit) {
		t.Fatalf("other-tenant Start during reservation = %v, want ErrSessionLimit", err)
	}

	close(gate)
	if err := <-startErr; err != nil {
		t.Fatalf("Start after release: %v", err)
	}
}

// gatedFinalizer parks inside runLoop's transcript Finalize (the session end window),
// so a test can probe the Manager while as.ended is set but m.active still holds the
// entry (#488 review item 6). It signals entered once, then waits for release/ctx.
type gatedFinalizer struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *gatedFinalizer) Finalize(ctx context.Context, _ uuid.UUID) (int, error) {
	g.once.Do(func() { close(g.entered) })
	select {
	case <-g.release:
	case <-ctx.Done():
	}
	return 0, nil
}

// TestEndWindow_MuteAndLiveRefused is #488 review item 6: once a session enters its
// end window (as.ended, the #487 tombstone), Live() hands out no handle and the
// tenant-scoped ops refuse ErrNoActiveSession — never publishing onto the detached
// bus and reporting a phantom success.
func TestEndWindow_MuteAndLiveRefused(t *testing.T) {
	store := newFakeStore()
	bart := seedAgents(store, 1)[0]
	fin := &gatedFinalizer{entered: make(chan struct{}), release: make(chan struct{})}
	mgr := newManagerDeps(t, store, reRunnableRunner, true, session.Deps{Transcript: fin})
	mgr.SetEndTimeoutForTest(2 * time.Second) // bound the parked Finalize
	// Cleanup uses sync.Once via a nil-safe release: a failed-early test leaves the
	// Finalize parked; Shutdown then waits out the 2s end budget. The happy path
	// closes release below before returning.
	t.Cleanup(mgr.Shutdown)

	tenant := uuid.New()
	if _, err := mgr.Start(context.Background(), tenant, uuid.New()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Trigger teardown: Stop cancels the loop; runLoop sets as.ended then parks in
	// the gated Finalize — the end window.
	stopped := make(chan struct{})
	go func() { _, _ = mgr.Stop(context.Background(), tenant); close(stopped) }()
	<-fin.entered // now as.ended == true, m.active[tenant] still present

	// Live hands out no handle during the end window.
	if l := mgr.Live(tenant); l != nil {
		t.Error("Live() during the end window returned a handle, want nil")
	}
	// Active reports the Tenant idle.
	if _, ok, _ := mgr.Active(context.Background(), tenant); ok {
		t.Error("Active() during the end window reported live, want false")
	}
	// A mute op refuses rather than publishing onto the detached bus.
	if _, err := mgr.SetAgentMute(context.Background(), tenant, bart.ID.String(), true); !errors.Is(err, session.ErrNoActiveSession) {
		t.Errorf("SetAgentMute during the end window = %v, want ErrNoActiveSession", err)
	}

	close(fin.release)
	<-stopped
}

// cfgCapturingRunner records each session's wirenpc.Config keyed by CampaignID, so
// a test can drive usage into ONE session's StageMetrics tee (#488 review item 4).
type cfgCapturingRunner struct {
	mu    sync.Mutex
	cfgs  map[uuid.UUID]wirenpc.Config
	ready chan struct{}
}

func newCfgCapturingRunner() *cfgCapturingRunner {
	return &cfgCapturingRunner{cfgs: map[uuid.UUID]wirenpc.Config{}, ready: make(chan struct{}, 8)}
}

func (r *cfgCapturingRunner) run(ctx context.Context, cfg wirenpc.Config) error {
	r.mu.Lock()
	r.cfgs[cfg.CampaignID] = cfg
	r.mu.Unlock()
	r.ready <- struct{}{}
	<-ctx.Done()
	return ctx.Err()
}

func (r *cfgCapturingRunner) cfg(campaignID uuid.UUID) wirenpc.Config {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cfgs[campaignID]
}

// TestSpendMetersIndependentPerTenant is #488 test-sequence (6, meter half): each
// Tenant's session has its OWN spend meter — usage recorded into t1's session moves
// t1's Spend and leaves t2's untouched. (The allowance snapshot caveat is
// re-documented, not fixed — see the allowance block in manager.go.)
func TestSpendMetersIndependentPerTenant(t *testing.T) {
	store := newFakeStore()
	soft, hard := 0.01, 1000.0 // tiny soft so one big call crosses it; roomy hard
	store.caps = storage.SpendCaps{SoftUSD: &soft, HardUSD: &hard}
	runner := newCfgCapturingRunner()
	mgr := newManagerDeps(t, store, runner.run, true, session.Deps{MaxSessions: 2})
	t.Cleanup(mgr.Shutdown)

	t1, t2 := uuid.New(), uuid.New()
	c1, c2 := uuid.New(), uuid.New()
	if _, err := mgr.Start(context.Background(), t1, c1); err != nil {
		t.Fatalf("Start t1: %v", err)
	}
	if _, err := mgr.Start(context.Background(), t2, c2); err != nil {
		t.Fatalf("Start t2: %v", err)
	}
	<-runner.ready
	<-runner.ready

	// Both meters start at zero.
	if s := mgr.Spend(t1); s.EstimatedUSD != 0 {
		t.Fatalf("Spend(t1) at start = %v, want 0", s.EstimatedUSD)
	}
	if s := mgr.Spend(t2); s.EstimatedUSD != 0 {
		t.Fatalf("Spend(t2) at start = %v, want 0", s.EstimatedUSD)
	}

	// Drive a big LLM call into t1's session recorder ONLY (the tee feeds t1's meter).
	bigGroqLLM(runner.cfg(c1).StageMetrics)

	s1 := mgr.Spend(t1)
	if s1.EstimatedUSD <= 0 || s1.State != spend.CapSoft {
		t.Fatalf("Spend(t1) after usage = %+v, want positive estimate + soft state", s1)
	}
	// t2's meter is UNTOUCHED — the meters are per-session, not shared.
	if s2 := mgr.Spend(t2); s2.EstimatedUSD != 0 || s2.State == spend.CapSoft {
		t.Fatalf("Spend(t2) = %+v, want still zero (t1's usage must not leak into t2)", s2)
	}
	// An unknown Tenant reports the feature-off zero Status.
	if s := mgr.Spend(uuid.New()); s.EstimatedUSD != 0 || s.State != "" {
		t.Errorf("Spend(unknown tenant) = %+v, want the zero Status", s)
	}
}
