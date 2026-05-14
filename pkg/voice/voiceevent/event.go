// Package voiceevent defines the shared event taxonomy for the voice pipeline.
//
// Per ADR-0020 the same vocabulary is consumed by two transports: voice tests
// observe events directly via [voicetest.Harness], and the SSE relay forwards
// them to browsers (per ADR-0014). Every event type therefore carries a stable
// wire name via [Event.EventName].
package voiceevent

import (
	"sync"
	"time"
)

// Event is anything the voice pipeline emits onto the shared bus.
//
// Implementations must return a stable, dot-namespaced wire name from
// EventName so it round-trips faithfully across the SSE boundary.
type Event interface {
	EventName() string
}

// VADSpeechStart marks the onset of an utterance as detected by the VAD stage.
type VADSpeechStart struct {
	At          time.Time
	Probability float64
}

// EventName implements [Event].
func (VADSpeechStart) EventName() string { return "vad.speech_start" }

// VADSpeechEnd marks the end of an utterance as detected by the VAD stage:
// the speech-active state has been left because probability stayed below the
// silence threshold for the configured number of consecutive frames.
type VADSpeechEnd struct {
	At          time.Time
	Probability float64
}

// EventName implements [Event].
func (VADSpeechEnd) EventName() string { return "vad.speech_end" }

// STTFinal is an authoritative transcript for one completed utterance, as
// committed by the STT provider. Per ADR-0021 the same event is emitted on
// the cassette-replay and live paths; the orchestrator does not distinguish.
type STTFinal struct {
	At   time.Time
	Text string
}

// EventName implements [Event].
func (STTFinal) EventName() string { return "stt.final" }

// Bus is an in-process pub/sub channel. Subscribers register a callback;
// Publish invokes every callback synchronously in the calling goroutine.
//
// Bus is safe for concurrent use. Callbacks must not block — slow consumers
// (e.g. SSE writers) must do their own buffering.
type Bus struct {
	mu   sync.Mutex
	subs map[*subscription]struct{}
}

type subscription struct {
	fn func(Event)
}

// NewBus returns an empty Bus.
func NewBus() *Bus {
	return &Bus{subs: map[*subscription]struct{}{}}
}

// Publish delivers e to every current subscriber. Subscribers added or removed
// concurrently with Publish either see e or don't, atomically.
func (b *Bus) Publish(e Event) {
	b.mu.Lock()
	fns := make([]func(Event), 0, len(b.subs))
	for s := range b.subs {
		fns = append(fns, s.fn)
	}
	b.mu.Unlock()

	for _, fn := range fns {
		fn(e)
	}
}

// Subscribe registers fn for every subsequent Publish. The returned function
// removes the subscription; calling it more than once is a no-op.
func (b *Bus) Subscribe(fn func(Event)) (unsubscribe func()) {
	s := &subscription{fn: fn}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()

	return func() {
		b.mu.Lock()
		delete(b.subs, s)
		b.mu.Unlock()
	}
}
