// Package idleclose is the Voice Instance's Idle Close watchdog (CONTEXT.md
// "Idle Close", ADR-0061): it ends a Voice Session that has processed no audio
// for the Idle Close Window, and sheds the quietest session when the process
// crosses a configured resource ceiling.
//
// It exists because a Voice Session holds real resources for as long as it runs
// — a Discord voice connection with its DAVE/MLS state, a claim-plane heartbeat,
// a per-session tape ring and its consent poller, a VAD session per Speaker Lane
// — and none of that is bounded by anything a silent room does. A production
// session left running for a day in a channel nobody ever joined paid all of it
// and produced nothing.
//
// The shape deliberately mirrors the ADR-0046 hard spend cap, the other policy
// that ends a live Voice Session: the watchdog only DECIDES, and hands the
// decision to a per-session callback the owner supplies (the session Manager sets
// its end_reason override and cancels the run ctx). Only the Voice Instance
// hosting a session may end it — no sweeper reaches across the fleet (ADR-0006,
// ADR-0057 (e)) — and the row closes 'ended', never 'failed': a deliberate policy
// stop is not a fault (ADR-0043, ADR-0046).
//
// The hot path is one atomic increment. [Session.Mark] is called once per audio
// frame the voice loop processes (inbound room audio and outbound Agent speech),
// so it takes no lock, allocates nothing, and reads no clock; the watchdog
// samples the counter on its own sweep and only then consults the clock. That is
// also why idleness is measured off a COUNTER rather than a timestamp: the
// silence clock (pkg/voice/wire) keeps the VAD and Segmenter busy at ~31 Hz
// through a completely empty channel, so anything keyed off those call rates
// would never go stale.
package idleclose

import (
	"context"
	"log/slog"
	"runtime"
	"runtime/metrics"
	"sync"
	"sync/atomic"
	"time"
)

// The end_reason values an Idle Close writes. Each is an ADR-0043 pair: a stable,
// greppable machine prefix plus prose, exactly like ADR-0046's `spend_cap_hard`.
// The prose is fixed rather than interpolated so the prefix stays the whole
// contract and a log/UI grep never has to tolerate a varying tail.
const (
	// ReasonIdle is written when a Voice Session processed no audio for the whole
	// Idle Close Window.
	ReasonIdle = "idle_no_audio: the Voice Session processed no audio for the idle close window"
	// ReasonResource is written when the Voice Instance crossed a configured
	// resource ceiling and this session was the quietest one it hosted.
	ReasonResource = "resource_ceiling: the Voice Instance crossed its configured resource ceiling"
	// ReasonChurn is written when a Voice Session ran through more Discord
	// reconnect cycles than its ceiling allows.
	ReasonChurn = "reconnect_churn: the Voice Session exceeded its reconnect-cycle ceiling"
)

// defaultSweep is the watchdog cadence when the Policy leaves it unset. 30s is
// two orders of magnitude below the default 15-minute window, so the close lands
// within half a percent of the configured deadline while the sweep itself costs
// one map walk and one runtime/metrics read per Voice Instance.
const defaultSweep = 30 * time.Second

// minSweepDivisor bounds the cadence against a short Window: the sweep interval
// never exceeds Window/minSweepDivisor, so an operator who sets a 20s window for
// a test still gets several checks inside it instead of one.
const minSweepDivisor = 2

// Policy is the deployment's Idle Close configuration, parsed from the
// GLYPHOXA_VOICE_IDLE_* env vars in the composition root (cmd/glyphoxa/boot.go)
// so the knobs stay deployment-owned and never leak into internal/session.
//
// Every check is independently opt-out: the zero Policy disables the feature
// completely, and [Guard.Enroll] then returns nil so the voice loop wires no
// activity tap and behaves byte-identically to the pre-Idle-Close path.
type Policy struct {
	// Window is the Idle Close Window: how long a Voice Session may process no
	// audio before the hosting Voice Instance closes it. Zero or negative disables
	// idle closing.
	Window time.Duration
	// Sweep is the watchdog cadence. Zero or negative takes [defaultSweep], capped
	// so it always fits inside a short Window (see [Guard.sweepInterval]).
	Sweep time.Duration
	// HeapCeiling is the Go heap footprint in bytes past which the Voice Instance
	// sheds its quietest Voice Session. Zero disables the check.
	//
	// It is deliberately a PROCESS reading, not a per-session one: Go attributes no
	// heap to a logical owner, and inventing a per-session number would be a
	// fiction. So the ceiling answers "this Voice Instance is over its budget",
	// and the victim rule (quietest first) makes the shed the least damaging one
	// available.
	HeapCeiling uint64
	// GoroutineCeiling is the process goroutine count past which the Voice Instance
	// sheds its quietest Voice Session. Zero disables the check. Same process-wide
	// caveat as HeapCeiling.
	GoroutineCeiling int
	// MaxCycles is how many Discord connect cycles one Voice Session may run
	// through before the hosting Voice Instance closes it. Zero disables the check.
	//
	// Unlike the two ceilings above this one IS per-session and exact, which is why
	// it is on by default: every reconnect cycle rebuilds the whole per-cycle world
	// — the Discord voice connection, the codec, the provider adapters each with
	// their own http.Transport, a Silero session per Speaker Lane, and (with
	// streaming STT) a fresh realtime websocket — and some of that provably does not
	// come back. A session that has cycled hundreds of times has leaked in
	// proportion, whatever else it is doing, so the count is the closest thing to a
	// direct per-session leak measurement the process actually has.
	MaxCycles int
}

