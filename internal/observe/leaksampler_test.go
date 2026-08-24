package observe

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// TestLeakSampleIntervalParsing pins the knob's contract: only a positive Go
// duration arms the gauge; every malformed value degrades to off, never to a
// surprise cadence.
func TestLeakSampleIntervalParsing(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"", 0},
		{"banana", 0},
		{"0", 0},
		{"-5m", 0},
		{"30", 0}, // bare number is not a Go duration
		{"30s", 30 * time.Second},
		{"5m", 5 * time.Minute},
	}
	for _, tc := range cases {
		got := leakSampleInterval(fakeEnv(map[string]string{"GLYPHOXA_GOROUTINE_LEAK_SAMPLE_INTERVAL": tc.in}))
		if got != tc.want {
			t.Errorf("leakSampleInterval(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// collectValue drives one real scrape of the collector — registry, promhttp
// handler, exposition text, the same path a Prometheus takes — and returns the
// gauge's value.
func collectValue(t *testing.T, c *goroutineLeakCollector) float64 {
	t.Helper()
	reg := prometheus.NewRegistry()
	if err := reg.Register(c); err != nil {
		t.Fatalf("register collector: %v", err)
	}
	srv := httptest.NewServer(promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	defer srv.Close()
	for _, line := range strings.Split(getBody(t, srv.URL), "\n") {
		if v, ok := strings.CutPrefix(line, "glyphoxa_goroutine_leaks "); ok {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				t.Fatalf("parse gauge value %q: %v", v, err)
			}
			return f
		}
	}
	t.Fatal("glyphoxa_goroutine_leaks not in scrape")
	return 0
}

// TestGoroutineLeakCollectorCachesBetweenIntervals pins the cost model the
// series' help text promises: at most one profile collection (one GC cycle)
// per interval, however often Prometheus scrapes.
func TestGoroutineLeakCollectorCachesBetweenIntervals(t *testing.T) {
	samples := 0
	clock := time.Unix(1000, 0)
	c := newGoroutineLeakCollector(time.Minute)
	c.now = func() time.Time { return clock }
	c.sample = func() (int, error) { samples++; return samples, nil }

	if got := collectValue(t, c); got != 1 {
		t.Fatalf("first scrape = %v, want 1 (fresh sample)", got)
	}
	clock = clock.Add(30 * time.Second) // inside the interval: cached
	if got := collectValue(t, c); got != 1 {
		t.Fatalf("scrape inside interval = %v, want cached 1", got)
	}
	if samples != 1 {
		t.Fatalf("samples inside interval = %d, want 1", samples)
	}
	clock = clock.Add(31 * time.Second) // past the interval: resample
	if got := collectValue(t, c); got != 2 {
		t.Fatalf("scrape past interval = %v, want fresh 2", got)
	}
}

// TestGoroutineLeakCollectorKeepsValueOnSampleError pins the failure mode: a
// broken sampler serves the stale value and does NOT retry until the next
// interval — a persistently failing sampler must not turn every scrape into a
// GC cycle.
func TestGoroutineLeakCollectorKeepsValueOnSampleError(t *testing.T) {
	samples := 0
	clock := time.Unix(1000, 0)
	c := newGoroutineLeakCollector(time.Minute)
	c.now = func() time.Time { return clock }
	c.sample = func() (int, error) {
		samples++
		if samples == 1 {
			return 7, nil
		}
		return 0, io.ErrUnexpectedEOF
	}

	if got := collectValue(t, c); got != 7 {
		t.Fatalf("first scrape = %v, want 7", got)
	}
	clock = clock.Add(2 * time.Minute)
	if got := collectValue(t, c); got != 7 {
		t.Fatalf("scrape after failed sample = %v, want stale 7", got)
	}
	if samples != 2 {
		t.Fatalf("samples = %d, want 2 (the failure still consumed the interval)", samples)
	}
	clock = clock.Add(30 * time.Second)
	collectValue(t, c)
	if samples != 2 {
		t.Fatalf("failed sample did not advance the clock: got %d samples, want 2", samples)
	}
}

// TestSampleGoroutineLeaksReadsRealProfile drives the production sampler
// against the real runtime: it must collect and parse without error. The
// count is not asserted exactly — leakprofile_test.go deliberately leaks a
// goroutine into this test binary, so the honest assertion is "a well-formed
// non-negative number".
func TestSampleGoroutineLeaksReadsRealProfile(t *testing.T) {
	n, err := sampleGoroutineLeaks()
	if err != nil {
		t.Fatalf("sampleGoroutineLeaks: %v", err)
	}
	if n < 0 {
		t.Fatalf("leak count = %d, want >= 0", n)
	}
}

// TestLeakGaugeMountedBehindEnvGate pins the wiring end to end: with the
// interval set, /metrics carries glyphoxa_goroutine_leaks; without it, the
// series must be absent so the default-off promise holds.
func TestLeakGaugeMountedBehindEnvGate(t *testing.T) {
	t.Setenv("GLYPHOXA_GOROUTINE_LEAK_SAMPLE_INTERVAL", "1h")
	armed := httptest.NewServer(newMux(NewPrometheusRecorder(), nil))
	defer armed.Close()
	if body := getBody(t, armed.URL+"/metrics"); !strings.Contains(body, "glyphoxa_goroutine_leaks") {
		t.Fatal("/metrics missing glyphoxa_goroutine_leaks with GLYPHOXA_GOROUTINE_LEAK_SAMPLE_INTERVAL set")
	}

	t.Setenv("GLYPHOXA_GOROUTINE_LEAK_SAMPLE_INTERVAL", "")
	off := httptest.NewServer(newMux(NewPrometheusRecorder(), nil))
	defer off.Close()
	if body := getBody(t, off.URL+"/metrics"); strings.Contains(body, "glyphoxa_goroutine_leaks") {
		t.Fatal("/metrics serves glyphoxa_goroutine_leaks without the env gate")
	}
}

func getBody(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
