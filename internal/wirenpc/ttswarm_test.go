package wirenpc

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	ttseleven "github.com/MrWong99/Glyphoxa/pkg/voice/tts/elevenlabs"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/wire"
)

// warmSpy counts Warm calls, signalling each on calls.
type warmSpy struct{ calls chan struct{} }

func (w *warmSpy) Warm(context.Context) error {
	w.calls <- struct{}{}
	return nil
}

// expectWarm waits for one Warm, re-firing trigger (when non-nil) until it lands:
// the wiring single-flights, so a trigger racing the previous warm's completion
// is legitimately dropped and a real trigger simply comes again.
func expectWarm(t *testing.T, w *warmSpy, why string, trigger func()) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if trigger != nil {
			trigger()
		}
		select {
		case <-w.calls:
			return
		case <-deadline:
			t.Fatalf("no TTS Warm %s", why)
		case <-tick.C:
		}
	}
}

// drainWarms waits out and discards any Warm still landing from earlier triggers.
func drainWarms(w *warmSpy) {
	for {
		select {
		case <-w.calls:
		case <-time.After(50 * time.Millisecond):
			return
		}
	}
}

// TestWireTTSWarm_WarmsOnConnectSpeechAndBarge is the #702 pre-warm wiring bar:
// the synthesis connection is (re)opened when the cycle connects, when a
// participant starts speaking (the reply's first sentence follows within the
// STT+LLM time) and after a barge (which tore down the connection a mid-stream
// sentence was using), so a reply's first sentence does not pay TCP+TLS.
func TestWireTTSWarm_WarmsOnConnectSpeechAndBarge(t *testing.T) {
	bus := voiceevent.NewBus()
	spy := &warmSpy{calls: make(chan struct{}, 8)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	clk := &warmClock{now: time.Unix(1_000_000, 0)}
	unsub := wireTTSWarmClock(ctx, bus, spy, nil, clk.Now)
	defer unsub()
	expectWarm(t, spy, "when the cycle connects", nil)

	clk.Advance(ttsWarmQuiet) // past the speech-start quiet window
	expectWarm(t, spy, "on VADSpeechStart", func() { bus.Publish(voiceevent.VADSpeechStart{At: time.Now()}) })
	drainWarms(spy)
	expectWarm(t, spy, "on BargeDetected", func() { bus.Publish(voiceevent.BargeDetected{At: time.Now()}) })
	drainWarms(spy)

	// Unrelated events do not warm.
	bus.Publish(voiceevent.TTSInvoked{At: time.Now()})
	select {
	case <-spy.calls:
		t.Fatal("Warm fired on an unrelated event")
	case <-time.After(50 * time.Millisecond):
	}

	// After unsubscribe nothing warms.
	unsub()
	bus.Publish(voiceevent.BargeDetected{At: time.Now()})
	select {
	case <-spy.calls:
		t.Fatal("Warm fired after unsubscribe")
	case <-time.After(50 * time.Millisecond):
	}
}

// blockingWarmer blocks every Warm until release closes, counting entries.
type blockingWarmer struct {
	entered chan struct{}
	release chan struct{}
}

func (b *blockingWarmer) Warm(ctx context.Context) error {
	b.entered <- struct{}{}
	select {
	case <-b.release:
	case <-ctx.Done():
	}
	return nil
}

// TestWireTTSWarm_SingleFlight: a burst of triggers while a warm is in flight
// (speech starts arrive per utterance and per lane) does not stack requests —
// one warm at a time, and the publisher (the bus, on the audio path) never blocks.
func TestWireTTSWarm_SingleFlight(t *testing.T) {
	bus := voiceevent.NewBus()
	bw := &blockingWarmer{entered: make(chan struct{}, 16), release: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer wireTTSWarm(ctx, bus, bw, nil)()

	<-bw.entered // the connect warm, now held in flight
	done := make(chan struct{})
	go func() {
		for range 10 {
			bus.Publish(voiceevent.VADSpeechStart{At: time.Now()})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked behind an in-flight Warm")
	}
	select {
	case <-bw.entered:
		t.Fatal("a second Warm started while one was in flight")
	case <-time.After(50 * time.Millisecond):
	}
	close(bw.release)
}

// warmClock is a settable clock for the speech-start quiet window.
type warmClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *warmClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *warmClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// expectNoWarm publishes ev and asserts no Warm lands.
func expectNoWarm(t *testing.T, bus *voiceevent.Bus, w *warmSpy, why string, ev voiceevent.Event) {
	t.Helper()
	bus.Publish(ev)
	select {
	case <-w.calls:
		t.Fatalf("Warm fired %s", why)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestWireTTSWarm_SpeechStartSkipsWhilePoolRecentlyActive: a speech start only
// warms when the pool has been quiet for the window. Inside it a warm or a
// synthesis (FirstAudio) already left a live connection, and an unthrottled HEAD
// per utterance start could hold the one idle connection the reply's first
// sentence is about to need (~140 ms HEAD round trip vs ~55-80 ms dial). Connect
// and barge triggers stay unconditional.
func TestWireTTSWarm_SpeechStartSkipsWhilePoolRecentlyActive(t *testing.T) {
	bus := voiceevent.NewBus()
	spy := &warmSpy{calls: make(chan struct{}, 8)}
	clk := &warmClock{now: time.Unix(1_000_000, 0)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer wireTTSWarmClock(ctx, bus, spy, nil, clk.Now)()
	expectWarm(t, spy, "when the cycle connects", nil)
	drainWarms(spy)

	clk.Advance(time.Second)
	expectNoWarm(t, bus, spy, "on a speech start 1 s after the connect warm", voiceevent.VADSpeechStart{At: clk.Now()})

	clk.Advance(ttsWarmQuiet)
	bus.Publish(voiceevent.FirstAudio{At: clk.Now()}) // a sentence is streaming: pool active
	clk.Advance(ttsWarmQuiet / 2)
	expectNoWarm(t, bus, spy, "on a speech start inside the window after a synthesis", voiceevent.VADSpeechStart{At: clk.Now()})

	clk.Advance(ttsWarmQuiet)
	expectWarm(t, spy, "on a speech start after a quiet window", func() { bus.Publish(voiceevent.VADSpeechStart{At: clk.Now()}) })
	drainWarms(spy)

	// Barge stays unconditional, even right after a warm.
	expectWarm(t, spy, "on BargeDetected inside the window", func() { bus.Publish(voiceevent.BargeDetected{At: clk.Now()}) })
}

// failingWarmSpy signals each Warm and fails it.
type failingWarmSpy struct{ calls chan struct{} }

func (w *failingWarmSpy) Warm(context.Context) error {
	w.calls <- struct{}{}
	return errors.New("dial refused")
}

// TestWireTTSWarm_FailedWarmDoesNotCountAsPoolActivity: a warm that failed left
// no connection behind, so the next speech start must warm again rather than
// sit out the quiet window.
func TestWireTTSWarm_FailedWarmDoesNotCountAsPoolActivity(t *testing.T) {
	bus := voiceevent.NewBus()
	spy := &failingWarmSpy{calls: make(chan struct{}, 64)}
	clk := &warmClock{now: time.Unix(1_000_000, 0)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer wireTTSWarmClock(ctx, bus, spy, nil, clk.Now)()
	<-spy.calls // the connect warm, failing

	clk.Advance(time.Second)
	expectWarm(t, (*warmSpy)(spy), "on a speech start right after a failed warm", func() {
		bus.Publish(voiceevent.VADSpeechStart{At: clk.Now()})
	})
}

// TestNewCycleSynth_WarmsTheClientItBuilds pins the production wiring: the one
// constructor connectAndServe gets its outbound synthesizer from also wires the
// pre-warm onto the very client it built, so a refactor cannot keep the
// synthesizer and silently drop the warm. The newTTS seam points the client at
// a server that records the warm's HEAD.
func TestNewCycleSynth_WarmsTheClientItBuilds(t *testing.T) {
	heads := make(chan struct{}, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			heads <- struct{}{}
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	orig := newTTS
	t.Cleanup(func() { newTTS = orig })
	newTTS = func(apiKey string, opts ...ttseleven.Option) *ttseleven.Client {
		return orig(apiKey, append(opts, ttseleven.WithBaseURL(srv.URL))...)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := wire.PlaybackSinkFunc(func(context.Context, <-chan tts.AudioChunk) {})
	synth, unwire := newCycleSynth(ctx, voiceevent.NewBus(), Config{keys: providerKeys{tts: "T-tts-key"}}, sink, nil)
	defer unwire()
	if synth == nil {
		t.Fatal("newCycleSynth returned no synthesizer")
	}
	select {
	case <-heads:
	case <-time.After(2 * time.Second):
		t.Fatal("the cycle's TTS client was never warmed on connect")
	}
}

// TestNewCycleSynth_TransportReachesTheClientAndReleaseClosesIt pins the #702
// transport wiring end to end: the transport the composition root parsed from
// GLYPHOXA_TTS_TRANSPORT reaches the cycle's client (a v4 Turbo sentence rides a
// Text-to-Dialogue websocket with "ws", a POST with the default), and the
// release func the cycle defers closes the client, so a reconnect cycle cannot
// leave a dialogue socket — one of the account's dialogue-session slots — open.
func TestNewCycleSynth_TransportReachesTheClientAndReleaseClosesIt(t *testing.T) {
	for _, tc := range []struct {
		transport   ttseleven.Transport
		wantSockets int64
		wantPosts   int64
	}{
		{ttseleven.TransportHTTP, 0, 1},
		{ttseleven.TransportWS, 1, 0},
	} {
		t.Run(string(tc.transport), func(t *testing.T) {
			var sockets, open, posts, heads atomic.Int64
			up := websocket.Upgrader{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !websocket.IsWebSocketUpgrade(r) {
					switch r.Method {
					case http.MethodPost:
						posts.Add(1)
						_, _ = w.Write(make([]byte, 64))
					case http.MethodHead:
						heads.Add(1)
					}
					return
				}
				conn, err := up.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				sockets.Add(1)
				open.Add(1)
				defer open.Add(-1)
				for n := 0; ; n++ {
					if _, _, err := conn.ReadMessage(); err != nil {
						return
					}
					if n == 0 {
						continue // the voices registration
					}
					_ = conn.WriteJSON(map[string]any{"audio": "AAAAAA=="})
					_ = conn.WriteJSON(map[string]any{"is_final_audio_for_turn": true})
				}
			}))
			defer srv.Close()
			orig := newTTS
			t.Cleanup(func() { newTTS = orig })
			newTTS = func(apiKey string, opts ...ttseleven.Option) *ttseleven.Client {
				return orig(apiKey, append(opts, ttseleven.WithBaseURL(srv.URL))...)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sink := wire.PlaybackSinkFunc(func(_ context.Context, chunks <-chan tts.AudioChunk) {
				go func() {
					for range chunks {
					}
				}()
			})
			// The constructor takes the session Config whole — the transport cannot
			// be dropped on the way from Config to the client — and says in the log
			// which transport the cycle speaks on, so a dead knob shows in prod.
			var logs bytes.Buffer
			cfg := Config{TTSTransport: tc.transport, keys: providerKeys{tts: "T-tts-key"}}
			synth, release := newCycleSynth(ctx, voiceevent.NewBus(), cfg, sink, slog.New(slog.NewTextHandler(&logs, nil)))
			if want := "transport=" + string(tc.transport); !strings.Contains(logs.String(), want) {
				t.Errorf("cycle log = %q, want it to name %s", logs.String(), want)
			}
			// The connect warm runs on its own goroutine. Let it land first: a warm
			// that overlaps the sentence finds the voice's only socket leased and
			// (rightly) lines up a second one, which is not what this test counts.
			for deadline := time.Now().Add(2 * time.Second); heads.Load() == 0; time.Sleep(time.Millisecond) {
				if time.Now().After(deadline) {
					release()
					t.Fatal("the connect warm never reached the server")
				}
			}
			ch, err := synth.Synthesize(ctx, tts.SynthesizeRequest{Sentence: "Willkommen.", Voice: ttseleven.DefaultVoice("v", "de")})
			if err != nil {
				release()
				t.Fatalf("Synthesize: %v", err)
			}
			for range ch {
			}
			if sockets.Load() != tc.wantSockets || posts.Load() != tc.wantPosts {
				t.Errorf("sockets = %d, POSTs = %d; want %d and %d", sockets.Load(), posts.Load(), tc.wantSockets, tc.wantPosts)
			}
			release()
			deadline := time.Now().Add(2 * time.Second)
			for open.Load() != 0 {
				if time.Now().After(deadline) {
					t.Fatalf("%d dialogue socket(s) still open after the cycle released its synthesizer", open.Load())
				}
				time.Sleep(2 * time.Millisecond)
			}
		})
	}
}
