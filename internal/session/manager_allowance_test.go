package session_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/spend"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/wirenpc"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// fakeAllowance is a scripted session.AllowanceChecker recording which tenant
// it was consulted for.
type fakeAllowance struct {
	mu       sync.Mutex
	state    spend.AllowanceState
	err      error
	gotFor   uuid.UUID
	consults int
}

func (f *fakeAllowance) AllowanceState(_ context.Context, tenantID uuid.UUID) (spend.AllowanceState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.consults++
	f.gotFor = tenantID
	return f.state, f.err
}

// allowanceManager builds a Manager with the checker wired via Deps.Allowance.
func allowanceManager(t *testing.T, store session.Store, run session.LoopRunner, bus *voiceevent.Bus, chk session.AllowanceChecker) *session.Manager {
	t.Helper()
	return session.NewManager(store, run,
		wirenpc.Config{Token: "test-token", Bus: bus}, nil,
		slog.New(slog.DiscardHandler), true, session.Deps{Allowance: chk})
}

// TestStart_AllowanceExhausted_Refused pins the ADR-0055 gate (b) start
// refusal: an exhausted allowance fails Start with ErrAllowanceExhausted and
// strands no 'running' row (#433).
func TestStart_AllowanceExhausted_Refused(t *testing.T) {
	store := newFakeStore()
	chk := &fakeAllowance{state: spend.AllowanceState{IncludedUSD: fp(15), MonthUSD: 15}}
	tenantID := uuid.New()
	mgr := allowanceManager(t, store, newReRunner().run, voiceevent.NewBus(), chk)

	_, err := mgr.Start(context.Background(), tenantID, uuid.New())
	if !errors.Is(err, session.ErrAllowanceExhausted) {
		t.Fatalf("Start = %v, want ErrAllowanceExhausted", err)
	}
	if n := store.runningCount(); n != 0 {
		t.Fatalf("running rows after refused Start = %d, want 0 (#433)", n)
	}
	if chk.gotFor != tenantID {
		t.Errorf("allowance consulted for %s, want %s", chk.gotFor, tenantID)
	}
	mgr.Shutdown()
}

// TestStart_AllowanceReadError_FailsClosed: a checker failure fails Start (no
// fail-open) and strands nothing.
func TestStart_AllowanceReadError_FailsClosed(t *testing.T) {
	store := newFakeStore()
	chk := &fakeAllowance{err: errors.New("billing db down")}
	mgr := allowanceManager(t, store, newReRunner().run, voiceevent.NewBus(), chk)

	if _, err := mgr.Start(context.Background(), uuid.New(), uuid.New()); err == nil {
		t.Fatal("Start must fail when the allowance read fails")
	}
	if n := store.runningCount(); n != 0 {
		t.Fatalf("running rows after failed Start = %d, want 0 (#433)", n)
	}
	mgr.Shutdown()
}

// TestStart_NoAllowance_ByteForByte: a wired checker whose tenant has no
// configured allowance changes nothing — no gate, no meter.
func TestStart_NoAllowance_ByteForByte(t *testing.T) {
	store := newFakeStore() // no tenant caps either
	runner := newReRunner()
	chk := &fakeAllowance{} // IncludedUSD nil
	mgr := allowanceManager(t, store, runner.run, voiceevent.NewBus(), chk)

	if _, err := mgr.Start(context.Background(), uuid.New(), uuid.New()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-runner.started
	if runner.cfg().Gate != nil {
		t.Fatal("no allowance and no caps: cfg.Gate must stay nil")
	}
	mgr.Shutdown()
}

// TestAllowanceRemaining_TightensHardCap_EndsWithAllowanceReason pins the
// mid-session mechanics: a remaining allowance below any tenant cap becomes
// the session's hard cap, and crossing it ends the session cleanly with the
// DISTINCT allowance_exhausted end reason (not spend_cap_hard).
func TestAllowanceRemaining_TightensHardCap_EndsWithAllowanceReason(t *testing.T) {
	store := newFakeStore() // no tenant caps: the allowance is the only bound
	// $15 allowance, $14.20 already spent this month -> $0.80 remaining; one
	// bigGroqLLM call (~$1.38) crosses it.
	chk := &fakeAllowance{state: spend.AllowanceState{IncludedUSD: fp(15), MonthUSD: 14.2}}
	runner := newReRunner()
	bus := voiceevent.NewBus()
	events := collectSpendCaps(bus)
	mgr := allowanceManager(t, store, runner.run, bus, chk)

	vs, err := mgr.Start(context.Background(), uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-runner.started

	bigGroqLLM(runner.cfg().StageMetrics)
	waitInactive(t, mgr)

	closed := store.session(vs.ID)
	if closed.Status != storage.VoiceSessionEnded {
		t.Fatalf("row status = %q, want ended (a policy stop is not a fault)", closed.Status)
	}
	if closed.EndReason == nil || *closed.EndReason != "allowance_exhausted: estimated spend crossed the plan's monthly allowance" {
		t.Fatalf("end_reason = %v, want the allowance_exhausted reason", closed.EndReason)
	}
	if evs := events(); len(evs) != 1 || evs[0].Level != voiceevent.SpendCapHard {
		t.Fatalf("spend-cap events = %+v, want one hard", evs)
	}
	mgr.Shutdown()
}

// TestTenantCapBelowAllowance_KeepsSpendCapReason: when the tenant's own hard
// cap is the binding constraint, the end reason stays spend_cap_hard — the
// allowance only renames the stop it actually caused.
func TestTenantCapBelowAllowance_KeepsSpendCapReason(t *testing.T) {
	store := newFakeStore()
	store.caps = storage.SpendCaps{HardUSD: fp(1.0)} // binding: below the remaining allowance
	chk := &fakeAllowance{state: spend.AllowanceState{IncludedUSD: fp(50), MonthUSD: 0}}
	runner := newReRunner()
	mgr := allowanceManager(t, store, runner.run, voiceevent.NewBus(), chk)

	vs, err := mgr.Start(context.Background(), uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-runner.started

	bigGroqLLM(runner.cfg().StageMetrics)
	waitInactive(t, mgr)

	closed := store.session(vs.ID)
	if closed.EndReason == nil || *closed.EndReason != "spend_cap_hard: estimated spend crossed the hard cap" {
		t.Fatalf("end_reason = %v, want spend_cap_hard (the tenant cap was binding)", closed.EndReason)
	}
	mgr.Shutdown()
}
