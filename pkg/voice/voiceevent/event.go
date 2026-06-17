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
//
// TurnID is the per-turn correlation id (A3): it originates here, at the start
// of a turn, and propagates through [AddressRouted] → [TTSInvoked] →
// [FirstAudio] so one turn's stage spans join up. It is a log/exemplar
// correlation id only — never a metric label (ADR-0032 §2.1).
//
// SpeechEndAt is the [VADSpeechEnd.At] of the utterance this transcript came
// from, carried forward so the headline response-latency span
// (speech-end → first audio) is self-contained per TurnID — the metrics
// subscriber need not guess which speech-end belongs to this turn under
// concurrent speech. Zero when the utterance was flushed without a speech-end
// transition (end-of-stream).
type STTFinal struct {
	At          time.Time
	Text        string
	TurnID      string
	SpeechEndAt time.Time
}

// EventName implements [Event].
func (STTFinal) EventName() string { return "stt.final" }

// AddressTarget identifies the Agent the address detector selected for one
// utterance — the Tenant's Butler or one of the Campaign's Character NPCs
// per CONTEXT.md ("Address Detection", "Agent Role").
//
// AgentID is the stable identifier downstream stages (Hot Context assembly,
// Persona injection, LLM dispatch) use to look up the Agent record. The
// well-known value "butler" is reserved for the Butler default route;
// Character NPCs carry their Agent record's primary key. Name is the
// human-readable display name ("Butler", "Bart") — preserved on the wire
// for SSE consumers and test diagnostics, but not load-bearing for routing.
type AddressTarget struct {
	AgentID   string
	AgentRole string // "butler" or "character"
	Name      string
}

// AddressRouted marks the routing decision for one [STTFinal] utterance.
//
// Per CONTEXT.md the address detector picks exactly one Agent per utterance:
// a Character NPC if the speaker named one, otherwise the Butler. The
// algorithm choice (regex / LLM judge / two-stage / v1 cherry-pick) is
// Q13.4-open in DESIGN.md; this event pins only the resulting decision so
// downstream stages can consume it without depending on the algorithm.
//
// Text carries the utterance text the detector was asked to route, so
// downstream consumers (Hot Context, SSE relay) do not need to re-correlate
// against the originating STTFinal.
type AddressRouted struct {
	At     time.Time
	Text   string
	Target AddressTarget
	// TurnID is the correlation id copied from the [STTFinal] this routing
	// decision answers (A3); see [STTFinal.TurnID].
	TurnID string
}

// EventName implements [Event].
func (AddressRouted) EventName() string { return "address.routed" }

// TTSInvoked marks the dispatch of one sentence to the TTS stage.
//
// Per ADR-0021's TTS cassette policy the observable contract for TTS is "the
// provider was invoked with sentence N" — synthesized audio is not fed back to
// tests. The orchestrator publishes this event when it hands the sentence to the
// underlying [tts.Synthesizer], BEFORE the Synthesize call returns — so a sentence
// whose Synthesize start-errors (empty VoiceID, auth failure) still emits
// TTSInvoked, the invoked-but-never-spoke signal (#20). It announces the dispatch
// attempt, not a success: whether the sentence was actually spoken is signalled by
// [FirstAudio], not here.
//
// Index is 0-based within the current turn and increments per dispatch attempt on
// the same stage.
type TTSInvoked struct {
	At       time.Time
	Sentence string
	Index    int
	// TurnID is the correlation id of the turn this sentence belongs to (A3),
	// threaded from the reply reactor; see [STTFinal.TurnID].
	TurnID string
}

// EventName implements [Event].
func (TTSInvoked) EventName() string { return "tts.invoked" }

