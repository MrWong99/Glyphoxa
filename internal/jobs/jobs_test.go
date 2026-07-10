package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// --- fakes ---

type retryCall struct {
	id        uuid.UUID
	attempts  int
	lastError string
	runAfter  time.Time
}

type deadCall struct {
	id        uuid.UUID
	attempts  int
	lastError string
}

// fakeStore hands out queued jobs to ClaimJob (then ErrNotFound) and records every
// terminal write, so a test can assert the routing without a real DB.
type fakeStore struct {
	mu         sync.Mutex
	claimQueue []storage.Job
	lastLease  time.Duration
	backlog    map[string]int

	// terminalErr, when set, is returned by every terminal write — used to simulate
	// a superseded/stale claim (storage.ErrNotFound), which must count no metric.
	terminalErr error

	claims    int
	sweeps    int
	completed []uuid.UUID
	retried   []retryCall
	dead      []deadCall
}

func (f *fakeStore) ClaimJob(_ context.Context, _ []string, lease time.Duration) (storage.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claims++
	f.lastLease = lease
	if len(f.claimQueue) == 0 {
		return storage.Job{}, storage.ErrNotFound
	}
	j := f.claimQueue[0]
	f.claimQueue = f.claimQueue[1:]
	return j, nil
}

func (f *fakeStore) CompleteJob(_ context.Context, id uuid.UUID, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.terminalErr != nil {
		return f.terminalErr
	}
	f.completed = append(f.completed, id)
	return nil
}

func (f *fakeStore) RetryJob(_ context.Context, id uuid.UUID, attempts int, lastError string, runAfter time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.terminalErr != nil {
		return f.terminalErr
	}
	f.retried = append(f.retried, retryCall{id, attempts, lastError, runAfter})
	return nil
}

func (f *fakeStore) MarkJobDead(_ context.Context, id uuid.UUID, attempts int, lastError string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.terminalErr != nil {
		return f.terminalErr
	}
	f.dead = append(f.dead, deadCall{id, attempts, lastError})
	return nil
}

func (f *fakeStore) SweepExpiredJobs(_ context.Context, _ []string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sweeps++
	return 0, nil
}

func (f *fakeStore) CountJobBacklog(_ context.Context, _ []string) (map[string]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.backlog, nil
}

type outcomeKV struct{ kind, outcome string }

type fakeMetrics struct {
	mu        sync.Mutex
	outcomes  []outcomeKV
	durations []string
	backlog   map[string]int
}

func newFakeMetrics() *fakeMetrics { return &fakeMetrics{backlog: map[string]int{}} }

func (m *fakeMetrics) JobOutcome(kind, outcome string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.outcomes = append(m.outcomes, outcomeKV{kind, outcome})
}

func (m *fakeMetrics) JobDuration(kind string, _ time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.durations = append(m.durations, kind)
}

func (m *fakeMetrics) SetJobBacklog(kind string, n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.backlog[kind] = n
}

func testJob(kind string, attempts, max int, payload string) storage.Job {
	return storage.Job{
		ID:          uuid.New(),
		Kind:        kind,
		Payload:     []byte(payload),
		Status:      storage.JobRunning,
		Attempts:    attempts,
		MaxAttempts: max,
	}
}

// --- 1. backoff ---

func TestBackoffDoublingCapAndJitter(t *testing.T) {
	cases := []struct {
		attempts int
		base     time.Duration
	}{
		{1, 30 * time.Second},
		{2, 60 * time.Second},
		{3, 120 * time.Second},
		{10, 30 * time.Minute}, // 30s<<9 = ~4.3h, capped to 30m
		{100, 30 * time.Minute},
	}
	for _, c := range cases {
		// Sample repeatedly: jitter must always land the result in [base, 1.5*base).
		for i := 0; i < 200; i++ {
			d := backoff(c.attempts)
			if d < c.base {
				t.Fatalf("backoff(%d) = %s, below base %s", c.attempts, d, c.base)
			}
			if d >= c.base+c.base/2 {
				t.Fatalf("backoff(%d) = %s, at/above 1.5*base %s", c.attempts, d, c.base+c.base/2)
			}
		}
	}
}

// --- 2. success ---

