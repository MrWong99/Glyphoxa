//go:build integration

package storage_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// migrateForJobs migrates up and returns a pool; the job table needs no tenant or
// campaign (its payload carries its own scope), so this skips the campaign seed.
func migrateForJobs(t *testing.T) *storage.Store {
	t.Helper()
	dsn := startPostgres(t)
	pool, _, _ := seedCampaign(t, dsn)
	return storage.New(pool)
}

// eventually polls fn until it returns nil or the deadline elapses, so lease-
// expiry assertions ride the DB clock (a tiny lease + re-claim) instead of local
// clock math.
func eventually(t *testing.T, d time.Duration, fn func() error) {
	t.Helper()
	deadline := time.Now().Add(d)
	var last error
	for time.Now().Before(deadline) {
		if last = fn(); last == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s: %v", d, last)
}

func TestEnqueueThenClaim(t *testing.T) {
	st := migrateForJobs(t)
	ctx := context.Background()

	id, err := st.EnqueueJob(ctx, "test.echo", []byte(`{"n":1}`), 0)
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}

	// A different kind is not claimed.
	if _, err := st.ClaimJob(ctx, []string{"test.other"}, time.Minute); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("ClaimJob wrong kind = %v, want ErrNotFound", err)
	}

	j, err := st.ClaimJob(ctx, []string{"test.echo"}, time.Minute)
	if err != nil {
		t.Fatalf("ClaimJob: %v", err)
	}
	if j.ID != id {
		t.Errorf("claimed id = %s, want %s", j.ID, id)
	}
	if j.Status != storage.JobRunning {
		t.Errorf("status = %q, want running", j.Status)
	}
	if j.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", j.Attempts)
	}
	if j.MaxAttempts != storage.DefaultJobMaxAttempts {
		t.Errorf("max_attempts = %d, want %d", j.MaxAttempts, storage.DefaultJobMaxAttempts)
	}
	if j.LeasedUntil == nil {
		t.Error("leased_until = nil, want a future lease")
	}
	if string(j.Payload) != `{"n": 1}` && string(j.Payload) != `{"n":1}` {
		t.Errorf("payload = %s, want the enqueued json", j.Payload)
	}

	// Queue now drained: nothing else runnable.
	if _, err := st.ClaimJob(ctx, []string{"test.echo"}, time.Minute); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("ClaimJob drained = %v, want ErrNotFound", err)
	}
}

// TestConcurrentClaimSkipLocked proves FOR UPDATE SKIP LOCKED: two workers
// claiming concurrently against two runnable jobs get two DISTINCT jobs and
// neither blocks on the other's locked candidate.
func TestConcurrentClaimSkipLocked(t *testing.T) {
	st := migrateForJobs(t)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if _, err := st.EnqueueJob(ctx, "test.echo", []byte(`{}`), 0); err != nil {
			t.Fatalf("EnqueueJob %d: %v", i, err)
		}
	}

	var wg sync.WaitGroup
	ids := make([]uuid.UUID, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			j, err := st.ClaimJob(ctx, []string{"test.echo"}, time.Minute)
			errs[i], ids[i] = err, j.ID
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent claim %d: %v", i, err)
		}
	}
	if ids[0] == ids[1] {
		t.Fatalf("both workers claimed the same job %s — SKIP LOCKED not isolating", ids[0])
	}
}

// TestLeaseExpiryReclaim: a claimed job whose short lease expires becomes runnable
// again (still below max_attempts) and re-claims as the SAME row with attempts=2.
func TestLeaseExpiryReclaim(t *testing.T) {
	st := migrateForJobs(t)
	ctx := context.Background()

	id, err := st.EnqueueJob(ctx, "test.echo", []byte(`{}`), 5)
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	first, err := st.ClaimJob(ctx, []string{"test.echo"}, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("first ClaimJob: %v", err)
	}
	if first.Attempts != 1 {
		t.Fatalf("first attempts = %d, want 1", first.Attempts)
	}

	var second storage.Job
	eventually(t, 2*time.Second, func() error {
		j, err := st.ClaimJob(ctx, []string{"test.echo"}, time.Minute)
		if err != nil {
			return err // ErrNotFound until the lease expires
		}
		second = j
		return nil
	})
	if second.ID != id {
		t.Errorf("re-claimed id = %s, want same %s", second.ID, id)
	}
	if second.Attempts != 2 {
		t.Errorf("re-claim attempts = %d, want 2", second.Attempts)
	}
}

