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
//
// SpeakerID is the Discord snowflake string of the participant whose Speaker Lane
// detected the onset (ADR-0050), or "" when the transition came off the default
// (unattributed) lane — the single-lane MVP path, byte-identical to before speaker
// lanes existed. Additive per ADR-0039's stated seam.
type VADSpeechStart struct {
	At          time.Time
	Probability float64
	SpeakerID   string
}

// EventName implements [Event].
func (VADSpeechStart) EventName() string { return "vad.speech_start" }

// VADSpeechEnd marks the end of an utterance as detected by the VAD stage:
// the speech-active state has been left because probability stayed below the
// silence threshold for the configured number of consecutive frames.
// SpeakerID is the Discord snowflake string of the participant whose Speaker Lane
// detected the end-of-speech (ADR-0050), or "" for the default lane. NOTE: unlike
// the ADR-0050 enumeration (VADSpeechStart / STTPartial / STTFinal / BargeDetected),
// VADSpeechEnd also carries SpeakerID — lane routing of the bus-driven speech
// transitions needs the speech-end attributed to disarm the right lane's barge
// window and endpoint the right lane. Additive, in the ADR's spirit.
type VADSpeechEnd struct {
	At          time.Time
	Probability float64
	SpeakerID   string
}

// EventName implements [Event].
func (VADSpeechEnd) EventName() string { return "vad.speech_end" }

// STTPartial is the MUTABLE interim hypothesis of the in-progress utterance
// (ADR-0042/0020). Text REPLACES all previous partials for the same UtteranceID —
// it is not cumulative. Only [STTFinal] reaches Address Detection and the
// Transcript (ADR-0012); a partial is a live-view/speculation signal, never
// routed and never persisted.
//
// A partial may be published CONCURRENTLY with other turns' events: it originates
// on the streaming adapter's read goroutine (the [FirstAudio] precedent), so a
// metrics/SSE subscriber may observe it interleaved. Consumers MUST correlate by
// UtteranceID and never assume "latest partial": utterance N+1's partial can
// precede utterance N's STTFinal.
//
// UtteranceID is stamped manager-side from the CURRENT utterance, so for up to
// ~one round-trip after a new speech_start an in-flight partial for the PREVIOUS
// utterance can arrive carrying the new utterance's id (a stale-text partial).
// Speculation consumers must tolerate this: the STTFinal's normalized match
// against the speculated query self-heals it, and a mismatch falls back to inline
// retrieval (ADR-0042).
type STTPartial struct {
	At   time.Time
	Text string
	// UtteranceID is minted at the local VAD speech_start ([NewUtteranceID]) and
	// joins this utterance's partials to the [STTFinal] its manual commit yields.
	UtteranceID string
	// SpeakerID is the Discord snowflake string of the Speaker Lane this partial's
	// stream belongs to (ADR-0050), or "" for the default lane. Stamped by the
	// per-lane [StreamManager] on the partials it publishes.
	SpeakerID string
}

// EventName implements [Event].
func (STTPartial) EventName() string { return "stt.partial" }

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
	// UtteranceID joins this final to the [STTPartial]s of the utterance it came
	// from (ADR-0042), minted at the local VAD speech_start. It is empty on the
	// batch path (no stream, no partials) — the byte-for-byte no-streaming default.
	UtteranceID string
	// SpeakerID is the Discord snowflake string of the Speaker Lane this utterance
	// was segmented on (ADR-0050) — the attribution Address Detection and the
	// Transcript inherit. "" for the default (unattributed) lane, the single-lane
	// MVP path. Stamped by [STT.PublishFinal] on both the batch and streamed-commit
	// paths.
	SpeakerID string
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
	AgentRole string // AgentRoleButler or AgentRoleCharacter
	Name      string
}

// The two Agent Role values an [AddressTarget] may carry (CONTEXT.md "Agent
// Role"). They are the sole valid AgentRole strings: the matchers validate
// against them at construction and the Butler GM-address gate
// ([orchestrator.WithButlerGMGate]) keys off AgentRoleButler, so a mistyped or
// empty role is a wiring error caught loudly rather than a silently disarmed
// gate.
const (
	AgentRoleButler    = "butler"
	AgentRoleCharacter = "character"
)

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

