package observe

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// snapshotName is the on-disk contract: UTC timestamp to millisecond precision
// plus the breaching latency, so a lexicographic listing is chronological and
// an operator can pick the worst spike out of a directory listing.
var snapshotName = regexp.MustCompile(`^flight-\d{8}T\d{6}\.\d{3}Z-\d+ms\.trace$`)

// newTestRecorder builds an armed recorder over a temp dir with a fake
// window-dump, so a test exercises the trigger/write/rotate path without
// arming the process-global runtime tracer.
func newTestRecorder(t *testing.T, threshold time.Duration) (*FlightRecorder, string) {
	t.Helper()
	dir := t.TempDir()
	f := newFlightRecorder(threshold, dir, func(w io.Writer) (int64, error) {
		n, err := io.WriteString(w, "fake-trace")
		return int64(n), err
	}, func() {}, slog.New(slog.DiscardHandler))
	t.Cleanup(f.Close)
	return f, dir
}

// snapshots lists the trace files the recorder has written, waiting briefly for
// the writer goroutine (the hook is deliberately asynchronous).
func snapshots(t *testing.T, dir string, want int) []string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read snapshot dir: %v", err)
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		if len(names) == want || time.Now().After(deadline) {
			return names
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestFlightRecorderCooldownExpires drives the cooldown off a fake clock so
// both directions are pinned: it must SUPPRESS a breach inside the window and
// must RE-ARM once the window elapses. Without the second half, a recorder that
// snapshots exactly once per process lifetime would look healthy.
func TestFlightRecorderCooldownExpires(t *testing.T) {
	f, dir := newTestRecorder(t, 500*time.Millisecond)
	var clock atomic.Int64 // nanoseconds since base; atomic so the writer may read it freely
	base := time.Date(2026, 8, 24, 20, 0, 0, 0, time.UTC)
	f.now = func() time.Time { return base.Add(time.Duration(clock.Load())) }

	f.LatencyBreach(900 * time.Millisecond)
	if names := snapshots(t, dir, 1); len(names) != 1 {
		t.Fatalf("first breach: snapshots = %v, want 1", names)
	}

	// Inside the cooldown: same incident, no second dump.
	clock.Store(int64(10 * time.Second))
	f.LatencyBreach(900 * time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	if names := snapshots(t, dir, 1); len(names) != 1 {
		t.Fatalf("breach 10s later: snapshots = %v, want still 1", names)
	}

	// Past the cooldown: a later spike is a new incident and gets its own trace.
	clock.Store(int64(flightCooldown + time.Second))
	f.LatencyBreach(900 * time.Millisecond)
	if names := snapshots(t, dir, 2); len(names) != 2 {
		t.Fatalf("breach after the cooldown: snapshots = %v, want 2", names)
	}
}

// TestFlightRecorderWritesSnapshotOnBreach is the headline behavior: a turn
// slower than the threshold leaves an inspectable trace file behind (#607).
func TestFlightRecorderWritesSnapshotOnBreach(t *testing.T) {
	f, dir := newTestRecorder(t, 500*time.Millisecond)

	f.LatencyBreach(501 * time.Millisecond)

	names := snapshots(t, dir, 1)
	if len(names) != 1 {
		t.Fatalf("snapshots = %v, want exactly 1", names)
	}
	if !snapshotName.MatchString(names[0]) {
		t.Fatalf("snapshot %q does not match %v", names[0], snapshotName)
	}
}

// TestFlightRecorderIgnoresLatencyUnderThreshold: the common case is a healthy
// turn, and a healthy turn must cost nothing but a comparison — no file, no
// disk churn, no writer wakeup.
func TestFlightRecorderIgnoresLatencyUnderThreshold(t *testing.T) {
	f, dir := newTestRecorder(t, 500*time.Millisecond)

	f.LatencyBreach(499 * time.Millisecond)

	time.Sleep(50 * time.Millisecond) // give the writer a chance to misbehave
	if names := snapshots(t, dir, 0); len(names) != 0 {
		t.Fatalf("snapshots = %v, want none", names)
	}
}

// TestFlightRecorderKeepsBoundedHistory: trace windows are megabytes each, and
// the directory is a mounted emptyDir with a finite size, so a night of spikes
// must not fill the node's disk. Oldest out, newest in, ADR-0032's "small,
// bounded surface" applied to files.
func TestFlightRecorderKeepsBoundedHistory(t *testing.T) {
	f, dir := newTestRecorder(t, 500*time.Millisecond)

	var seeded []string
	for i := range flightKeep {
		name := fmt.Sprintf("flight-20200101T00000%d.000Z-900ms.trace", i)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("old"), 0o644); err != nil {
			t.Fatalf("seed snapshot: %v", err)
		}
		seeded = append(seeded, name)
	}

	f.LatencyBreach(900 * time.Millisecond)

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, seeded[0])); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("oldest snapshot %q survived rotation", seeded[0])
		}
		time.Sleep(5 * time.Millisecond)
	}
	names := snapshots(t, dir, flightKeep)
	if len(names) != flightKeep {
		t.Fatalf("snapshots = %v, want %d", names, flightKeep)
	}
}

