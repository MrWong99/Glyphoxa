package session_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/spend"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/wirenpc"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

func fp(v float64) *float64 { return &v }

// reRunner is a LoopRunner safe to invoke more than once (the hard-cap test starts
// a second session after the first self-ends): it signals the first run via
// started (once), captures each run's cfg, and blocks every run until its ctx is
// cancelled.
type reRunner struct {
	started chan struct{}
	once    sync.Once
	mu      sync.Mutex
	gotCfg  wirenpc.Config
	cancels int
}

func newReRunner() *reRunner { return &reRunner{started: make(chan struct{})} }

func (r *reRunner) run(ctx context.Context, cfg wirenpc.Config) error {
	r.mu.Lock()
	r.gotCfg = cfg
	r.mu.Unlock()
	r.once.Do(func() { close(r.started) })
	<-ctx.Done()
	r.mu.Lock()
	r.cancels++
	r.mu.Unlock()
	return ctx.Err()
}

func (r *reRunner) cfg() wirenpc.Config {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.gotCfg
}

func (r *reRunner) wasCancelled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cancels > 0
}

// usageSpy is a StageRecorder that counts the LLMTokens fan-out, so a test can
// prove the meter TEE wraps (does not replace) the base production recorder.
type usageSpy struct {
	observe.Discard
	mu  sync.Mutex
	llm int
}

func (s *usageSpy) LLMTokens(observe.Provider, string, int, int) {
	s.mu.Lock()
	s.llm++
	s.mu.Unlock()
}

func (s *usageSpy) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.llm
}

// spendManager builds a Manager whose base config carries a bus + the usage spy,
// so the spend tests can drive usage through the captured cfg and observe both the
// meter and the passed-through base recorder.
func spendManager(t *testing.T, store session.Store, run session.LoopRunner, bus *voiceevent.Bus, spy observe.StageRecorder) *session.Manager {
	t.Helper()
	return session.NewManager(store, run,
		wirenpc.Config{Token: "test-token", Bus: bus, StageMetrics: spy}, nil,
		slog.New(slog.DiscardHandler), true)
}

// collectSpendCaps subscribes a collector for SpendCapReached events on bus.
func collectSpendCaps(bus *voiceevent.Bus) (get func() []voiceevent.SpendCapReached) {
	var mu sync.Mutex
	var got []voiceevent.SpendCapReached
	voiceevent.On(bus, func(e voiceevent.SpendCapReached) {
		mu.Lock()
		got = append(got, e)
		mu.Unlock()
	})
	return func() []voiceevent.SpendCapReached {
		mu.Lock()
		defer mu.Unlock()
		return append([]voiceevent.SpendCapReached(nil), got...)
	}
}

// bigGroqLLM drives one large priced LLM usage call through rec (1M+1M groq tokens
// ≈ $1.38), enough to cross a sub-dollar cap in one or two calls.
func bigGroqLLM(rec observe.StageRecorder) {
	rec.LLMTokens(observe.ProviderGroq, "llama-3.3-70b-versatile", 1_000_000, 1_000_000)
}