// Enabled reports whether the Policy arms any check at all. The composition root
// branches on it to decide whether to run a watchdog goroutine at all.
func (p Policy) Enabled() bool {
	return p.Window > 0 || p.HeapCeiling > 0 || p.GoroutineCeiling > 0 || p.MaxCycles > 0
}

// Usage is one sample of the Voice Instance's resource footprint, as the ceilings
// are judged against.
type Usage struct {
	// HeapBytes is the Go runtime's memory footprint: everything it has mapped and
	// not returned to the OS. It tracks a container memory limit far better than
	// live-object bytes, which is what an operator actually sets a ceiling against.
	HeapBytes uint64
	// Goroutines is runtime.NumGoroutine().
	Goroutines int
}

// Guard is one Voice Instance's watchdog over every Voice Session it hosts. Build
// it with [New], run its single goroutine with [Guard.Run], and enroll each
// session with [Guard.Enroll]. A nil *Guard is a valid feature-off value: every
// method is a no-op on it.
type Guard struct {
	policy Policy
	log    *slog.Logger

	// now/usage/ticks are the injected seams (the internal/presence elector's
	// discipline): tests advance a clock by hand, report a synthetic footprint, and
	// drive Run's loop through a channel they fire themselves, so nothing here
	// needs a wall-clock wait. All three are written before Run starts.
	now   func() time.Time
	usage func() Usage
	ticks <-chan time.Time

	mu sync.Mutex
	// order is the enrollment order of the live handles. A slice rather than a bare
	// map so a sweep visits sessions deterministically and the victim rule breaks a
	// tie by "enrolled first" instead of by map iteration order.
	order []*Session
}

// New builds a Guard for policy. A nil logger discards logs. It starts nothing —
// call [Guard.Run].
func New(policy Policy, log *slog.Logger) *Guard {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Guard{
		policy: policy,
		log:    log,
		now:    time.Now,
		usage:  runtimeUsage,
	}
}

// Session is one enrolled Voice Session's handle. The voice loop calls
// [Session.Mark] on the audio path and the owner calls [Session.Release] when the
// session's loop returns. A nil *Session is the feature-off value: both methods
// are no-ops on it.
type Session struct {
	guard *Guard
	id    string
	// close is the owner's "end this Voice Session with this end_reason" callback.
	// It must not block: the Guard calls it from the sweep goroutine (the session
	// Manager's implementation hands off to a fresh goroutine, the #211 lock-order
	// pattern).
	close func(reason string)

	// marks counts audio frames this session processed. Written by the audio loop
	// (lock-free, allocation-free, clock-free) and read by the sweep, which is the
	// only reason the whole design needs no lock on the hot path.
	marks atomic.Uint64
	// cycles counts the Discord connect cycles this session has run through,
	// written by the reconnect loop and read by the sweep. Same lock-free
	// discipline as marks, though this one moves at most once per reconnect.
	cycles atomic.Uint64

	// The fields below are guarded by Guard.mu.
	seen       uint64    // marks as of lastActive
	lastActive time.Time // when marks last moved (enrollment time until it does)
	closed     bool      // this Guard already fired close for this session
	released   bool      // the owner deregistered it
}

