package session_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/highlight"
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/wirenpc"
)

// fakeUsageWriter is an in-memory usage_ledger: AddUsage accumulates the rows
// like the real upsert, so a test can read what has been durably flushed so far.
type fakeUsageWriter struct {
	mu      sync.Mutex
	flushes int
	chars   int64
}

func (u *fakeUsageWriter) AddUsage(_ context.Context, rows []storage.UsageRow) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.flushes++
	for _, r := range rows {
		u.chars += r.TTSCharacters
	}
	return nil
}

func (u *fakeUsageWriter) flushedChars() int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.chars
}

// meteringRunner is a loop runner that meters usage through the session's own
// StageMetrics (the path the live pipeline's capture points take) each time the
// test asks it to, and otherwise blocks until cancelled.
type meteringRunner struct {
	started chan struct{}
	meter   chan int
}

func newMeteringRunner() *meteringRunner {
	return &meteringRunner{started: make(chan struct{}), meter: make(chan int)}
}

func (r *meteringRunner) run(ctx context.Context, cfg wirenpc.Config) error {
	close(r.started)
	for {
		select {
		case n := <-r.meter:
			cfg.StageMetrics.TTSCharacters(observe.ProviderElevenLabs, n)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestUsageLedgerFlushesPeriodicallyDuringSession is #660: a running session's
// metered usage reaches the durable usage_ledger WHILE the session runs, not
// only at loop exit. Before, a crash / OOM kill / node restart mid-session lost
// the whole session's usage, and the allowance gate (which reads flushed rows
// only) saw none of a running session's spend.
func TestUsageLedgerFlushesPeriodicallyDuringSession(t *testing.T) {
	store := newFakeStore()
	runner := newMeteringRunner()
	usage := &fakeUsageWriter{}
	mgr := newManagerDeps(t, store, runner.run, true, session.Deps{Usage: usage})
	mgr.SetUsageFlushIntervalForTest(5 * time.Millisecond)

	tenantID := uuid.New()
	if _, err := mgr.Start(context.Background(), tenantID, uuid.New(), ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-runner.started

	runner.meter <- 120
	waitUntil(t, "the first interval flush while the session is live", func() bool { return usage.flushedChars() == 120 })
	if _, live, _ := mgr.Active(context.Background(), tenantID); !live {
		t.Fatal("session ended; the flush under test must happen mid-session")
	}

	// Usage metered after a flush lands in the next one: nothing is double
	// counted and nothing is dropped.
	runner.meter <- 30
	waitUntil(t, "the second interval flush", func() bool { return usage.flushedChars() == 150 })

	if _, err := mgr.Stop(context.Background(), tenantID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := usage.flushedChars(); got != 150 {
		t.Errorf("flushed chars after stop = %d, want 150 (each unit of usage flushed exactly once)", got)
	}
}

// orderProbe records, at the moment each end-of-session step runs, what was
// already durable: the usage flushed so far and whether the session row was
// terminal.
type orderProbe struct {
	store *fakeStore
	usage *fakeUsageWriter

	mu sync.Mutex
	// usageAtTranscript is the usage already flushed when the FIRST finalizer ran.
	usageAtTranscript int64
	// statusAt maps a best-effort finalizer to the row status it observed.
	statusAt map[string]storage.VoiceSessionStatus
}

func (p *orderProbe) note(step string, id uuid.UUID) {
	st := p.store.session(id).Status
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.statusAt == nil {
		p.statusAt = map[string]storage.VoiceSessionStatus{}
	}
	p.statusAt[step] = st
}

type probeTranscript struct{ p *orderProbe }

func (f probeTranscript) Finalize(_ context.Context, _ uuid.UUID) (int, error) {
	flushed := f.p.usage.flushedChars()
	f.p.mu.Lock()
	f.p.usageAtTranscript = flushed
	f.p.mu.Unlock()
	return 4, nil
}

type probeHighlights struct{ p *orderProbe }

func (probeHighlights) Begin(_, _, _ uuid.UUID) highlight.Sink { return spySink{} }
func (f probeHighlights) Finalize(_ context.Context, id uuid.UUID) error {
	f.p.note("highlights", id)
	return nil
}

type probeChunker struct{ p *orderProbe }

func (f probeChunker) FlushSession(_ context.Context, id uuid.UUID) error {
	f.p.note("chunker", id)
	return nil
}

// TestSessionEndWritesLedgerAndTerminalRowBeforeBestEffortFinalizers pins the
// end-of-session order: the Usage Ledger is flushed FIRST and the terminal row
// lands as soon as its line count is known, both AHEAD of the best-effort
// finalizers (highlights drain, chunk flush), each of which may take its full
// budget. A SIGKILL at the end of a short termination grace period then costs a
// convenience, never the session's billed usage or a row stuck 'running'.
func TestSessionEndWritesLedgerAndTerminalRowBeforeBestEffortFinalizers(t *testing.T) {
	store := newFakeStore()
	runner := newMeteringRunner()
	usage := &fakeUsageWriter{}
	probe := &orderProbe{store: store, usage: usage}
	mgr := newManagerDeps(t, store, runner.run, true, session.Deps{
		Usage:      usage,
		Transcript: probeTranscript{probe},
		Highlights: probeHighlights{probe},
		Chunker:    probeChunker{probe},
	})

	tenantID := uuid.New()
	if _, err := mgr.Start(context.Background(), tenantID, uuid.New(), ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-runner.started
	runner.meter <- 77

	ended, err := mgr.Stop(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if ended.LineCount != 4 {
		t.Errorf("ended line_count = %d, want 4 (the terminal row still carries the finalized count)", ended.LineCount)
	}

	probe.mu.Lock()
	defer probe.mu.Unlock()
	if probe.usageAtTranscript != 77 {
		t.Errorf("usage flushed before the first finalizer = %d, want 77 (ledger flush must come first)", probe.usageAtTranscript)
	}
	for _, step := range []string{"highlights", "chunker"} {
		if got := probe.statusAt[step]; got != storage.VoiceSessionEnded {
			t.Errorf("row status when the %s finalizer ran = %q, want %q (terminal row must land first)", step, got, storage.VoiceSessionEnded)
		}
	}
}