// TestStart_NoCaps_ByteForByteToday pins the feature-off default: with neither cap
// set the session gets NO gate and its StageMetrics is the base recorder untouched
// — today's behavior. Spend() reports the zero Status.
func TestStart_NoCaps_ByteForByteToday(t *testing.T) {
	store := newFakeStore() // default caps: both nil
	runner := newReRunner()
	spy := &usageSpy{}
	mgr := spendManager(t, store, runner.run, voiceevent.NewBus(), spy)

	if _, err := mgr.Start(context.Background(), uuid.New(), uuid.New()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-runner.started
	cfg := runner.cfg()

	if cfg.Gate != nil {
		t.Fatal("no caps configured: cfg.Gate must be nil (feature off)")
	}
	if cfg.StageMetrics != observe.StageRecorder(spy) {
		t.Fatal("no caps configured: cfg.StageMetrics must be the base recorder, untouched (no tee)")
	}
	if s := mgr.Spend(); s.State != spend.CapNone || s.EstimatedUSD != 0 {
		t.Fatalf("Spend() with no caps = %+v, want zero Status", s)
	}
	mgr.Shutdown()
}

// TestStart_WithCaps_GatesAndTees pins step 7: a configured cap gives the session a
// gate AND a teed recorder; driving a usage call crosses the soft cap, so the gate
// refuses new turns, the base recorder still saw the call (tee, not replace), and
// Spend() reflects the same accumulator.
func TestStart_WithCaps_GatesAndTees(t *testing.T) {
	store := newFakeStore()
	store.caps = storage.SpendCaps{SoftUSD: fp(1.0)} // soft-only
	runner := newReRunner()
	spy := &usageSpy{}
	mgr := spendManager(t, store, runner.run, voiceevent.NewBus(), spy)

	if _, err := mgr.Start(context.Background(), uuid.New(), uuid.New()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-runner.started
	cfg := runner.cfg()

	if cfg.Gate == nil {
		t.Fatal("a configured cap must give the session a turn gate")
	}
	if cfg.Gate.AllowTurn() != true {
		t.Fatal("gate must allow turns before any spend")
	}

	// Drive usage through the SESSION recorder (as the pipeline would): the tee
	// forwards to both the base spy and the meter.
	bigGroqLLM(cfg.StageMetrics)

	if cfg.Gate.AllowTurn() != false {
		t.Fatal("gate must refuse new turns after crossing the soft cap")
	}
	if spy.count() != 1 {
		t.Fatalf("base recorder LLMTokens count = %d, want 1 (tee must wrap, not replace, the base)", spy.count())
	}
	if s := mgr.Spend(); s.State != spend.CapSoft || s.EstimatedUSD <= 0 {
		t.Fatalf("Spend() after crossing soft = %+v, want soft state + positive estimate", s)
	}
	mgr.Shutdown()
}

// TestSoftCap_SessionKeepsRunning pins step 9: crossing the soft cap publishes
// SpendCapReached{soft}, but the session stays live (soft never ends it) and
// Spend() reports the soft state.
func TestSoftCap_SessionKeepsRunning(t *testing.T) {
	store := newFakeStore()
	store.caps = storage.SpendCaps{SoftUSD: fp(1.0)}
	runner := newReRunner()
	bus := voiceevent.NewBus()
	events := collectSpendCaps(bus)
	mgr := spendManager(t, store, runner.run, bus, &usageSpy{})

	if _, err := mgr.Start(context.Background(), uuid.New(), uuid.New()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-runner.started
	bigGroqLLM(runner.cfg().StageMetrics)

	evs := events()
	if len(evs) != 1 || evs[0].Level != voiceevent.SpendCapSoft {
		t.Fatalf("published spend-cap events = %+v, want one soft", evs)
	}
	if _, active := mgr.Snapshot(); !active {
		t.Fatal("soft cap must NOT end the session")
	}
	if runner.wasCancelled() {
		t.Fatal("soft cap must not cancel the run ctx")
	}
	if s := mgr.Spend(); s.State != spend.CapSoft {
		t.Fatalf("Spend().State = %q, want soft", s.State)
	}
	mgr.Shutdown()
}

// TestHardCap_EndsSessionCleanly pins step 8: crossing the hard cap cancels the run
// ctx, closes the row with status ENDED (a policy stop, not failed) + the
// spend_cap_hard reason, publishes SpendCapReached{hard} while the session is still
// active, frees the single-active guard, and lets the next Start succeed.
func TestHardCap_EndsSessionCleanly(t *testing.T) {
	store := newFakeStore()
	store.caps = storage.SpendCaps{HardUSD: fp(1.0)}
	runner := newReRunner()
	bus := voiceevent.NewBus()
	events := collectSpendCaps(bus)
	mgr := spendManager(t, store, runner.run, bus, &usageSpy{})

	vs, err := mgr.Start(context.Background(), uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-runner.started

	// Cross the hard cap through the session recorder — this fires onHard.
	bigGroqLLM(runner.cfg().StageMetrics)

	// The hard-cap trip cancels the run ctx on a goroutine; wait for the session to
	// end (the guard frees).
	waitInactive(t, mgr)

	// The row closed ENDED (not failed) with the spend_cap_hard reason.
	closed := store.session(vs.ID)
	if closed.Status != storage.VoiceSessionEnded {
		t.Fatalf("hard-cap row status = %q, want ended (a policy stop is not a fault)", closed.Status)
	}
	if closed.EndReason == nil || *closed.EndReason != "spend_cap_hard: estimated spend crossed the hard cap" {
		t.Fatalf("hard-cap end_reason = %v, want the spend_cap_hard reason", closed.EndReason)
	}
	if evs := events(); len(evs) != 1 || evs[0].Level != voiceevent.SpendCapHard {
		t.Fatalf("published spend-cap events = %+v, want one hard", evs)
	}
	if !runner.wasCancelled() {
		t.Fatal("hard cap must cancel the run ctx")
	}

	// Guard freed: the SAME manager accepts a new Start (the reRunner tolerates the
	// second run without re-signalling started).
	if _, err := mgr.Start(context.Background(), uuid.New(), uuid.New()); err != nil {
		t.Fatalf("second Start after hard-cap end must succeed, got %v", err)
	}
	mgr.Shutdown()
}

// waitInactive polls until the manager reports no active session, or fails.
func waitInactive(t *testing.T, mgr *session.Manager) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, active := mgr.Snapshot(); !active {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("session did not end within the deadline after the hard cap")
}
