package observe

import (
	"bytes"
	"fmt"
	"log/slog"
	"runtime/pprof"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// goroutineLeaksHelp doubles as the operator documentation for the series: the
// number is only as fresh as the last elapsed interval, and refreshing it is
// not free.
const goroutineLeaksHelp = "Goroutines the Go 1.27 goroutineleak profile reports as permanently blocked " +
	"(parked on a concurrency primitive no runnable goroutine can reach). Refreshed at most once per " +
	"GLYPHOXA_GOROUTINE_LEAK_SAMPLE_INTERVAL on scrape; each refresh runs a GC cycle."

// leakSampleInterval parses GLYPHOXA_GOROUTINE_LEAK_SAMPLE_INTERVAL: a Go
// duration > 0 arms the leak gauge, anything else (unset, unparsable, zero,
// negative) keeps it off. Off is the default for the same reason GLYPHOXA_PPROF
// defaults off — every sample forces a GC cycle, which the realtime voice path
// should never pay without an operator deciding so — and a typo therefore
// degrades to "gauge absent", visibly, rather than to some surprise cadence.
// GOROUTINE is in the name so it greps alongside its companion knob
// GLYPHOXA_VOICE_GOROUTINE_CEILING and never reads as a memory-leak setting.
func leakSampleInterval(getenv func(string) string) time.Duration {
	d, err := time.ParseDuration(getenv("GLYPHOXA_GOROUTINE_LEAK_SAMPLE_INTERVAL"))
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

// goroutineLeakCollector exports glyphoxa_goroutine_leaks by lazily sampling
// the runtime's goroutineleak profile at scrape time, never more than once per
// interval. Lazy-on-scrape instead of a ticker goroutine deliberately: it
// needs no lifecycle context (a background sampler that outlived its server
// would be a goroutine leak inside the leak detector), and an unscraped pod
// pays nothing.
type goroutineLeakCollector struct {
	desc     *prometheus.Desc
	interval time.Duration
	now      func() time.Time    // seam for tests
	sample   func() (int, error) // seam for tests; production samples the pprof profile

	mu   sync.Mutex
	last time.Time
	n    float64
}

func newGoroutineLeakCollector(interval time.Duration) *goroutineLeakCollector {
	return &goroutineLeakCollector{
		// Same shared namespace const as every other series (ADR-0032 §2.2);
		// no subsystem — a process-level reading like jobs_backlog, not a
		// glyphoxa_voice_* pipeline series.
		desc:     prometheus.NewDesc(prometheus.BuildFQName(namespace, "", "goroutine_leaks"), goroutineLeaksHelp, nil, nil),
		interval: interval,
		now:      time.Now,
		sample:   sampleGoroutineLeaks,
	}
}

// sampleGoroutineLeaks runs the runtime's leak detection (WriteTo is what
// forces the GC-based reachability pass — Profile.Count() alone never detects
// anything) and reads the leak count out of the debug=1 header line,
// "goroutineleak profile: total N". Parsing text sounds fragile but is the
// contract: the header format is shared by every legacy-form profile and the
// count is not exposed any other way without decoding the proto form.
func sampleGoroutineLeaks() (int, error) {
	p := pprof.Lookup("goroutineleak")
	if p == nil {
		return 0, fmt.Errorf("goroutineleak profile not registered — toolchain older than Go 1.27?")
	}
	var buf bytes.Buffer
	if err := p.WriteTo(&buf, 1); err != nil {
		return 0, fmt.Errorf("collect goroutineleak profile: %w", err)
	}
	header, _, _ := bytes.Cut(buf.Bytes(), []byte("\n"))
	var n int
	if _, err := fmt.Sscanf(string(header), "goroutineleak profile: total %d", &n); err != nil {
		return 0, fmt.Errorf("parse goroutineleak header %q: %w", header, err)
	}
	return n, nil
}

func (c *goroutineLeakCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }

func (c *goroutineLeakCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	if c.last.IsZero() || c.now().Sub(c.last) >= c.interval {
		// A failed sample keeps the previous value AND still advances the
		// clock: a persistently failing sampler must not turn every scrape
		// into a GC cycle. The warn is the only signal that the exported
		// number has gone stale (e.g. a future Go release reshaping the
		// profile header), so it must not be dropped — but at one line per
		// interval it cannot flood either.
		if n, err := c.sample(); err == nil {
			c.n = float64(n)
		} else {
			slog.Default().Warn("goroutine leak sample failed; glyphoxa_goroutine_leaks is stale", "err", err)
		}
		c.last = c.now()
	}
	v := c.n
	c.mu.Unlock()
	ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, v)
}