func TestRunOneSuccessCompletesAndMeters(t *testing.T) {
	store := &fakeStore{}
	metrics := newFakeMetrics()
	r := New(store, metrics, testLog(), Config{})

	var gotPayload json.RawMessage
	r.Register("test.echo", func(_ context.Context, p json.RawMessage) error {
		gotPayload = p
		return nil
	})

	job := testJob("test.echo", 1, 5, `{"n":1}`)
	r.runOne(context.Background(), job)

	if string(gotPayload) != `{"n":1}` {
		t.Errorf("handler payload = %s, want the job payload", gotPayload)
	}
	if len(store.completed) != 1 || store.completed[0] != job.ID {
		t.Errorf("completed = %v, want [%s]", store.completed, job.ID)
	}
	if len(store.retried) != 0 || len(store.dead) != 0 {
		t.Errorf("unexpected retry/dead on success: %v %v", store.retried, store.dead)
	}
	if len(metrics.outcomes) != 1 || metrics.outcomes[0] != (outcomeKV{"test.echo", outcomeDone}) {
		t.Errorf("outcomes = %v, want one done", metrics.outcomes)
	}
	if len(metrics.durations) != 1 {
		t.Errorf("durations = %v, want one duration", metrics.durations)
	}
}

// --- 3. retry while attempts remain ---

func TestRunOneFailureBelowMaxRetriesWithBackoff(t *testing.T) {
	store := &fakeStore{}
	metrics := newFakeMetrics()
	r := New(store, metrics, testLog(), Config{})
	r.Register("test.echo", func(context.Context, json.RawMessage) error {
		return errors.New("boom")
	})

	job := testJob("test.echo", 2, 5, `{}`) // attempt 2 of 5
	before := time.Now()
	r.runOne(context.Background(), job)

	if len(store.retried) != 1 {
		t.Fatalf("retried = %v, want one retry", store.retried)
	}
	rc := store.retried[0]
	if rc.id != job.ID {
		t.Errorf("retry id = %s, want %s", rc.id, job.ID)
	}
	if rc.lastError != "boom" {
		t.Errorf("retry last_error = %q, want boom", rc.lastError)
	}
	// backoff(2) is in [60s, 90s); runAfter = ~before + that.
	delta := rc.runAfter.Sub(before)
	if delta < 60*time.Second || delta >= 90*time.Second+time.Second {
		t.Errorf("runAfter delta = %s, want within [60s,90s)", delta)
	}
	if len(store.dead) != 0 || len(store.completed) != 0 {
		t.Errorf("unexpected dead/complete on retry: %v %v", store.dead, store.completed)
	}
	if len(metrics.outcomes) != 1 || metrics.outcomes[0].outcome != outcomeRetry {
		t.Errorf("outcomes = %v, want one retry", metrics.outcomes)
	}
}

// --- 4. dead-letter at max ---

func TestRunOneFailureAtMaxDeadLetters(t *testing.T) {
	store := &fakeStore{}
	metrics := newFakeMetrics()
	r := New(store, metrics, testLog(), Config{})
	r.Register("test.echo", func(context.Context, json.RawMessage) error {
		return errors.New("still boom")
	})

	job := testJob("test.echo", 5, 5, `{}`) // attempt 5 of 5 — exhausted
	r.runOne(context.Background(), job)

	if len(store.dead) != 1 {
		t.Fatalf("dead = %v, want one dead-letter", store.dead)
	}
	if store.dead[0].id != job.ID || store.dead[0].lastError != "still boom" {
		t.Errorf("dead = %+v, want id %s last_error 'still boom'", store.dead[0], job.ID)
	}
	if len(store.retried) != 0 {
		t.Errorf("retried = %v, want none at max", store.retried)
	}
	if len(metrics.outcomes) != 1 || metrics.outcomes[0].outcome != outcomeDead {
		t.Errorf("outcomes = %v, want one dead", metrics.outcomes)
	}
}

// --- superseded claim: a stale terminal write counts no metric ---

