package elevenlabs_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts/elevenlabs"
)

// connCountingServer is an httptest server that counts the TCP connections it
// accepts and the requests it serves; every request answers 200 with a little PCM.
func connCountingServer(t *testing.T) (srv *httptest.Server, conns, reqs *atomic.Int32) {
	t.Helper()
	conns, reqs = new(atomic.Int32), new(atomic.Int32)
	srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodPost {
			_, _ = w.Write(make([]byte, 256))
		}
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, conns, reqs
}

// synthesizeAndDrain runs one Synthesize to the end of its stream. It reports
// with Errorf, not Fatalf, so it is safe to call off the test goroutine.
func synthesizeAndDrain(ctx context.Context, t *testing.T, c *elevenlabs.Client) {
	t.Helper()
	ch, err := c.Synthesize(ctx, tts.SynthesizeRequest{
		Sentence: "Willkommen, Reisender.",
		Voice:    tts.Voice{ProviderID: elevenlabs.ProviderID, VoiceID: "v"},
	})
	if err != nil {
		t.Errorf("Synthesize: %v", err)
		return
	}
	for range ch {
	}
}

// TestWarm_FirstSentenceReusesTheWarmedConnection is the #702 pre-warm bar: Warm
// opens the connection ahead of time so the first sentence of a reply rides it
// instead of paying TCP+TLS (~60–130 ms measured against the real API).
func TestWarm_FirstSentenceReusesTheWarmedConnection(t *testing.T) {
	srv, conns, reqs := connCountingServer(t)
	c := elevenlabs.New("k", elevenlabs.WithBaseURL(srv.URL))

	if err := c.Warm(context.Background()); err != nil {
		t.Fatalf("Warm: %v", err)
	}
	if got := conns.Load(); got != 1 {
		t.Fatalf("connections after Warm = %d, want 1 (Warm must open one)", got)
	}
	synthesizeAndDrain(context.Background(), t, c)
	if got := conns.Load(); got != 1 {
		t.Errorf("connections after Warm + Synthesize = %d, want 1 (the sentence must reuse the warmed connection)", got)
	}
	if got := reqs.Load(); got != 2 {
		t.Errorf("requests served = %d, want 2 (one warm, one synthesis)", got)
	}
}

// TestWarm_NoKey_IsANoOp: a keyless client (cassette tiers, CI) cannot synthesize,
// so warming it must not touch the network at all.
func TestWarm_NoKey_IsANoOp(t *testing.T) {
	t.Setenv("ELEVENLABS_API_KEY", "")
	srv, conns, _ := connCountingServer(t)
	c := elevenlabs.New("", elevenlabs.WithBaseURL(srv.URL))
	if err := c.Warm(context.Background()); err != nil {
		t.Fatalf("Warm: %v", err)
	}
	if got := conns.Load(); got != 0 {
		t.Errorf("keyless Warm opened %d connections, want 0", got)
	}
}

// overlapServer is a connection-counting server that holds every request until
// width of them have arrived, then releases that batch together. A round of width
// concurrent calls therefore genuinely overlaps — each needs a connection of its
// own — instead of overlapping only when the scheduler happens to interleave them
// (a fast first call would otherwise finish and lend its connection to the second).
func overlapServer(t *testing.T, width int) (srv *httptest.Server, conns *atomic.Int32) {
	t.Helper()
	conns = new(atomic.Int32)
	var (
		mu      sync.Mutex
		arrived int
		release = make(chan struct{})
	)
	srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gate := release
		if arrived++; arrived%width == 0 {
			close(release)
			release = make(chan struct{})
		}
		mu.Unlock()
		<-gate
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodPost {
			_, _ = w.Write(make([]byte, 256))
		}
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, conns
}

// poolWatch observes the client's connection pool from the client side, through
// net/http/httptrace. The transport hands a finished connection back to its idle
// pool on its own goroutine, some time AFTER the caller has read the body to EOF,
// so "the call returned" does not mean "the connection is reusable yet": a next
// round started in that window dials afresh although nothing was dropped. The
// tests below therefore wait for the pool's own verdict on each connection
// (kept, or refused with an error) before they start the next round.
type poolWatch struct {
	idle   chan error // one PutIdleConn verdict per finished request
	reused atomic.Int32
	dialed atomic.Int32
}

