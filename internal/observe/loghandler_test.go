package observe

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// disgoLogger reproduces how disgo tags its voice logger: name=bot → name=voice
// → name=voice_conn via chained With, then logs the decrypt error at Error. The
// returned logger writes through the filter into buf.
func disgoLogger(buf *bytes.Buffer, level slog.Level, onDAVE func()) *slog.Logger {
	base := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: level})
	h := NewDAVEFilterHandler(base, onDAVE)
	return slog.New(h).With("name", "bot").With("name", "voice").With("name", "voice_conn")
}

func TestDAVEFilterSuppressesBenignNoiseInProd(t *testing.T) {
	// AC: on a prod (Info) console the benign DAVE trickle leaves zero lines, but
	// the counter still moves — the information is preserved as a metric.
	var buf bytes.Buffer
	var count int
	log := disgoLogger(&buf, slog.LevelInfo, func() { count++ })

	for i := 0; i < 5; i++ {
		log.Error("error while reading packet",
			"err", "failed to DAVE decrypt packet: failed to decrypt frame")
	}

	if buf.Len() != 0 {
		t.Fatalf("benign DAVE noise leaked to prod console:\n%s", buf.String())
	}
	if count != 5 {
		t.Fatalf("DAVE counter = %d, want 5 (every packet counted even when not logged)", count)
	}
}

func TestDAVEFilterEmitsRateLimitedSurvivorAtDebug(t *testing.T) {
	// In dev (Debug) one survivor per window is emitted at Debug; the survivor
	// that OPENS a window reports suppressed=N for the drops since the previous
	// survivor. We drive wall-clock-independent times via the record's own
	// timestamp by logging through a handler over a fixed base, advancing time
	// with explicit records.
	var buf bytes.Buffer
	base := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	h := NewDAVEFilterHandler(base, nil).
		WithAttrs([]slog.Attr{slog.String("name", "voice_conn")})

	emit := func(at time.Time) {
		r := slog.NewRecord(at, slog.LevelError, "error while reading packet", 0)
		r.AddAttrs(slog.String("err", "failed to DAVE decrypt packet: failed to decrypt frame"))
		if err := h.Handle(t.Context(), r); err != nil {
			t.Fatal(err)
		}
	}

	t0 := time.Unix(1000, 0)
	emit(t0)                       // opens window 1, suppressed=0
	emit(t0.Add(time.Second))      // suppressed
	emit(t0.Add(2 * time.Second))  // suppressed
	emit(t0.Add(3 * time.Second))  // suppressed (3 dropped in window 1)
	emit(t0.Add(11 * time.Second)) // opens window 2 → survivor reports suppressed=3

	out := buf.String()
	if n := strings.Count(out, "error while reading packet"); n != 2 {
		t.Fatalf("got %d survivor lines, want exactly 2 (one per window):\n%s", n, out)
	}
	if strings.Count(out, "level=DEBUG") != 2 {
		t.Fatalf("survivors not all downgraded to Debug:\n%s", out)
	}
	if !strings.Contains(out, "suppressed=3") {
		t.Fatalf("window-2 survivor missing suppressed=3 count:\n%s", out)
	}
}

func TestDAVEFilterPreservesRealGatewayError(t *testing.T) {
	// The DONE gate: a real voice-gateway error must still surface at Error even
	// in prod. We exercise the two ways a record can look like the benign one but
	// isn't, plus a same-name different-message error.
	cases := []struct {
		name string
		msg  string
		err  string
	}{
		{"different message, voice_conn", "voice gateway closed", "websocket: close 4014"},
		{"benign message but non-DAVE err", "error while reading packet", "udp: connection reset"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := disgoLogger(&buf, slog.LevelInfo, nil)
			log.Error(tc.msg, "err", tc.err)

			out := buf.String()
			if !strings.Contains(out, "level=ERROR") {
				t.Fatalf("real gateway error was suppressed; want level=ERROR:\n%q", out)
			}
			if !strings.Contains(out, tc.msg) {
				t.Fatalf("real gateway error message missing:\n%q", out)
			}
		})
	}
}

func TestDAVEFilterIgnoresNonVoiceConn(t *testing.T) {
	// The DAVE-message text coming from somewhere that is NOT name=voice_conn
	// (e.g. a different subsystem) is not our known-benign case — pass it through.
	var buf bytes.Buffer
	base := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	log := slog.New(NewDAVEFilterHandler(base, nil)).With("name", "bot")

	log.Error("error while reading packet",
		"err", "failed to DAVE decrypt packet: failed to decrypt frame")

	if buf.Len() == 0 {
		t.Fatal("record without name=voice_conn was suppressed; only voice_conn is benign")
	}
}

func TestRateLimiterCountsAndReopens(t *testing.T) {
	l := &rateLimiter{window: 10 * time.Second}
	t0 := time.Unix(0, 0)

	if emit, sup := l.allow(t0); !emit || sup != 0 {
		t.Fatalf("first event: emit=%v sup=%d, want true/0", emit, sup)
	}
	for i := 0; i < 3; i++ {
		if emit, _ := l.allow(t0.Add(time.Second)); emit {
			t.Fatal("within-window event was emitted, want suppressed")
		}
	}
	if emit, sup := l.allow(t0.Add(11 * time.Second)); !emit || sup != 3 {
		t.Fatalf("next window: emit=%v sup=%d, want true/3", emit, sup)
	}
}

func TestRateLimiterConcurrent(t *testing.T) {
	// The limiter is shared by pointer across handler derivations and hit from the
	// disgo receive goroutine(s); the race detector guards the shared state.
	l := &rateLimiter{window: time.Millisecond}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.allow(time.Now())
		}()
	}
	wg.Wait()
}