func TestRunOneSupersededCompleteCountsNoOutcome(t *testing.T) {
	store := &fakeStore{terminalErr: storage.ErrNotFound} // reclaimed by a newer lease
	metrics := newFakeMetrics()
	r := New(store, metrics, testLog(), Config{})
	r.Register("test.echo", func(context.Context, json.RawMessage) error { return nil })

	r.runOne(context.Background(), testJob("test.echo", 1, 5, `{}`))

	if len(store.completed) != 0 {
		t.Errorf("completed = %v, want none recorded (write fenced out)", store.completed)
	}
	for _, o := range metrics.outcomes {
		if o.outcome == outcomeDone {
			t.Errorf("recorded a done outcome for a superseded claim: %v", metrics.outcomes)
		}
	}
	// Duration is still observed (the handler did run); only the outcome is skipped.
	if len(metrics.durations) != 1 {
		t.Errorf("durations = %v, want the handler execution still timed", metrics.durations)
	}
}

// --- 5. handler deadline ≈ now+Lease, panic recovered onto failure path ---

func TestRunOnePanicRecoveredWithLeaseDeadline(t *testing.T) {
	store := &fakeStore{}
	r := New(store, nil, testLog(), Config{Lease: 200 * time.Millisecond})

	var sawDeadline time.Time
	var hadDeadline bool
	r.Register("test.echo", func(ctx context.Context, _ json.RawMessage) error {
		sawDeadline, hadDeadline = ctx.Deadline()
		panic("kaboom")
	})

	before := time.Now()
	job := testJob("test.echo", 1, 5, `{}`)
	r.runOne(context.Background(), job) // must NOT propagate the panic

	if !hadDeadline {
		t.Fatal("handler ctx had no deadline, want now+Lease")
	}
	delta := sawDeadline.Sub(before)
	if delta < 150*time.Millisecond || delta > 300*time.Millisecond {
		t.Errorf("deadline delta = %s, want ≈ Lease (200ms)", delta)
	}
	// Panic routed to the failure path (attempts 1 < 5 → retry), loop intact.
	if len(store.retried) != 1 {
		t.Errorf("retried = %v, want one retry after panic", store.retried)
	}
}

// --- 6. empty registry idles without touching the Store ---

func TestRunEmptyRegistryIdlesNoStoreCalls(t *testing.T) {
	store := &fakeStore{}
	r := New(store, newFakeMetrics(), testLog(), Config{PollInterval: time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()

	time.Sleep(30 * time.Millisecond) // let it spin if it were going to
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not unwind promptly on cancel")
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if store.claims != 0 || store.sweeps != 0 {
		t.Errorf("empty registry touched Store: claims=%d sweeps=%d", store.claims, store.sweeps)
	}
}

// --- pass loop: drains all runnable, refreshes gauges, survives across passes ---

func TestPassDrainsAndRefreshesBacklog(t *testing.T) {
	store := &fakeStore{
		claimQueue: []storage.Job{
			testJob("test.echo", 1, 5, `{}`),
			testJob("test.echo", 1, 5, `{}`),
		},
		backlog: map[string]int{"test.echo": 0},
	}
	metrics := newFakeMetrics()
	r := New(store, metrics, testLog(), Config{Lease: time.Minute})

	var runs int
	var mu sync.Mutex
	r.Register("test.echo", func(context.Context, json.RawMessage) error {
		mu.Lock()
		runs++
		mu.Unlock()
		return nil
	})

	r.pass(context.Background())

	if runs != 2 {
		t.Errorf("handler runs = %d, want 2 (drained both)", runs)
	}
	if store.sweeps != 1 {
		t.Errorf("sweeps = %d, want one per pass", store.sweeps)
	}
	if got, ok := metrics.backlog["test.echo"]; !ok || got != 0 {
		t.Errorf("backlog gauge = %v (present=%v), want Set to 0", got, ok)
	}
	if store.lastLease != time.Minute {
		t.Errorf("claim lease = %s, want configured 1m", store.lastLease)
	}
}

// Register duplicate kind panics (programmer error in wiring).
func TestRegisterDuplicatePanics(t *testing.T) {
	r := New(&fakeStore{}, nil, testLog(), Config{})
	r.Register("test.echo", func(context.Context, json.RawMessage) error { return nil })
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate Register did not panic")
		}
	}()
	r.Register("test.echo", func(context.Context, json.RawMessage) error { return nil })
}