// EnsembleRouted marks the routing decision for one [STTFinal] utterance that
// addressed TWO OR MORE Agents at once (ADR-0024 returns a set when
// address.Config.MaxTargets > 1). It is the atomic Ensemble Turn signal (ADR-0025):
// the address detector publishes exactly ONE EnsembleRouted for the whole set
// instead of N independent [AddressRouted], so the turn-taking layer runs the set
// as ONE floor-holding unit (speculative fan-out + Lead race) rather than N
// contending turns. A single-target utterance still publishes a plain
// [AddressRouted] — the byte-identical MaxTargets=1 default, where the ensemble
// branch is dead code.
//
// Targets are the post-GM-gate survivors in the matcher's score-sorted order
// (Targets[0] is the top-scored), and len(Targets) >= 2 by construction. Text and
// TurnID mirror [AddressRouted]: the utterance text and the correlation id copied
// from the originating [STTFinal] (A3).
type EnsembleRouted struct {
	At      time.Time
	Text    string
	TurnID  string
	Targets []AddressTarget
}

// EventName implements [Event].
func (EnsembleRouted) EventName() string { return "address.ensemble" }

// EnsembleLead marks the Agent the speculative race elected as the Ensemble Turn's
// Lead (ADR-0025, #301): the first candidate to finish a complete, non-empty draft
// takes the floor and speaks under the ensemble's ORIGINAL TurnID. It is published
// the moment the Lead is chosen, before its first sentence dispatches, so the
// transcript relay attributes the turn's line to the winning Agent (exactly as
// [AddressRouted] does for a single-target turn). The losing candidates' drafts are
// discarded (ADR-0012 — a loser commits nothing); their Reactions are #302.
type EnsembleLead struct {
	At     time.Time
	TurnID string
	Target AddressTarget
}

// EventName implements [Event].
func (EnsembleLead) EventName() string { return "ensemble.lead" }

// SpeakRequested marks a GM-puppeteered direct-speech request: the `/say <text>
// as:<agent>` slash command (ADR-0010, #295) asks an Agent to speak Text verbatim,
// bypassing Address Detection and the LLM Replier entirely (ADR-0024 — /say is the
// explicit-target path, so it must NOT publish [AddressRouted], which would trigger
// the LLM Replier). It is the seam between the session Manager (which validates the
// active session + agent and publishes this) and the orchestrator's DirectSpeech
// reactor (which takes the barge-in floor and dispatches Text to TTS in the Agent's
// Voice). Target names the Agent whose Voice speaks; TurnID is minted at publish so
// the resulting [TTSInvoked] / [FirstAudio] / [FirstOpus] and the transcript line
// projection correlate exactly as an LLM turn's do (A3, ADR-0012/0040).
type SpeakRequested struct {
	At     time.Time
	TurnID string
	Target AddressTarget
	Text   string
}

// EventName implements [Event].
func (SpeakRequested) EventName() string { return "speak.requested" }

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
	// never spoken (latency investigation root cause #2). Coalescing is
	// same-target only: a segment routed to a different agent inside the window
	// supersedes instead (#146), so this reason never marks a cross-target
	// address. [TurnEnded.Text] carries its dropped transcript.
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
	// TurnEndMute: a GM muted the Agent, cutting its turn (#211). It is a
	// deliberate control action, DISTINCT from a human [BargeDetected] — CONTEXT.md
	// reserves Barge-in for the human-interrupts-Agent case. Transcript and history
	// commit exactly as a barge would (delivered-sentences-only, ADR-0012), but the
	// cause is a mute, never a barge.
	TurnEndMute TurnEndReason = "mute"
	// TurnEndSpendCap: the session's estimated spend crossed its soft cap, so the
	// replier refused a NEW turn before taking the floor (#130, ADR-0046). Like
	// [TurnEndMute] it is a deliberate policy stop, not a barge or a fault — the turn
	// opened no floor, ran no producer, and produced no audio.
	TurnEndSpendCap TurnEndReason = "spend_cap"
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
// SpeakerID is the Discord snowflake string of the participant who barged — the
// Speaker Lane whose confirmed speech reclaimed the floor (ADR-0050). It is ""
// when the barge came off the default (single, unattributed) lane, the MVP path.
// With per-speaker lanes the barge window is per-speaker, so this names exactly
// who interrupted (needed by Epic 8's detector and future floor rules).
type BargeDetected struct {
	At        time.Time
	SpeakerID string
}

// EventName implements [Event].
func (BargeDetected) EventName() string { return "barge.detected" }

