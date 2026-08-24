package observe

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"runtime/pprof"
	"strings"
	"testing"
	"time"
)

// TestPprofGateOffByDefault pins the #586 contract: without GLYPHOXA_PPROF=1
// the observability mux serves NO /debug/pprof endpoints — profiles expose
// goroutine stacks (argument values included) and burn CPU on demand, so the
// default must stay 404.
func TestPprofGateOffByDefault(t *testing.T) {
	// Pin the gate closed rather than trusting the ambient environment — a
	// developer with GLYPHOXA_PPROF=1 exported (say, while chasing a live-pod
	// profile) must not see this test fail.
	t.Setenv("GLYPHOXA_PPROF", "")
	srv := httptest.NewServer(newMux(NewPrometheusRecorder(), nil))
	defer srv.Close()

	for _, path := range []string{"/debug/pprof/", "/debug/pprof/goroutineleak"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET %s without GLYPHOXA_PPROF = %d, want 404", path, resp.StatusCode)
		}
	}
}

// TestPprofGateServesGoroutineLeakProfile is the live-pod leak-diagnosis
// DONE-gate for the Go 1.27 upgrade: with the gate open, the observability
// listener serves the new goroutineleak profile via pprof.Index, so an
// operator can ask a running pod "which goroutines can never wake up again"
// without a rebuild.
func TestPprofGateServesGoroutineLeakProfile(t *testing.T) {
	t.Setenv("GLYPHOXA_PPROF", "1")
	srv := httptest.NewServer(newMux(NewPrometheusRecorder(), nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/debug/pprof/goroutineleak?debug=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/debug/pprof/goroutineleak = %d, want 200; body:\n%s", resp.StatusCode, body)
	}
	if !strings.HasPrefix(string(body), "goroutineleak profile: total ") {
		t.Fatalf("/debug/pprof/goroutineleak?debug=1 did not serve the text profile:\n%s", body)
	}
}

// leakBlockedForever spawns the canonical leak the goroutineleak profile is
// built to catch: a goroutine parked on a channel receive whose channel goes
// unreachable the moment this function returns, so no send can ever wake it.
// The goroutine survives for the rest of the test binary — deliberate, and
// safe here because this package's tests don't assert a quiet goroutine
// baseline the way the goleak-guarded packages do.
//
//go:noinline
func leakBlockedForever() {
	ch := make(chan struct{})
	go func() { <-ch }()
}

// TestGoroutineLeakProfileDetectsBlockedGoroutine proves the profile does its
// job in this binary, not just that the endpoint answers: a goroutine blocked
// on an unreachable channel must show up in the profile's stacks. Detection is
// GC-driven and only sees the goroutine once it has actually parked, so the
// test polls — the first collection after spawning can legitimately miss it.
func TestGoroutineLeakProfileDetectsBlockedGoroutine(t *testing.T) {
	p := pprof.Lookup("goroutineleak")
	if p == nil {
		t.Fatal("goroutineleak profile missing — toolchain older than Go 1.27?")
	}

	leakBlockedForever()

	deadline := time.Now().Add(10 * time.Second)
	var last []byte
	for {
		var buf bytes.Buffer
		if err := p.WriteTo(&buf, 1); err != nil {
			t.Fatalf("WriteTo: %v", err)
		}
		last = buf.Bytes()
		if bytes.Contains(last, []byte("leakBlockedForever")) {
			return // leak reported with its spawning frame — the diagnosis works
		}
		if time.Now().After(deadline) {
			t.Fatalf("leaked goroutine never appeared in the goroutineleak profile:\n%s", last)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestGoroutineLeakProfileIgnoresReachableBlocked pins the other half of the
// contract: a goroutine blocked on a channel the test still holds is merely
// waiting, not leaked — the profile must not report it, or every idle worker
// pool in the app would read as a leak.
func TestGoroutineLeakProfileIgnoresReachableBlocked(t *testing.T) {
	p := pprof.Lookup("goroutineleak")
	if p == nil {
		t.Fatal("goroutineleak profile missing — toolchain older than Go 1.27?")
	}

	wake := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-wake // reachable via this test's stack: waiting, not leaked
	}()
	defer func() {
		close(wake)
		<-done
	}()

	// The assertion is only meaningful once the goroutine has actually parked
	// on the receive — before that, "not reported" would be vacuously true.
	// Wait until the all-goroutine dump shows the closure blocked in a chan
	// receive, then collect.
	waitDeadline := time.Now().Add(10 * time.Second)
	for {
		buf := make([]byte, 1<<20)
		dump := buf[:runtime.Stack(buf, true)]
		if i := bytes.Index(dump, []byte("TestGoroutineLeakProfileIgnoresReachableBlocked.func1")); i >= 0 {
			// The goroutine's state header ("goroutine N [chan receive]:")
			// precedes the frame that names the closure.
			header := dump[:i]
			if j := bytes.LastIndex(header, []byte("goroutine ")); j >= 0 && bytes.Contains(header[j:], []byte("[chan receive")) {
				break
			}
		}
		if time.Now().After(waitDeadline) {
			t.Fatal("helper goroutine never parked on its channel receive")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Collected after the park by construction, so the profile's GC pass has
	// genuinely classified this goroutine — and must have classified it as
	// merely waiting.
	var buf bytes.Buffer
	if err := p.WriteTo(&buf, 1); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	if bytes.Contains(buf.Bytes(), []byte("TestGoroutineLeakProfileIgnoresReachableBlocked")) {
		t.Fatalf("reachable blocked goroutine was reported as leaked:\n%s", buf.Bytes())
	}
}
