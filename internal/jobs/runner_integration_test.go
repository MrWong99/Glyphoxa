//go:build integration

// These tests stand up a real Postgres (testcontainers) behind the `integration`
// tag, mirroring internal/storage's harness (ADR-0021/0033). They prove the
// restart-survival + exactly-once-across-replicas acceptance criteria (#286, AC1)
// against the actual claim SQL rather than a fake.

package jobs_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/MrWong99/Glyphoxa/internal/jobs"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

const pgImage = "pgvector/pgvector:pg17"

const testKind = "test.echo"

func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// newStore spins up (or reuses via GLYPHOXA_TEST_DSN) a Postgres, migrates up, and
// returns a Store over a fresh pool.
func newStore(t *testing.T) *storage.Store {
	t.Helper()
	ctx := context.Background()

	dsn := os.Getenv("GLYPHOXA_TEST_DSN")
	if dsn == "" {
		container, err := tcpostgres.Run(ctx, pgImage,
			tcpostgres.WithDatabase("glyphoxa_test"),
			tcpostgres.WithUsername("glyphoxa"),
			tcpostgres.WithPassword("glyphoxa"),
			testcontainers.WithWaitStrategy(
				wait.ForLog("database system is ready to accept connections").
					WithOccurrence(2).WithStartupTimeout(60*time.Second)),
		)
		if err != nil {
			t.Skipf("SKIPPED DB TEST — NO POSTGRES: could not start %s (is Docker running?). "+
				"Set GLYPHOXA_TEST_DSN to run without Docker. err: %v", pgImage, err)
		}
		t.Cleanup(func() { _ = testcontainers.TerminateContainer(container) })
		dsn, err = container.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			t.Fatalf("connection string: %v", err)
		}
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if err := storage.MigrateUp(ctx, db); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	_ = db.Close()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return storage.New(pool)
}

// recorder is a mutex-guarded execution ledger the test-only handler appends to.
type recorder struct {
	mu  sync.Mutex
	ids []uuid.UUID
}

func (r *recorder) record(id uuid.UUID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids = append(r.ids, id)
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.ids)
}

// TestTwoRunnersExactlyOnceThenRestartSurvival is AC1: N jobs spread across two
// Runner instances sharing one DB each run exactly once (the claim's FOR UPDATE
// SKIP LOCKED prevents double-claims; a long lease prevents reclaim), and a job
// enqueued while NO runner is live is picked up when a fresh Runner starts.
func TestTwoRunnersExactlyOnceThenRestartSurvival(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	const n = 12
	want := make(map[uuid.UUID]bool, n)
	for i := 0; i < n; i++ {
		id, err := store.EnqueueJob(ctx, testKind, []byte(`{}`), 5)
		if err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
		want[id] = true
	}

	rec := &recorder{}

	runCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		r := jobs.New(store, nil, testLog(), jobs.Config{PollInterval: 20 * time.Millisecond, Lease: 5 * time.Minute})
		r.Register(testKind, func(_ context.Context, _ json.RawMessage) error {
			// The ledger counts executions; the done-status assertion below proves each
			// of the N distinct rows completed. Total == N proves no row ran twice.
			rec.record(uuid.New())
			return nil
		})
		wg.Add(1)
		go func() { defer wg.Done(); r.Run(runCtx) }()
	}

	// Wait until every enqueued job is done.
	waitAllDone(t, store, want, 10*time.Second)
	cancel()
	wg.Wait()

	// Exactly N executions total across both runners — no job ran twice.
	if got := rec.count(); got != n {
		t.Fatalf("total executions = %d, want exactly %d (a double-claim would exceed N)", got, n)
	}

	// --- restart survival: enqueue with no runner live, then start a fresh one ---
	survivorID, err := store.EnqueueJob(ctx, testKind, []byte(`{}`), 5)
	if err != nil {
		t.Fatalf("enqueue survivor: %v", err)
	}

	freshCtx, freshCancel := context.WithCancel(ctx)
	defer freshCancel()
	fresh := jobs.New(store, nil, testLog(), jobs.Config{PollInterval: 20 * time.Millisecond, Lease: 5 * time.Minute})
	fresh.Register(testKind, func(_ context.Context, _ json.RawMessage) error {
		rec.record(uuid.New())
		return nil
	})
	go fresh.Run(freshCtx)

	waitAllDone(t, store, map[uuid.UUID]bool{survivorID: true}, 5*time.Second)
}

// waitAllDone polls until every id in want reports status='done', or fails.
func waitAllDone(t *testing.T, store *storage.Store, want map[uuid.UUID]bool, d time.Duration) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(d)
	ids := make([]uuid.UUID, 0, len(want))
	for id := range want {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })

	for time.Now().Before(deadline) {
		allDone := true
		for _, id := range ids {
			j, err := store.GetJob(ctx, id)
			if err != nil {
				t.Fatalf("GetJob %s: %v", id, err)
			}
			if j.Status != storage.JobDone {
				allDone = false
				break
			}
		}
		if allDone {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("not all jobs reached done within %s", d)
}