// TestFlightRecorderCoalescesBurst: a degraded provider makes every turn in a
// session breach, and 200 consecutive breaches must not mean 200 window dumps
// — each dump costs CPU and megabytes, on the very node already struggling.
// One snapshot per cooldown covers the incident.
func TestFlightRecorderCoalescesBurst(t *testing.T) {
	f, dir := newTestRecorder(t, 500*time.Millisecond)

	for range 10 {
		f.LatencyBreach(900 * time.Millisecond)
		time.Sleep(2 * time.Millisecond) // let the writer drain between triggers
	}

	time.Sleep(50 * time.Millisecond)
	if names := snapshots(t, dir, 1); len(names) != 1 {
		t.Fatalf("snapshots = %v, want exactly 1 for a burst", names)
	}
}

// TestFlightRecorderCloseIsIdempotentAndStopsWriter: Close sits behind a
// `defer` next to error returns in the voice entrypoints, so a second call
// must be harmless — and the writer goroutine must actually die, or an armed
// voice node leaks one per restart of the loop.
func TestFlightRecorderCloseIsIdempotentAndStopsWriter(t *testing.T) {
	defer goleak.VerifyNone(t)

	dir := t.TempDir()
	stops := 0
	f := newFlightRecorder(500*time.Millisecond, dir,
		func(w io.Writer) (int64, error) { return 0, nil },
		func() { stops++ },
		slog.New(slog.DiscardHandler))

	f.Close()
	f.Close()

	if stops != 1 {
		t.Fatalf("runtime recorder stopped %d times, want exactly 1", stops)
	}
}

// TestStartFlightRecorderOffByDefault: an unarmed process must not pay the
// recorder's continuous CPU cost, and callers rely on the nil to skip both the
// hook install and the deferred Close (ADR-0032's opt-in posture, as with
// GLYPHOXA_PPROF).
func TestStartFlightRecorderOffByDefault(t *testing.T) {
	defer goleak.VerifyNone(t)

	if f := StartFlightRecorder(func(string) string { return "" }, slog.New(slog.DiscardHandler)); f != nil {
		f.Close()
		t.Fatal("StartFlightRecorder armed a recorder with no threshold set")
	}
}

// TestStartFlightRecorderArmsAndCreatesDir: with the knob set, the recorder
// runs against the real runtime tracer and the snapshot directory exists before
// the first breach — a missing dir must not turn into a lost trace mid-incident.
func TestStartFlightRecorderArmsAndCreatesDir(t *testing.T) {
	defer goleak.VerifyNone(t)

	dir := filepath.Join(t.TempDir(), "snapshots")
	env := map[string]string{
		"GLYPHOXA_FLIGHT_TRACE_THRESHOLD_MS": "500",
		"GLYPHOXA_FLIGHT_TRACE_DIR":          dir,
	}
	f := StartFlightRecorder(func(k string) string { return env[k] }, slog.New(slog.DiscardHandler))
	if f == nil {
		t.Fatal("StartFlightRecorder returned nil for an armed threshold")
	}
	defer f.Close()

	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("snapshot dir %q not created: %v", dir, err)
	}
	if f.threshold != 500*time.Millisecond {
		t.Fatalf("threshold = %v, want 500ms", f.threshold)
	}
}

// TestFlightThreshold pins the arming knob's fail-safe posture: the flight
// recorder costs ~1-2% CPU on the realtime voice path while armed, so anything
// that is not an explicit positive millisecond count must read as OFF — a typo
// degrades to "no snapshots", visibly, never to a surprise always-on recorder.
func TestFlightThreshold(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  string
		want time.Duration
	}{
		{"unset", "", 0},
		{"zero", "0", 0},
		{"negative", "-5", 0},
		{"junk", "abc", 0},
		{"duration string is not a millisecond count", "750ms", 0},
		{"armed", "750", 750 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := flightThreshold(func(k string) string {
				if k != "GLYPHOXA_FLIGHT_TRACE_THRESHOLD_MS" {
					return ""
				}
				return tc.env
			})
			if got != tc.want {
				t.Fatalf("flightThreshold(%q) = %v, want %v", tc.env, got, tc.want)
			}
		})
	}
}
