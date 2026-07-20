package presence

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeOwnerStore scripts AcquireOrRenewPresenceOwner outcomes in order and records
// whether Release was called, so an elector test drives elections deterministically
// without a real Postgres.
type fakeOwnerStore struct {
	mu        sync.Mutex
	outcomes  []bool  // successive acquire results; the last repeats once exhausted
	errs      []error // parallel to outcomes; nil = success
	calls     int
	released  bool
	releaseCh chan struct{}
}

func (f *fakeOwnerStore) AcquireOrRenewPresenceOwner(_ context.Context, _ string, _ time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.calls
	if i >= len(f.outcomes) {
		i = len(f.outcomes) - 1
	}
	f.calls++
	var err error
	if i < len(f.errs) {
		err = f.errs[i]
	}
	return f.outcomes[i], err
}

func (f *fakeOwnerStore) ReleasePresenceOwner(_ context.Context, _ string) error {
	f.mu.Lock()
	f.released = true
	ch := f.releaseCh
	f.mu.Unlock()
	if ch != nil {
		close(ch)
	}
	return nil
}

func (f *fakeOwnerStore) wasReleased() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.released
}

// TestOwnerElectorGainAndLoss covers sequence (3): the elector fires onChange(true)
// when it wins the presence_owner row and onChange(false) when a later renew loses
// it — the gain/loss transitions that drive Registry.SetActive.
func TestOwnerElectorGainAndLoss(t *testing.T) {
	store := &fakeOwnerStore{outcomes: []bool{true, false}}
	ticks := make(chan time.Time)
	changes := make(chan bool, 8)

	e := NewOwnerElector(store, "instance-a", func(owner bool) { changes <- owner }, nil,
		OwnerElectorConfig{Interval: time.Hour, Expiry: 15 * time.Second})
	e.ticks = ticks

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()

	// The immediate boot reconcile wins the row.
	if got := <-changes; got != true {
		t.Fatalf("first onChange = %v, want true (won the election)", got)
	}
	// A tick renews and this time loses.
	ticks <- time.Now()
	if got := <-changes; got != false {
		t.Fatalf("second onChange = %v, want false (lost the election)", got)
	}

	cancel()
	<-done
}

// TestOwnerElectorReleasesOnCancel covers sequence (3): ctx cancellation (SIGTERM)
// releases the claim and signals loss, so a drained owner hands the row over
// immediately instead of forcing the fleet to wait out its expiry.
func TestOwnerElectorReleasesOnCancel(t *testing.T) {
	store := &fakeOwnerStore{outcomes: []bool{true}, releaseCh: make(chan struct{})}
	changes := make(chan bool, 8)

	e := NewOwnerElector(store, "instance-a", func(owner bool) { changes <- owner }, nil,
		OwnerElectorConfig{Interval: time.Hour, Expiry: 15 * time.Second})
	e.ticks = make(chan time.Time)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()

	if got := <-changes; got != true {
		t.Fatalf("first onChange = %v, want true", got)
	}

	cancel()
	<-done

	if !store.wasReleased() {
		t.Error("ctx cancel must Release the presence-owner claim")
	}
	// The shutdown must signal loss so Registry.SetActive(false) fires.
	if got := <-changes; got != false {
		t.Fatalf("shutdown onChange = %v, want false", got)
	}
}