// FirstAudio marks the moment the first synthesized [tts.AudioChunk] of a
// sentence crosses the TeeSynthesizer→PlaybackPump boundary — "first audio handed
// to the pump" (A3 hook 1). It is published by the wire tee, off its forward
// goroutine, so a metrics subscriber may receive it concurrently with other turns
// and must lock its per-turn state.
//
// It is NO LONGER the headline response-latency boundary — that is [FirstOpus],
// the audible-on-wire moment. FirstAudio still owns two things: the per-sentence
// tts_ttfb pairing ([TTSInvoked]↔FirstAudio by arrival order within a TurnID), and
// the turn-lifecycle success signal ("this turn produced audio", which gates the
// abandoned outcome). There is no sentence index: the metrics subscriber keys on
// TurnID and uses the FIRST FirstAudio per turn for the success signal.
type FirstAudio struct {
	At     time.Time
	TurnID string
}

// EventName implements [Event].
func (FirstAudio) EventName() string { return "voice.first_audio" }

// FirstOpus marks the moment the FIRST Opus packet of a turn is pulled from the
// playback [voice.Source] by disgo's sender to be streamed to Discord — the
// audible-on-wire boundary. It is the END of the headline response-latency SLO
// per Luk's definition ("I stop talking until the first TTS opus packets are
// streamed back to Discord"): strictly later than [FirstAudio] (handed-to-pump),
// it includes the codec encode and the pump's real-time pacing that FirstAudio
// excludes, so the span finally measures what the user experiences.
//
// Published once per turn by the wire playback path's Source decorator on the
// first non-EOF frame it yields. It runs on disgo's sender goroutine, so a
// metrics subscriber may receive it concurrently and must lock its per-turn
// state. A turn whose audio is barge-cancelled before any frame reaches the wire
// never emits it (correctly: nothing was audible).
type FirstOpus struct {
	At     time.Time
	TurnID string
}

// EventName implements [Event].
func (FirstOpus) EventName() string { return "voice.first_opus" }

// TurnEndReason is the bounded cause a turn ended without (or after) audio,
// carried on [TurnEnded]. It is published by the seam that KNOWS the cause — the
// only place the precise reason is available — so the metrics subscriber records
// it instead of guessing. It is a log/exemplar value AND maps to the bounded
// metric reason label (ADR-0032 §2.1): keep this set small.
type TurnEndReason string

const (
	// TurnEndSupersedeCoalesced: the floor's same-utterance grace window folded a
	// late VAD-split segment into the turn already speaking — the late segment is
	// never spoken (latency investigation root cause #2). [TurnEnded.Text] carries
	// its dropped transcript.
	TurnEndSupersedeCoalesced TurnEndReason = "supersede_coalesced"
	// TurnEndBarge: a confirmed human barge-in cancelled the turn (the floor was
	// yielded while this turn held it).
	TurnEndBarge TurnEndReason = "barge"
	// TurnEndTTSError: the turn's TTS synthesis failed (a real provider/synth error,
	// not a context cancel).
	TurnEndTTSError TurnEndReason = "tts_error"
	// TurnEndProviderError: the reply producer (LLM round/tool loop) failed before
	// the turn could produce audio.
	TurnEndProviderError TurnEndReason = "provider_error"
)

// TurnEnded marks a turn that ended for a known reason — distinct from a turn
// that simply vanished (reaped by the metrics TTL sweep with no signal). It
// carries the turn's TurnID and the precise [TurnEndReason] so the metrics
// subscriber records WHY a turn died (barge vs supersede vs tts/provider error)
// rather than the coarse "no first audio" catch-all. Text is the dropped
// transcript, set only for [TurnEndSupersedeCoalesced]; empty otherwise.
//
// Published by the seam that knows the cause: the [orchestrator.Replier]
// (supersede-coalesced, tts/provider error) and the [orchestrator.BargeIn]
// (barge). The subscriber treats first-audio as terminal, so a TurnEnded arriving
// AFTER first audio (e.g. a barge mid-playback) is a normal interruption and does
// not re-count the turn.
type TurnEnded struct {
	At     time.Time
	TurnID string
	Reason TurnEndReason
	Text   string
}

// EventName implements [Event].
func (TurnEnded) EventName() string { return "turn.ended" }

