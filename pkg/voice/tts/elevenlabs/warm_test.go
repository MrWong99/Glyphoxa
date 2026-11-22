package elevenlabs_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

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

func synthesizeAndDrain(t *testing.T, c *elevenlabs.Client) {
	t.Helper()
	ch, err := c.Synthesize(context.Background(), tts.SynthesizeRequest{
		Sentence: "Willkommen, Reisender.",
		Voice:    tts.Voice{ProviderID: elevenlabs.ProviderID, VoiceID: "v"},
	})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
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
	synthesizeAndDrain(t, c)
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

// TestSynthesize_LookaheadPair_StaysWarm: the pipelined look-ahead keeps two
// Synthesize calls in flight at once (#626), so the default transport must keep
// both connections idle afterwards — the next pair reuses them with no re-dial.
func TestSynthesize_LookaheadPair_StaysWarm(t *testing.T) {
	srv, conns, _ := connCountingServer(t)
	c := elevenlabs.New("k", elevenlabs.WithBaseURL(srv.URL))

	pair := func() {
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				synthesizeAndDrain(t, c)
			}()
		}
		wg.Wait()
	}
	// A pair that happens not to overlap needs one connection, one that does
	// needs two; with both kept idle no round ever dials a third.
	for range 4 {
		pair()
	}
	if got := conns.Load(); got > 2 {
		t.Errorf("%d connections dialled across look-ahead pairs, want at most 2; idle connections are being dropped", got)
	}
}

// TestSynthesize_LookaheadPairPlusWarm_StaysWarm: the look-ahead pair (#626) can
// overlap a [elevenlabs.Client.Warm] (a speech start or barge lands while both
// sentences stream), so three connections are open at once. The default
// transport must keep all three idle afterwards; the stdlib default of two idle
// per host would close one after every such round and the next round re-dials.
// The server holds each round's requests until all three have arrived, so the
// three genuinely overlap.
func TestSynthesize_LookaheadPairPlusWarm_StaysWarm(t *testing.T) {
	var conns, arrived atomic.Int32
	var mu sync.Mutex
	release := make(chan struct{})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gate := release
		if arrived.Add(1)%3 == 0 {
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
	c := elevenlabs.New("k", elevenlabs.WithBaseURL(srv.URL))

	round := func() {
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				synthesizeAndDrain(t, c)
			}()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := c.Warm(context.Background()); err != nil {
				t.Errorf("Warm: %v", err)
			}
		}()
		wg.Wait()
	}
	round()
	if got := conns.Load(); got != 3 {
		t.Fatalf("connections after the first overlapping round = %d, want 3", got)
	}
	for range 3 {
		round()
	}
	if got := conns.Load(); got != 3 {
		t.Errorf("connections grew from 3 to %d across overlapping rounds; the pool drops idle connections", got)
	}
}