// MuteChanged marks a change to one Agent's mute state in the live Voice Session
// (#211). It is the mute-control event on the shared bus: the session Manager
// publishes it AFTER writing its authoritative mute set, and both surfaces react —
// the orchestrator's mute reactor cuts the floor / gates routes, the wirenpc
// wiring prunes the address matcher, and the SSE relay forwards it to the web
// panel. The ordering is load-bearing: the set is written before the event, so a
// reactor reading the set on this event always sees the change.
//
// Muted=true means the Agent is now muted (produces no audio, no transcript);
// Muted=false restores it. AgentID is the Agent's stable id (a Character NPC's
// primary key, or the Butler's).
type MuteChanged struct {
	At      time.Time
	AgentID string
	Muted   bool
}

// EventName implements [Event].
func (MuteChanged) EventName() string { return "mute.changed" }

// TapeConsentChanged marks a change to one Speaker's rollover-tape consent (#306,
// ADR-0051) in the live Voice Session. The presence layer publishes it AFTER
// persisting the consent row (the [MuteChanged] ordering precedent: the durable
// state is written before the event), and the wirenpc wiring reacts by calling
// tape.SetConsent — arming or clearing that Speaker's lane. Granted=true means the
// Speaker just consented (their audio may now be captured); Granted=false is a
// revoke (future capture stops and their existing ring is cleared).
type TapeConsentChanged struct {
	At         time.Time
	CampaignID string
	SpeakerID  string
	Granted    bool
}

// EventName implements [Event].
func (TapeConsentChanged) EventName() string { return "tape.consent_changed" }

// SpendCapLevel names which per-session spend cap a [SpendCapReached] announces
// (#130, ADR-0046): the soft cap (refuse new turns) or the hard cap (end the
// session). It is a bounded enum, DISTINCT from the [TurnEndReason] a refused turn
// carries — this event is about the session-level cap crossing, not one turn.
type SpendCapLevel string

const (
	// SpendCapSoft: the soft cap was crossed — the Session screen shows the
	// spend-cap-reached state; new Agent turns are refused, in-flight ones finish.
	SpendCapSoft SpendCapLevel = "soft"
	// SpendCapHard: the hard cap was crossed — the session is ending itself cleanly.
	SpendCapHard SpendCapLevel = "hard"
)

// SpendCapReached marks the moment a live Voice Session's estimated spend crossed
// one of its per-Tenant caps (#130, ADR-0046). The session Manager publishes it
// from the spend meter's once-firing callback AFTER it has taken effect (the gate
// already refuses new turns for soft; the session is already ending for hard), so a
// subscriber reacting to it sees a consistent state. The SSE relay forwards it as a
// `spendcap` frame so the Session screen renders which cap tripped and the
// (estimated) spend; the terminal reload truth for a hard-capped session is
// GetSession (status + end_reason).
type SpendCapReached struct {
	At    time.Time
	Level SpendCapLevel
}

// EventName implements [Event].
func (SpendCapReached) EventName() string { return "spend.cap_reached" }

// ConnectionState is the Voice Session's Discord gateway connection lifecycle as
// seen by the web Session screen (#123, ADR-0020). It is a coarse UI/telemetry
// state DISTINCT from the low-level pkg/voice voice.State: this taxonomy lives in
// voiceevent so both the SSE relay and the deferred split-Mode VoiceControlService
// path (ADR-0039) name the same states.
type ConnectionState string

const (
	// ConnectionConnecting: a connect-and-serve cycle has begun but the voice
	// channel join has not yet succeeded.
	ConnectionConnecting ConnectionState = "connecting"
	// ConnectionConnected: the Bot joined the voice channel and is serving.
	ConnectionConnected ConnectionState = "connected"
	// ConnectionFailed: the session hit a FATAL, non-retryable gateway rejection
	// (invalid Bot token, disallowed intents, …) and reached its terminal failed
	// state — the loop stops retrying rather than backing off forever.
	ConnectionFailed ConnectionState = "failed"
)

// ConnectionStateChanged marks a transition of the Voice Session's gateway
// connection state (#123). connectAndServe publishes {connecting} at the start of
// each cycle and {connected} once the join succeeds; Run publishes {failed} with a
// readable Detail when the loop returns a fatal classification. Detail carries the
// [wirenpc.FatalError.Error] prose on a failed transition and is empty otherwise.
// The SSE relay forwards it to the live Session screen (ADR-0014 Hop-B).
type ConnectionStateChanged struct {
	At     time.Time
	State  ConnectionState
	Detail string
}

// EventName implements [Event].
func (ConnectionStateChanged) EventName() string { return "connection.state" }

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