// Enroll registers a Voice Session with the watchdog and returns its handle, or
// nil when the Guard is nil or its Policy arms no check — so a caller can skip
// wiring the activity tap entirely on the feature-off path. id names the session
// in the watchdog's logs only.
//
// close is invoked at most once, from the sweep goroutine, with one of
// [ReasonIdle] / [ReasonResource]. The session stays enrolled after it fires (its
// loop takes seconds to unwind through its finalizers) but is never chosen again.
func (g *Guard) Enroll(id string, close func(reason string)) *Session {
	if g == nil || !g.policy.Enabled() || close == nil {
		return nil
	}
	s := &Session{guard: g, id: id, close: close}
	g.mu.Lock()
	defer g.mu.Unlock()
	s.lastActive = g.now()
	g.order = append(g.order, s)
	return s
}

// Mark records that this Voice Session processed one audio frame. It is THE hot
// path — called once per inbound room frame and once per outbound Agent frame,
// roughly 50/s per active speaker — so it is a single atomic increment: no lock,
// no allocation, and no clock read. Safe on a nil handle.
func (s *Session) Mark() {
	if s == nil {
		return
	}
	s.marks.Add(1)
}

// CycleStarted records that this Voice Session began another Discord connect
// cycle. The reconnect loop calls it once per attempt, including the first, so
// the count is "cycles run" and a ceiling of N permits N-1 reconnects. Safe on a
// nil handle.
func (s *Session) CycleStarted() {
	if s == nil {
		return
	}
	s.cycles.Add(1)
}

// Release deregisters the session so no later sweep can consider it. The owner
// calls it once its loop has returned; it is idempotent and safe on a nil handle.
func (s *Session) Release() {
	if s == nil {
		return
	}
	g := s.guard
	g.mu.Lock()
	defer g.mu.Unlock()
	if s.released {
		return
	}
	s.released = true
	for i, v := range g.order {
		if v == s {
			g.order = append(g.order[:i], g.order[i+1:]...)
			return
		}
	}
}

// Run sweeps every enrolled Voice Session on the Policy's cadence until ctx is
// done. It returns immediately when the Guard is nil or its Policy arms no check,
// so a feature-off deployment runs no ticker at all.
func (g *Guard) Run(ctx context.Context) {
	if g == nil || !g.policy.Enabled() {
		return
	}
	ticks := g.ticks
	if ticks == nil {
		t := time.NewTicker(g.sweepInterval())
		defer t.Stop()
		ticks = t.C
	}
	g.log.Info("idle close watchdog started",
		"window", g.policy.Window, "sweep", g.sweepInterval(),
		"heap_ceiling_bytes", g.policy.HeapCeiling, "goroutine_ceiling", g.policy.GoroutineCeiling)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			g.sweep(g.now())
		}
	}
}

// sweepInterval is the cadence Run ticks at: the configured Sweep, else
// [defaultSweep], in either case capped at Window/[minSweepDivisor] so a short
// window is still checked several times inside itself. It is never non-positive —
// time.NewTicker panics on that.
func (g *Guard) sweepInterval() time.Duration {
	d := g.policy.Sweep
	if d <= 0 {
		d = defaultSweep
	}
	if w := g.policy.Window; w > 0 && d > w/minSweepDivisor {
		d = w / minSweepDivisor
	}
	if d <= 0 {
		d = time.Millisecond
	}
	return d
}