func newPoolWatch() *poolWatch { return &poolWatch{idle: make(chan error, 64)} }

// ctx returns a context whose requests report to the watch.
func (p *poolWatch) ctx() context.Context {
	return httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			if info.Reused {
				p.reused.Add(1)
			} else {
				p.dialed.Add(1)
			}
		},
		PutIdleConn: func(err error) { p.idle <- err },
	})
}

// awaitKept blocks until the pool has taken back n connections, failing the test
// if it refused one (the idle limit is too small for the round) or never got one
// back (keep-alives off, or a body left unread/unclosed). The timeout is only a
// failure path — a passing run never waits on it.
func (p *poolWatch) awaitKept(t *testing.T, n int) {
	t.Helper()
	timeout := time.After(30 * time.Second)
	for i := range n {
		select {
		case err := <-p.idle:
			if err != nil {
				t.Fatalf("the pool dropped a connection instead of keeping it idle: %v", err)
			}
		case <-timeout:
			t.Fatalf("only %d of %d connections were returned to the idle pool", i, n)
		}
	}
}

// overlappingRound runs every call at once and returns once all have finished AND
// the pool has taken each of their connections back.
func (p *poolWatch) overlappingRound(t *testing.T, calls ...func(ctx context.Context)) {
	t.Helper()
	var wg sync.WaitGroup
	for _, call := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			call(p.ctx())
		}()
	}
	wg.Wait()
	p.awaitKept(t, len(calls))
}

// assertWarmAcrossRounds runs the calls as one overlapping round, several times
// over. The first round must open exactly one connection per call and every later
// round must open none: each later call rides a connection the pool kept.
func assertWarmAcrossRounds(t *testing.T, conns *atomic.Int32, calls ...func(ctx context.Context)) {
	t.Helper()
	width := int32(len(calls))
	pool := newPoolWatch()

	pool.overlappingRound(t, calls...)
	if got := conns.Load(); got != width {
		t.Fatalf("connections after the first overlapping round = %d, want %d", got, width)
	}
	const laterRounds = 3
	for range laterRounds {
		pool.overlappingRound(t, calls...)
	}
	if got := conns.Load(); got != width {
		t.Errorf("connections grew from %d to %d across overlapping rounds; the pool drops idle connections", width, got)
	}
	if got, want := pool.reused.Load(), width*laterRounds; got != want {
		t.Errorf("calls served on a reused connection = %d, want %d (every call after the first round)", got, want)
	}
	if got := pool.dialed.Load(); got != width {
		t.Errorf("calls that had to dial = %d, want %d (the first round only)", got, width)
	}
}

// TestSynthesize_LookaheadPair_StaysWarm: the pipelined look-ahead keeps two
// Synthesize calls in flight at once (#626), so the default transport must keep
// both connections idle afterwards — the next pair reuses them with no re-dial.
// The server holds each pair until both requests have arrived, so the two
// genuinely overlap and need a connection each.
func TestSynthesize_LookaheadPair_StaysWarm(t *testing.T) {
	srv, conns := overlapServer(t, 2)
	c := elevenlabs.New("k", elevenlabs.WithBaseURL(srv.URL))

	synth := func(ctx context.Context) { synthesizeAndDrain(ctx, t, c) }
	assertWarmAcrossRounds(t, conns, synth, synth)
}

// TestSynthesize_LookaheadPairPlusWarm_StaysWarm: the look-ahead pair (#626) can
// overlap a [elevenlabs.Client.Warm] (a speech start or barge lands while both
// sentences stream), so three connections are open at once. The default
// transport must keep all three idle afterwards; the stdlib default of two idle
// per host would close one after every such round and the next round re-dials.
// The server holds each round's requests until all three have arrived, so the
// three genuinely overlap.
func TestSynthesize_LookaheadPairPlusWarm_StaysWarm(t *testing.T) {
	srv, conns := overlapServer(t, 3)
	c := elevenlabs.New("k", elevenlabs.WithBaseURL(srv.URL))

	synth := func(ctx context.Context) { synthesizeAndDrain(ctx, t, c) }
	warm := func(ctx context.Context) {
		if err := c.Warm(ctx); err != nil {
			t.Errorf("Warm: %v", err)
		}
	}
	assertWarmAcrossRounds(t, conns, synth, synth, warm)
}