// TestOwnerElectorSelfDemotesBeforeStealHorizon covers the #483 dual-owner window:
// a partitioned owner must have LOCALLY demoted (SetActive(false)) strictly before a
// challenger's steal horizon — heartbeat_at + Expiry in the DB — can promote a second
// owner, else both dispatch the same interaction for several seconds. The demotion
// check runs only on ticks (Interval apart) and a tick's acquire can itself burn
// opTimeout, so the threshold must be Expiry - Interval - opTimeout: the LAST tick
// before the threshold plus its op time still lands the demotion before Expiry.
// Models the challenger's clock by stepping ticks at Interval cadence and asserting
// the owner is demoted at a tick that completes before elapsed reaches Expiry — not
// merely "eventually". No Release on demotion (DB unreachable); re-promotion comes
// only via the next successful acquire.
func TestOwnerElectorSelfDemotesBeforeStealHorizon(t *testing.T) {
	store := &fakeOwnerStore{
		outcomes: []bool{true, true, true, true},
		errs:     []error{nil, errors.New("partition"), errors.New("partition"), nil},
	}
	changes := make(chan bool, 8)
	// Defaults: Interval 5s, Expiry 15s → opTimeout 3s → demotion threshold 7s.
	cfg := OwnerElectorConfig{Interval: 5 * time.Second, Expiry: 15 * time.Second}
	e := NewOwnerElector(store, "instance-a", func(owner bool) { changes <- owner }, nil, cfg)

	base := time.Now()
	clk := base
	e.now = func() time.Time { return clk }
	ctx := context.Background()

	// Boot: successful acquire → own, lastRenew stamped. The challenger's steal
	// horizon is base + Expiry (heartbeat_at was stamped at/before this renew).
	e.reconcile(ctx)
	if got := <-changes; got != true {
		t.Fatalf("boot onChange = %v, want true", got)
	}

	// First failing tick at Interval (elapsed 5s < 7s threshold): keep ownership —
	// a transient blip that also fails a challenger's acquire must not demote.
	clk = base.Add(cfg.Interval)
	e.reconcile(ctx)
	if len(changes) != 0 {
		t.Fatalf("demoted on the first failing tick: %d changes queued", len(changes))
	}

	// Second failing tick at 2×Interval (elapsed 10s ≥ 7s threshold): self-demote
	// NOW. Even if this tick's acquire burned its full opTimeout (3s), the local
	// deactivation lands at 13s — strictly before the 15s steal horizon.
	clk = base.Add(2 * cfg.Interval)
	e.reconcile(ctx)
	if got := <-changes; got != false {
		t.Fatalf("onChange at 2×Interval = %v, want false (self-demote)", got)
	}
	if worst := clk.Add(e.opTimeout()); !worst.Before(base.Add(cfg.Expiry)) {
		t.Fatalf("demotion tick + opTimeout = %v is not before the steal horizon %v",
			worst.Sub(base), cfg.Expiry)
	}
	if store.wasReleased() {
		t.Error("self-demotion must NOT Release the claim (DB unreachable; lease expiry hands over)")
	}

	// The DB recovers: the next successful acquire re-promotes.
	clk = base.Add(cfg.Expiry + cfg.Interval)
	e.reconcile(ctx)
	if got := <-changes; got != true {
		t.Fatalf("recovery onChange = %v, want true (re-promote via successful acquire)", got)
	}
}

// TestOwnerElectorErrorKeepsState covers sequence (3): a transient acquire error
// does not flip a live owner inactive — it logs and retains the last state, since a
// DB blip that fails our renew also fails a challenger's acquire. The cadence must
// leave real headroom (Interval < Expiry - opTimeout so demoteAfter > 0, #506
// re-review): the earlier Interval=1h clamped demoteAfter to 0, so the error tick
// self-demoted immediately and the test's final read mistook that demotion for the
// shutdown false — green for the wrong reason. With Interval 2s / Expiry 15s the
// demotion threshold is 11s, far above the sub-second real elapsed between the
// boot renew and the error tick, so the keeps-state branch is exercised for real.
func TestOwnerElectorErrorKeepsState(t *testing.T) {
	store := &fakeOwnerStore{
		outcomes: []bool{true, false, true},
		errs:     []error{nil, errors.New("db blip"), nil},
	}
	ticks := make(chan time.Time)
	changes := make(chan bool, 8)

	e := NewOwnerElector(store, "instance-a", func(owner bool) { changes <- owner }, nil,
		OwnerElectorConfig{Interval: 2 * time.Second, Expiry: 15 * time.Second})
	e.ticks = ticks

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()

	if got := <-changes; got != true {
		t.Fatalf("first onChange = %v, want true", got)
	}
	// A tick returns an error: state must NOT change (no onChange emitted).
	ticks <- time.Now()
	// A further tick succeeds as owner again: still no change (was already owner), so
	// the only remaining change would be shutdown's false.
	ticks <- time.Now()

	cancel()
	<-done
	if got := <-changes; got != false {
		t.Fatalf("after error/renew the only further change should be shutdown false, got %v", got)
	}
}
