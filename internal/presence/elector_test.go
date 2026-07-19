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

// TestOwnerElectorErrorKeepsState covers sequence (3): a transient acquire error
// does not flip a live owner inactive — it logs and retains the last state, since a
// DB blip that fails our renew also fails a challenger's acquire.
func TestOwnerElectorErrorKeepsState(t *testing.T) {
	store := &fakeOwnerStore{
		outcomes: []bool{true, false, true},
		errs:     []error{nil, errors.New("db blip"), nil},
	}
	ticks := make(chan time.Time)
	changes := make(chan bool, 8)

	e := NewOwnerElector(store, "instance-a", func(owner bool) { changes <- owner }, nil,
		OwnerElectorConfig{Interval: time.Hour, Expiry: 15 * time.Second})
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
