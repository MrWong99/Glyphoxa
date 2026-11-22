package wirenpc

import (
	"context"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
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

	unsub := wireTTSWarm(ctx, bus, spy, nil)
	defer unsub()
	expectWarm(t, spy, "when the cycle connects", nil)

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