// BargeDetected marks a confirmed human barge-in: a participant reclaimed the
// floor while an Agent was speaking, so the Agent's turn was torn down (ADR-0027).
// It is the observability signal for a yield that actually cancelled an active
// turn — speech that finds no Agent speaking does not emit it.
//
// Per-participant attribution (interrupted_by_user_id) is deferred until the VAD
// stage republishes per-participant speech events (ADR-0019); this slice runs a
// single VAD session, so the event carries only the moment of the cut.
type BargeDetected struct {
	At time.Time
}

// EventName implements [Event].
func (BargeDetected) EventName() string { return "barge.detected" }

// Bus is an in-process pub/sub channel. Subscribers register a callback;
// Publish invokes every callback synchronously in the calling goroutine.
//
// Delivery guarantees:
//   - Synchronous: Publish returns only after every callback has run.
//   - Ordered: callbacks run in subscription order (the order Subscribe was
//     called), so a deterministic pipeline stays deterministic — the same
//     value the [Glyphoxa address matcher] is built around. Tests and the SSE
//     relay therefore observe a stable fan-out order.
//   - Re-entrant: a callback may itself call Publish; the nested delivery runs
//     to completion (depth-first) before the outer fan-out continues. Note this
//     means a subscriber listening to several event types can observe a caused
//     event (e.g. AddressRouted) before the outer cause (STTFinal) finishes
//     fanning out.
//   - Snapshot: the subscriber set is snapshotted under lock at the start of
//     each Publish. A subscriber added or removed concurrently with — or from
//     inside — a Publish either sees that event or doesn't, atomically; one
//     removed mid-fan-out still receives the in-flight event.
//
// Bus is safe for concurrent use. Callbacks must not block — slow consumers
// (e.g. SSE writers) must do their own buffering — and must not panic: a panic
// propagates to the publisher and aborts delivery to the remaining subscribers.
//
// [Glyphoxa address matcher]: github.com/MrWong99/Glyphoxa/pkg/voice/address
type Bus struct {
	mu   sync.Mutex
	subs []*subscription // subscribers in registration order; unsubscribe compacts
}

type subscription struct {
	fn func(Event)
}

// NewBus returns an empty Bus.
func NewBus() *Bus {
	return &Bus{}
}

// Publish delivers e to every current subscriber, in subscription order, in the
// calling goroutine. See [Bus] for the full delivery contract.
func (b *Bus) Publish(e Event) {
	b.mu.Lock()
	fns := make([]func(Event), len(b.subs))
	for i, s := range b.subs {
		fns[i] = s.fn
	}
	b.mu.Unlock()

	for _, fn := range fns {
		fn(e)
	}
}

// Subscribe registers fn for every subsequent Publish, after any
// already-registered subscribers. The returned function removes the
// subscription; calling it more than once is a no-op.
func (b *Bus) Subscribe(fn func(Event)) (unsubscribe func()) {
	s := &subscription{fn: fn}
	b.mu.Lock()
	b.subs = append(b.subs, s)
	b.mu.Unlock()

	return func() {
		b.mu.Lock()
		for i, cur := range b.subs {
			if cur == s {
				// Compact in place; Publish has already copied the fn values it
				// is mid-delivery on, so this never disturbs an in-flight fan-out.
				b.subs = append(b.subs[:i], b.subs[i+1:]...)
				break
			}
		}
		b.mu.Unlock()
	}
}

// On registers fn for every subsequent Publish of an event whose concrete type
// is E, narrowing the bus's untyped delivery to a single event type. Events of
// any other type are ignored. The returned function removes the subscription;
// calling it more than once is a no-op.
//
// On is the typed building block the orchestrator's reactive wiring is composed
// from: it replaces the switch-on-e.(type) a raw [Bus.Subscribe] callback would
// otherwise spell out, the same way one net/http handler binds one route.
func On[E Event](bus *Bus, fn func(E)) (unsubscribe func()) {
	return bus.Subscribe(func(e Event) {
		if typed, ok := e.(E); ok {
			fn(typed)
		}
	})
}