// sweep is one watchdog pass at now: it refreshes each session's activity from
// its mark counter, closes every session that breached a PER-SESSION limit (the
// reconnect-cycle ceiling, then the Idle Close Window), and — only if none did —
// sheds the quietest session when the Voice INSTANCE is over a resource ceiling.
//
// The two-phase shape is deliberate. A per-session close has ALREADY freed (or is
// about to free) that session's footprint, and the runtime has not reported it
// yet, so shedding a second session on the same reading would over-correct on a
// number that is about to change. One sweep therefore ends at most one session
// for process-wide pressure, and none at all when it already ended one of its own.
//
// Callbacks are collected under the lock and invoked outside it: close reaches
// the session Manager, which takes its own mutex (CONTRIBUTING: never hold a lock
// across a call that can block).
func (g *Guard) sweep(now time.Time) {
	if g == nil {
		return
	}
	type breach struct {
		s      *Session
		reason string
	}
	var (
		breached []breach
		victim   *Session
	)

	g.mu.Lock()
	for _, s := range g.order {
		// A moved counter is the only evidence of audio. Reading it here — rather
		// than stamping a timestamp on the audio path — is what keeps Mark free.
		if n := s.marks.Load(); n != s.seen {
			s.seen = n
			s.lastActive = now
		}
		if s.closed {
			continue
		}
		// Churn first: a session cycling itself to death is leaking whether or not it
		// is also idle, and naming the churn is the more actionable end_reason.
		switch {
		case g.policy.MaxCycles > 0 && s.cycles.Load() > uint64(g.policy.MaxCycles):
			s.closed = true
			breached = append(breached, breach{s, ReasonChurn})
		case g.policy.Window > 0 && now.Sub(s.lastActive) >= g.policy.Window:
			s.closed = true
			breached = append(breached, breach{s, ReasonIdle})
		}
	}
	if len(breached) == 0 {
		victim = g.quietestLocked()
	}
	g.mu.Unlock()

	for _, b := range breached {
		g.log.Warn("closing Voice Session on an Idle Close policy breach",
			"session", b.s.id, "reason", b.reason, "window", g.policy.Window,
			"last_audio", b.s.lastActive, "cycles", b.s.cycles.Load(), "max_cycles", g.policy.MaxCycles)
		b.s.close(b.reason)
	}
	if victim == nil {
		return
	}
	g.log.Warn("shedding the quietest Voice Session: the Voice Instance crossed a resource ceiling",
		"session", victim.id, "last_audio", victim.lastActive,
		"heap_ceiling_bytes", g.policy.HeapCeiling, "goroutine_ceiling", g.policy.GoroutineCeiling)
	victim.close(ReasonResource)
}

// quietestLocked samples the Voice Instance's footprint and, if it is over a
// configured ceiling, returns the least recently active open session (ties broken
// by enrollment order), marking it closed. It returns nil when no ceiling is
// configured, when the instance is under both, or when there is nothing to shed.
// Caller holds g.mu.
//
// The sample is taken ONLY when a ceiling is configured, so an idle-only
// deployment never pays for a runtime/metrics read.
func (g *Guard) quietestLocked() *Session {
	if g.policy.HeapCeiling == 0 && g.policy.GoroutineCeiling == 0 {
		return nil
	}
	u := g.usage()
	overHeap := g.policy.HeapCeiling > 0 && u.HeapBytes > g.policy.HeapCeiling
	overGoroutines := g.policy.GoroutineCeiling > 0 && u.Goroutines > g.policy.GoroutineCeiling
	if !overHeap && !overGoroutines {
		return nil
	}
	var victim *Session
	for _, s := range g.order {
		// A session this Guard already closed is still unwinding; picking it again
		// would spend the sweep's one shed on a session that is already leaving.
		if s.closed {
			continue
		}
		if victim == nil || s.lastActive.Before(victim.lastActive) {
			victim = s
		}
	}
	if victim == nil {
		g.log.Warn("Voice Instance is over a resource ceiling but hosts no session that can be shed",
			"heap_bytes", u.HeapBytes, "goroutines", u.Goroutines)
		return nil
	}
	victim.closed = true
	return victim
}

// Enrolled reports how many Voice Sessions this watchdog currently tracks. A nil
// Guard tracks none. It is the seam that makes "the owner released its
// enrollment" observable — a handle that outlives its session is a leak of
// exactly the kind this package exists to prevent.
func (g *Guard) Enrolled() int {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.order)
}

// heapFootprintMetric / heapReleasedMetric are the runtime/metrics names
// [runtimeUsage] derives the Go heap footprint from: everything the runtime has
// mapped, minus what it has handed back to the OS. That difference is the number
// an operator's container memory limit actually sees, which is why the ceiling is
// expressed against it rather than against live-object bytes.
const (
	heapFootprintMetric = "/memory/classes/total:bytes"
	heapReleasedMetric  = "/memory/classes/heap/released:bytes"
)

// runtimeUsage is the production [Guard.usage] sampler. It runs once per sweep
// (30s by default), so the runtime/metrics read — which, unlike
// runtime.ReadMemStats, does not stop the world — is free at this cadence.
func runtimeUsage() Usage {
	samples := []metrics.Sample{
		{Name: heapFootprintMetric},
		{Name: heapReleasedMetric},
	}
	metrics.Read(samples)
	var total, released uint64
	if samples[0].Value.Kind() == metrics.KindUint64 {
		total = samples[0].Value.Uint64()
	}
	if samples[1].Value.Kind() == metrics.KindUint64 {
		released = samples[1].Value.Uint64()
	}
	if released > total {
		released = total
	}
	return Usage{HeapBytes: total - released, Goroutines: runtime.NumGoroutine()}
}