// TestSweepExpiredJobs: a running job whose lease expired at max_attempts is
// dead-lettered with a last_error; a below-max expired job is left alone.
func TestSweepExpiredJobs(t *testing.T) {
	st := migrateForJobs(t)
	ctx := context.Background()

	// atMax: max_attempts=1, claimed once (attempts==1==max), lease expires.
	atMax, err := st.EnqueueJob(ctx, "test.echo", []byte(`{}`), 1)
	if err != nil {
		t.Fatalf("EnqueueJob atMax: %v", err)
	}
	// belowMax: max_attempts=5, claimed once (attempts==1<max), lease expires.
	belowMax, err := st.EnqueueJob(ctx, "test.echo", []byte(`{}`), 5)
	if err != nil {
		t.Fatalf("EnqueueJob belowMax: %v", err)
	}
	if _, err := st.ClaimJob(ctx, []string{"test.echo"}, 20*time.Millisecond); err != nil {
		t.Fatalf("claim 1: %v", err)
	}
	if _, err := st.ClaimJob(ctx, []string{"test.echo"}, 20*time.Millisecond); err != nil {
		t.Fatalf("claim 2: %v", err)
	}

	var swept int
	eventually(t, 2*time.Second, func() error {
		n, err := st.SweepExpiredJobs(ctx, []string{"test.echo"})
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("no expired-at-max job swept yet")
		}
		swept = n
		return nil
	})
	if swept != 1 {
		t.Fatalf("swept = %d, want 1", swept)
	}

	// atMax is dead with a recorded error.
	dead, err := st.GetJob(ctx, atMax)
	if err != nil {
		t.Fatalf("GetJob atMax: %v", err)
	}
	if dead.Status != storage.JobDead {
		t.Errorf("atMax status = %q, want dead", dead.Status)
	}
	if dead.LastError == nil || *dead.LastError == "" {
		t.Error("atMax last_error empty, want a swept reason")
	}

	// belowMax was NOT swept — it stays retryable.
	alive, err := st.GetJob(ctx, belowMax)
	if err != nil {
		t.Fatalf("GetJob belowMax: %v", err)
	}
	if alive.Status == storage.JobDead {
		t.Error("belowMax dead, want left retryable")
	}
}

// TestTerminalWritesFencedByClaimGeneration proves the claim-generation fence: a
// stale worker A (lease expired) whose job was reclaimed and completed by worker B
// cannot flip the terminal state back. A's late CompleteJob/RetryJob/MarkJobDead —
// carrying A's attempts snapshot — match no row (ErrNotFound) and leave the job
// exactly as B left it, so a done/dead job never silently re-runs (ADR-0049).
func TestTerminalWritesFencedByClaimGeneration(t *testing.T) {
	st := migrateForJobs(t)
	ctx := context.Background()

	id, err := st.EnqueueJob(ctx, "test.echo", []byte(`{}`), 5)
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}

	// Worker A claims (attempts=1) with a tiny lease, then stalls.
	jobA, err := st.ClaimJob(ctx, []string{"test.echo"}, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("A ClaimJob: %v", err)
	}

	// Lease expires; worker B reclaims (attempts=2) and completes it.
	var jobB storage.Job
	eventually(t, 2*time.Second, func() error {
		j, e := st.ClaimJob(ctx, []string{"test.echo"}, time.Minute)
		if e != nil {
			return e
		}
		jobB = j
		return nil
	})
	if jobB.Attempts != 2 {
		t.Fatalf("B attempts = %d, want 2", jobB.Attempts)
	}
	if err := st.CompleteJob(ctx, id, jobB.Attempts); err != nil {
		t.Fatalf("B CompleteJob: %v", err)
	}

	// Worker A now finishes and issues its late terminal writes with attempts=1.
	// Every one is fenced out (ErrNotFound) — a superseded claim, not applied.
	if err := st.CompleteJob(ctx, id, jobA.Attempts); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("stale CompleteJob = %v, want ErrNotFound (fenced)", err)
	}
	if err := st.RetryJob(ctx, id, jobA.Attempts, "late boom", time.Now()); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("stale RetryJob = %v, want ErrNotFound (fenced)", err)
	}
	if err := st.MarkJobDead(ctx, id, jobA.Attempts, "late dead"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("stale MarkJobDead = %v, want ErrNotFound (fenced)", err)
	}

	// The job is exactly as B left it: done, attempts=2, and NOT re-runnable.
	final, err := st.GetJob(ctx, id)
	if err != nil {
		t.Fatalf("GetJob final: %v", err)
	}
	if final.Status != storage.JobDone {
		t.Errorf("final status = %q, want done (A's stale writes must not flip it)", final.Status)
	}
	if final.Attempts != 2 {
		t.Errorf("final attempts = %d, want 2", final.Attempts)
	}
	if _, err := st.ClaimJob(ctx, []string{"test.echo"}, time.Minute); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("job re-claimable after stale writes = %v, want ErrNotFound (no re-run)", err)
	}
}
