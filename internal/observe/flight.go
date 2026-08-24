package observe

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/trace"
	"slices"
	"strconv"
	"sync"
	"time"
)

// flightThreshold parses GLYPHOXA_FLIGHT_TRACE_THRESHOLD_MS: a plain integer
// count of milliseconds > 0 arms the flight recorder, anything else (unset,
// unparsable, zero, negative) keeps it off. Off is the default for the same
// reason GLYPHOXA_PPROF is (internal/observe/server.go): an armed recorder
// costs the realtime voice path ~1-2% CPU continuously, which no pod should
// pay without an operator deciding so — so a typo degrades to "no snapshots",
// visibly, rather than to a surprise always-on tracer.
//
// Milliseconds rather than a Go duration string because the number an operator
// reaches for is the response_latency SLO itself (p50 ≤ 1.2s, p95 ≤ 2.5s), read
// off the same histogram whose buckets are seconds — "2500" is the natural
// transcription of "snapshot anything past p95".
func flightThreshold(getenv func(string) string) time.Duration {
	ms, err := strconv.Atoi(getenv("GLYPHOXA_FLIGHT_TRACE_THRESHOLD_MS"))
	if err != nil || ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

// flightKeep bounds the snapshot directory: the newest N files survive, the
// rest are deleted after each write. A trace window is megabytes (MaxBytes
// below), so an unbounded directory would eventually fill the volume it sits
// on — and the older a snapshot, the less an operator wants it.
const flightKeep = 8

// flightCooldown is the minimum wall time between two snapshots. A degraded
// session breaches on every turn; without a cooldown one incident would burn
// the whole flightKeep history (and the CPU to write it) on near-identical
// windows. One file per incident is what an operator can actually read.
const flightCooldown = 30 * time.Second

// FlightRecorder turns a response_latency SLO breach into an execution-trace
// snapshot of the seconds leading up to it. It is off unless
// GLYPHOXA_FLIGHT_TRACE_THRESHOLD_MS arms it, and voice-mode only.
type FlightRecorder struct {
	threshold time.Duration
	dir       string
	writeTo   func(io.Writer) (int64, error) // seam for tests; production dumps the trace window
	stop      func()                         // seam for tests; production stops the runtime recorder
	log       *slog.Logger
	now       func() time.Time // seam for tests

	trigger  chan time.Duration
	done     chan struct{}
	closeOne sync.Once

	// last is the time of the previous snapshot. Writer-goroutine-owned — no
	// lock, because nothing else ever reads it.
	last time.Time
}

// StartFlightRecorder arms the #607 flight recorder, or returns nil when
// GLYPHOXA_FLIGHT_TRACE_THRESHOLD_MS does not (the default). A nil recorder is
// usable as "off": callers skip the hook install and the deferred Close.
//
// While armed, the runtime keeps a rolling execution-trace window in memory —
// roughly the last 10s, capped at 16 MiB — at about 1-2% CPU. That cost is why
// this is voice-mode-only and opt-in: it buys the one thing a histogram bucket
// cannot give, namely what a single slow turn was actually blocked on. Every
// response_latency span past the threshold dumps that window to
// GLYPHOXA_FLIGHT_TRACE_DIR (default $TMPDIR/glyphoxa-flight); read a snapshot
// with `go tool trace flight-....trace`.
//
// Deployment note: the container image is FROM scratch and its filesystem is
// read-only, so the directory must be a writable mount (an emptyDir volume) for
// snapshots to survive the write — see the commented example in the chart's
// values.yaml. If it is not writable, arming still costs the CPU but every
// snapshot fails with a logged warning, and nothing else breaks.
func StartFlightRecorder(getenv func(string) string, log *slog.Logger) *FlightRecorder {
	threshold := flightThreshold(getenv)
	if threshold <= 0 {
		return nil
	}
	dir := getenv("GLYPHOXA_FLIGHT_TRACE_DIR")
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "glyphoxa-flight")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		// Log and carry on rather than refusing to boot: a voice node with an
		// unwritable diagnostics dir is still a working voice node.
		log.Warn("flight recorder: snapshot dir unusable; snapshots will fail",
			"dir", dir, "err", err)
	}
	// MinAge 10s covers a whole slow turn (the SLO tail is ~2.5s) plus the lead-up
	// that explains it; MaxBytes caps the in-memory window — and each snapshot on
	// disk — at 16 MiB, which is what makes flightKeep a bounded disk budget.
	rec := trace.NewFlightRecorder(trace.FlightRecorderConfig{
		MinAge:   10 * time.Second,
		MaxBytes: 16 << 20,
	})
	if err := rec.Start(); err != nil {
		// At most one flight recorder may be active per process; if something
		// else holds it, stay off rather than half-armed.
		log.Warn("flight recorder: not armed", "err", err)
		return nil
	}
	log.Info("flight recorder armed: response_latency breaches will dump an execution trace",
		"threshold", threshold, "dir", dir, "keep", flightKeep)
	return newFlightRecorder(threshold, dir, rec.WriteTo, rec.Stop, log)
}

