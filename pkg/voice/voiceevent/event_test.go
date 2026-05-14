package voiceevent

import (
	"sync"
	"testing"
)

func TestVADSpeechStart_EventName(t *testing.T) {
	t.Parallel()

	got := VADSpeechStart{}.EventName()
	if got != "vad.speech_start" {
		t.Errorf("EventName = %q, want %q", got, "vad.speech_start")
	}
}

func TestVADSpeechEnd_EventName(t *testing.T) {
	t.Parallel()

	got := VADSpeechEnd{}.EventName()
	if got != "vad.speech_end" {
		t.Errorf("EventName = %q, want %q", got, "vad.speech_end")
	}
}

func TestSTTFinal_EventName(t *testing.T) {
	t.Parallel()

	got := STTFinal{}.EventName()
	if got != "stt.final" {
		t.Errorf("EventName = %q, want %q", got, "stt.final")
	}
}

func TestTTSInvoked_EventName(t *testing.T) {
	t.Parallel()

	got := TTSInvoked{}.EventName()
	if got != "tts.invoked" {
		t.Errorf("EventName = %q, want %q", got, "tts.invoked")
	}
}

func TestBus_PublishDeliversToSubscriber(t *testing.T) {
	t.Parallel()

	bus := NewBus()
	var got []Event
	unsub := bus.Subscribe(func(e Event) { got = append(got, e) })
	t.Cleanup(unsub)

	bus.Publish(VADSpeechStart{Probability: 0.9})

	if len(got) != 1 {
		t.Fatalf("subscriber received %d events, want 1", len(got))
	}
	if got[0].EventName() != "vad.speech_start" {
		t.Errorf("EventName = %q, want %q", got[0].EventName(), "vad.speech_start")
	}
}

func TestBus_UnsubscribeStopsDelivery(t *testing.T) {
	t.Parallel()

	bus := NewBus()
	var got []Event
	unsub := bus.Subscribe(func(e Event) { got = append(got, e) })

	bus.Publish(VADSpeechStart{Probability: 0.9})
	unsub()
	bus.Publish(VADSpeechStart{Probability: 0.9})

	if len(got) != 1 {
		t.Errorf("after unsubscribe got %d events, want 1", len(got))
	}
}

func TestBus_PublishFansOutToMultipleSubscribers(t *testing.T) {
	t.Parallel()

	bus := NewBus()
	var a, b int
	t.Cleanup(bus.Subscribe(func(Event) { a++ }))
	t.Cleanup(bus.Subscribe(func(Event) { b++ }))

	bus.Publish(VADSpeechStart{})
	bus.Publish(VADSpeechStart{})

	if a != 2 || b != 2 {
		t.Errorf("fan-out counts: a=%d b=%d, want 2,2", a, b)
	}
}

func TestBus_ConcurrentPublishAndSubscribe(t *testing.T) {
	t.Parallel()

	bus := NewBus()
	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Publisher goroutine.
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
				bus.Publish(VADSpeechStart{})
			}
		}
	})

	// Repeatedly subscribe and unsubscribe — the race detector catches
	// any unsynchronised access to the subs map.
	for range 100 {
		var n int
		var mu sync.Mutex
		unsub := bus.Subscribe(func(Event) {
			mu.Lock()
			n++
			mu.Unlock()
		})
		unsub()
	}
	close(stop)
	wg.Wait()
}