// newFlightRecorder wires an armed recorder over an existing directory and
// starts its single writer goroutine. StartFlightRecorder is the production
// constructor; this seam exists so a test can drive the trigger/write path
// without arming the process-global runtime tracer (only one may be active).
func newFlightRecorder(threshold time.Duration, dir string, writeTo func(io.Writer) (int64, error), stop func(), log *slog.Logger) *FlightRecorder {
	f := &FlightRecorder{
		threshold: threshold,
		dir:       dir,
		writeTo:   writeTo,
		stop:      stop,
		log:       log,
		now:       time.Now,
		// Capacity 1: the hook must never block the voice path, and a burst of
		// breaches wants ONE snapshot, not a queue of them.
		trigger: make(chan time.Duration, 1),
		done:    make(chan struct{}),
	}
	go f.run()
	return f
}

// LatencyBreach is the ResponseLatency hook. It runs on the observe subscriber
// goroutine while it holds its lock, so it does exactly two things — compare
// and hand off — and never touches the filesystem itself. A trigger that finds
// the writer busy is dropped: the in-flight snapshot covers the same window.
func (f *FlightRecorder) LatencyBreach(d time.Duration) {
	if d < f.threshold {
		return
	}
	select {
	case f.trigger <- d:
	default:
	}
}

// run is the single writer goroutine: every snapshot is written here, so the
// trace window is dumped at most once at a time and the hot path pays nothing
// but a channel send.
func (f *FlightRecorder) run() {
	for {
		select {
		case <-f.done:
			return
		case d := <-f.trigger:
			f.snapshot(d)
		}
	}
}

// snapshot dumps the trace window to a timestamped file. Every failure is
// logged and swallowed: a voice node must keep talking when its diagnostics
// cannot write (the FROM scratch image has no writable dir unless one is
// mounted — see StartFlightRecorder).
func (f *FlightRecorder) snapshot(d time.Duration) {
	now := f.now()
	if !f.last.IsZero() && now.Sub(f.last) < flightCooldown {
		return
	}
	f.last = now
	name := fmt.Sprintf("flight-%s-%dms.trace",
		now.UTC().Format("20060102T150405.000Z"), d.Milliseconds())
	path := filepath.Join(f.dir, name)
	file, err := os.Create(path)
	if err != nil {
		f.log.Warn("flight recorder: create snapshot failed", "path", path, "err", err)
		return
	}
	if _, err := f.writeTo(file); err != nil {
		f.log.Warn("flight recorder: write snapshot failed", "path", path, "err", err)
	}
	if err := file.Close(); err != nil {
		f.log.Warn("flight recorder: close snapshot failed", "path", path, "err", err)
	}
	f.rotate()
}

// rotate deletes everything but the newest flightKeep snapshots. The filename
// carries a UTC timestamp with a fixed-width layout, so lexicographic order IS
// chronological order — no stat calls, and a clock that jumps still yields a
// deterministic ordering rather than an arbitrary one.
func (f *FlightRecorder) rotate() {
	names, err := filepath.Glob(filepath.Join(f.dir, "flight-*.trace"))
	if err != nil {
		f.log.Warn("flight recorder: list snapshots failed", "dir", f.dir, "err", err)
		return
	}
	if len(names) <= flightKeep {
		return
	}
	slices.Sort(names)
	for _, old := range names[:len(names)-flightKeep] {
		if err := os.Remove(old); err != nil {
			f.log.Warn("flight recorder: prune snapshot failed", "path", old, "err", err)
		}
	}
}

// Close stops the writer goroutine and the runtime recorder. Idempotent, so a
// deferred Close beside an error path cannot panic the process.
func (f *FlightRecorder) Close() {
	f.closeOne.Do(func() {
		close(f.done)
		f.stop()
	})
}
