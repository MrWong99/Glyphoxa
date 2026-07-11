package observe

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// StageSubscriber is the orchestrator-side metrics sibling: it subscribes to the
// voiceevent bus and drives the latency histograms on a [StageRecorder] for the
// spans whose BOTH endpoints are bus events joinable by TurnID (A2). Spans with
// an off-bus endpoint are recorded at their own seam, NOT here:
//   - llm_round / provider_calls / provider_errors → agenttool provider adapter
//   - stt_request → the STT adapter; tts_total → no "tts done" event; codec_* → wire.
//
// It keys per-turn timing state by TurnID so concurrent/overlapping turns never
// cross-attribute (the WithBargeIn(0) reply runs on its own goroutine, and
// [voiceevent.FirstAudio] is published off the tee's forward goroutine — so
// deliveries race and the state is mutex-guarded).
//
// Spans it owns:
//   - response_latency = first FirstAudio per TurnID − STTFinal.SpeechEndAt
//     (skipped when SpeechEndAt is zero: an end-of-stream flush with no
//     speech-end transition has no valid span start — recording it would push a
//     decades-long delta into +Inf and wreck the headline p95).
//   - address_detect   = AddressRouted.At − STTFinal.At
//   - tts_ttfb (per sentence) = FirstAudio[i].At − TTSInvoked[i].At, paired by
//     arrival order within a TurnID (dispatch is sequential, so the interleave is
//     clean: TTSInvoked0, FirstAudio0, TTSInvoked1, FirstAudio1, …).
//
// Barge-in needs no handling here: a turn cancelled before its first audio simply
// never emits a FirstAudio, so it records no response_latency sample (the correct
// "cancelled = no audible response" behaviour); a turn cut after first audio
// already recorded its sample correctly. Dangling state from such turns is reaped
// by [StageSubscriber.Sweep] (TTL), not by a barge signal.
type StageSubscriber struct {
	rec StageRecorder
	now func() time.Time
	ttl time.Duration
	log *slog.Logger

	mu    sync.Mutex
	turns map[string]*turnState
}

// turnState is the per-TurnID timing state, opened at STTFinal and closed when
// the headline sample lands (or reaped by TTL). lastSeen drives expiry.
type turnState struct {
	speechEndAt    time.Time // STTFinal.SpeechEndAt; zero ⇒ no headline sample
	sttFinalAt     time.Time
	routedAt       time.Time // AddressRouted.At; zero until routed (for the turn-end log)
	firstAudioAt   time.Time // first FirstAudio.At (handed-to-pump); zero ⇒ never reached audio
	firstOpusAt    time.Time // first FirstOpus.At (audible-on-wire, the SLO end); zero ⇒ never reached the wire
	role           AgentRole
	roleKnown      bool // set at AddressRouted; gates the headline (needs the role label)
	firstAudioDone bool // first FirstAudio consumed (success outcome + firstAudioAt)
	latencyDone    bool // first FirstOpus consumed for the response_latency SLO sample
	outcomeDone    bool // terminal TurnOutcome already recorded (first_audio/yielded); blocks a double-count at reap

	yieldedText string // TurnYielded.Text: the dropped late-segment transcript (logged at turn-end)

	// pendingTTS is the MOST-RECENT unmatched TTSInvoked time awaiting its
	// FirstAudio for per-sentence tts_ttfb (zero = none pending). We keep only the
	// latest, not a FIFO queue: dispatch is serial within a turn (the producer
	// blocks on TTS.Dispatch, which drains the synthesis before the next sentence
	// — orchestrator tts.go), so the order is strictly TTSInvoked_i ≺ FirstAudio_i
	// ≺ TTSInvoked_{i+1}. A TTSInvoked left unmatched when the next one arrives was
	// a zero-chunk or start-errored synthesis (TTSInvoked is published per dispatch
	// attempt, FirstAudio only on a chunk) that will never get a FirstAudio — so the
	// correct match for any FirstAudio is always the latest invoke, and FIFO-front
	// would mispair it against the stale unmatched one. (Revisit if Ensemble Turns
	// make dispatch concurrent — tts.go anticipates that.)
	pendingTTS time.Time

	lastSeen time.Time
}

// defaultTurnTTL bounds how long a turn's state lives without progress before
// [StageSubscriber.Sweep] reaps it — long enough to outlast a slow LLM + TTS turn
// (the SLO tail is seconds), short enough that abandoned turns (barged, errored,
// or never-synthesized) don't accumulate.
const defaultTurnTTL = 60 * time.Second

// NewStageSubscriber builds a subscriber recording onto rec. A nil rec is
// replaced with [Discard] so it is always safe to construct. opts configure
// optional behaviour (e.g. [WithTurnLog]).
func NewStageSubscriber(rec StageRecorder, opts ...StageSubscriberOption) *StageSubscriber {
	if rec == nil {
		rec = Discard{}
	}
	s := &StageSubscriber{
		rec:   rec,
		now:   time.Now,
		ttl:   defaultTurnTTL,
		log:   slog.New(slog.DiscardHandler),
		turns: make(map[string]*turnState),
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// StageSubscriberOption configures a [StageSubscriber] at construction.
type StageSubscriberOption func(*StageSubscriber)

// WithTurnLog enables the one-line-per-turn structured INFO timeline log
// (turn_id, speech_end → routed → first_audio, or the abandoned reap), the
// per-turn timing trace Sprint-2 log cleanup removed. A 20s wait is then
// debuggable from the logs even when the survivorship-biased response_latency
// histogram shows nothing for the failed turns. A nil logger disables it (the
// default). It is one line per turn-end, not the old per-call-site noise.
func WithTurnLog(log *slog.Logger) StageSubscriberOption {
	return func(s *StageSubscriber) {
		if log == nil {
			log = slog.New(slog.DiscardHandler)
		}
		s.log = log
	}
}

// Subscribe wires the subscriber onto bus and returns the unsubscribe function.
// Call it once from the run wiring (buildConversation); the returned teardown
// detaches every handler. Events without a TurnID (the unkeyed test/non-barge
// path) are ignored for the turn-keyed spans.
func (s *StageSubscriber) Subscribe(bus *voiceevent.Bus) (unsubscribe func()) {
	unsubs := []func(){
		voiceevent.On(bus, s.onSTTFinal),
		voiceevent.On(bus, s.onAddressRouted),
		voiceevent.On(bus, s.onEnsembleRouted),
		voiceevent.On(bus, s.onTTSInvoked),
		voiceevent.On(bus, s.onFirstAudio),
		voiceevent.On(bus, s.onFirstOpus),
		voiceevent.On(bus, s.onTurnEnded),
	}
	return func() {
		for _, u := range unsubs {
			u()
		}
	}
}

func (s *StageSubscriber) onSTTFinal(e voiceevent.STTFinal) {
	if e.TurnID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.turns[e.TurnID]
	if t == nil {
		t = &turnState{}
		s.turns[e.TurnID] = t
	}
	t.speechEndAt = e.SpeechEndAt
	t.sttFinalAt = e.At
	t.lastSeen = s.now()
}

func (s *StageSubscriber) onAddressRouted(e voiceevent.AddressRouted) {
	if e.TurnID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.turns[e.TurnID]
	if t == nil {
		// AddressRouted before STTFinal shouldn't happen, but tolerate it.
		t = &turnState{}
		s.turns[e.TurnID] = t
	}
	t.role = normalizeRole(e.Target.AgentRole)
	t.roleKnown = true
	t.routedAt = e.At
	t.lastSeen = s.now()

	// address_detect = route decision − transcript. Only when we have the
	// STTFinal anchor (sttFinalAt set); otherwise we can't compute the span.
	if !t.sttFinalAt.IsZero() {
		s.rec.AddressDetect(e.At.Sub(t.sttFinalAt))
	}
}

// onEnsembleRouted stage-marks an Ensemble Turn's routing decision exactly like
// onAddressRouted (#301): the ensemble opens ONE turn under its TurnID, so the
// role (Targets[0], the top-scored/eventual coalesce anchor) and the address_detect
// span are recorded the same way a single-target route is. The elected Lead speaks
// under this same TurnID, so its FirstOpus/latency pair against this mark.
func (s *StageSubscriber) onEnsembleRouted(e voiceevent.EnsembleRouted) {
	if e.TurnID == "" || len(e.Targets) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.turns[e.TurnID]
	if t == nil {
		t = &turnState{}
		s.turns[e.TurnID] = t
	}
	// TODO(#301): the role label uses Targets[0].AgentRole (the top-scored coalesce
	// anchor), which may not match the eventually-elected Lead's AgentRole. It is a
	// metrics label only (ADR-0032 §2.1 — never a per-turn identity), and an
	// ensemble's candidates share the character role in practice, so the approximation
	// is harmless; revisit if a mixed-role ensemble (Butler + Character) ever ships.
	t.role = normalizeRole(e.Targets[0].AgentRole)
	t.roleKnown = true
	t.routedAt = e.At
	t.lastSeen = s.now()

	if !t.sttFinalAt.IsZero() {
		s.rec.AddressDetect(e.At.Sub(t.sttFinalAt))
	}
}

func (s *StageSubscriber) onTTSInvoked(e voiceevent.TTSInvoked) {
	if e.TurnID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.turns[e.TurnID]
	if t == nil {
		t = &turnState{}
		s.turns[e.TurnID] = t
	}
	t.pendingTTS = e.At // keep only the latest; see turnState.pendingTTS
	t.lastSeen = s.now()
}

func (s *StageSubscriber) onFirstAudio(e voiceevent.FirstAudio) {
	if e.TurnID == "" {
		return
	}
	var logAttrs []any
	defer func() { s.flushLog(logAttrs) }() // registered first → runs after Unlock (LIFO)
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.turns[e.TurnID]
	if t == nil {
		// FirstAudio with no opened turn (e.g. STTFinal lost): nothing to pair.
		return
	}
	t.lastSeen = s.now()

	// Per-sentence TTS time-to-first-byte: pair against the most-recent unmatched
	// TTSInvoked for this turn (serial dispatch, see turnState.pendingTTS). The
	// provider isn't on the bus; the voice slice synthesizes exclusively via
	// ElevenLabs, so the label is that fixed known provider (not a guess).
	if !t.pendingTTS.IsZero() {
		s.rec.TTSTimeToFirstByte(ttsProvider, e.At.Sub(t.pendingTTS))
		t.pendingTTS = time.Time{}
	}

	// First-audio is the turn-lifecycle SUCCESS signal ("this turn produced
	// audio"), recorded once; the headline response-latency SLO ends later, at the
	// first Opus packet on the wire (onFirstOpus). outcomeDone blocks the TTL reap
	// from also counting this turn as abandoned.
	if t.firstAudioDone {
		return
	}
	t.firstAudioDone = true
	t.firstAudioAt = e.At

	if !t.outcomeDone {
		t.outcomeDone = true
		s.rec.TurnOutcome(TurnFirstAudio, ReasonNone)
		logAttrs = s.turnLogAttrs(e.TurnID, t, "first_audio")
	}
}

// onFirstOpus records the headline response-latency SLO sample: the FIRST Opus
// packet of the turn reaching the Discord send path (audible-on-wire) minus the
// turn's VAD speech-end (Luk's definition, task #7). It is the audible-on-wire
// end the old handed-to-pump FirstAudio boundary undercounted. Recorded once per
// turn, and only when the span start is valid (non-zero SpeechEndAt) and the role
// label is known. A turn whose audio never reaches the wire (barge-cancelled
// before any frame) emits no FirstOpus and so records no sample — correctly, the
// user heard nothing.
func (s *StageSubscriber) onFirstOpus(e voiceevent.FirstOpus) {
	if e.TurnID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.turns[e.TurnID]
	if t == nil {
		// FirstOpus with no opened turn (STTFinal lost): nothing to anchor against.
		return
	}
	t.lastSeen = s.now()
	if t.latencyDone {
		return
	}
	t.latencyDone = true
	t.firstOpusAt = e.At
	if t.speechEndAt.IsZero() || !t.roleKnown {
		return
	}
	s.rec.ResponseLatency(t.role, e.At.Sub(t.speechEndAt))
}

// onTurnEnded records the terminal outcome of a turn that ended for a KNOWN
// reason (barge / supersede-coalesced / tts or provider error), published by the
// seam that knows the cause. It records the outcome+reason once (outcomeDone
// blocks the TTL reap — and a later end signal — from re-counting it) so the
// counter attributes WHY a turn died instead of the coarse no-first-audio
// catch-all. A turn-end arriving AFTER first audio (e.g. a barge mid-playback) is
// a normal interruption: outcomeDone is already set, so it is ignored.
//
// A BARGE for a turn this subscriber never OPENED (no prior STTFinal /
// AddressRouted / TTSInvoked spine) is ignored — that is the #310 Highlight
// voice-replay case: a barge cutting a replay publishes TurnEnded{replayTurnID,
// barge}, but a replay runs no STT/LLM/TTS, so the id was never opened; recording
// it would fabricate a phantom abandoned/barge turn for audio that actually played
// (the #391 class). The guard is NARROW to barge on purpose: every OTHER spine-less
// terminal is a legit sole signal that must still be counted by fabricating the
// turn — a text-modality Cross-talk Reaction sub-turn's TurnEnded{text_delivered}
// (#389) and the /say pre-dispatch refusals (directspeech.go) never open a turn yet
// their outcome is real.
func (s *StageSubscriber) onTurnEnded(e voiceevent.TurnEnded) {
	if e.TurnID == "" {
		return
	}
	var logAttrs []any
	defer func() { s.flushLog(logAttrs) }() // registered first → runs after Unlock (LIFO)
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.turns[e.TurnID]
	if t == nil {
		if e.Reason == voiceevent.TurnEndBarge {
			return // an unopened barge = a #310 replay cut; no spine to attribute audio that played
		}
		t = &turnState{}
		s.turns[e.TurnID] = t
	}
	t.lastSeen = s.now()
	if e.Text != "" {
		t.yieldedText = e.Text
	}
	if t.outcomeDone {
		return
	}
	t.outcomeDone = true
	outcome, reason, label := turnEndOutcome(e.Reason)
	s.rec.TurnOutcome(outcome, reason)
	logAttrs = s.turnLogAttrs(e.TurnID, t, label)
}

// turnEndOutcome maps a wire [voiceevent.TurnEndReason] to the bounded metric
// outcome+reason and the log label. A coalesced end is its own `yielded` outcome
// (it never reached TTS); the failure reasons are all `abandoned` (no audio) with
// the precise cause. An unknown reason collapses to abandoned/no_first_audio
// rather than leaking an unbounded label.
func turnEndOutcome(r voiceevent.TurnEndReason) (TurnOutcome, TurnReason, string) {
	switch r {
	case voiceevent.TurnEndSupersedeCoalesced:
		return TurnYielded, ReasonSupersessionGrace, "yielded"
	case voiceevent.TurnEndTextDelivered:
		return TurnTextDelivered, ReasonNone, "text_delivered"
	case voiceevent.TurnEndBarge:
		return TurnAbandoned, ReasonBarge, "abandoned"
	case voiceevent.TurnEndMute:
		return TurnAbandoned, ReasonMute, "abandoned"
	case voiceevent.TurnEndSpendCap:
		return TurnAbandoned, ReasonSpendCap, "abandoned"
	case voiceevent.TurnEndTTSError:
		return TurnAbandoned, ReasonTTSError, "abandoned"
	case voiceevent.TurnEndProviderError:
		return TurnAbandoned, ReasonProviderError, "abandoned"
	default:
		return TurnAbandoned, ReasonNoFirstAudio, "abandoned"
	}
}

// turnLogAttrs builds the slog attrs for the one-line per-turn timing trace at
// turn-end. Called under s.mu (it reads turnState), but it does NO I/O: the
// caller emits s.log.Info AFTER releasing the lock (see [StageSubscriber.flushLog])
// so the per-turn log write never happens under the mutex. outcome is
// "first_audio" (reached audio), "abandoned" (reaped without it), or "yielded"
// (coalesced away by the floor grace window). Durations are relative to
// speech-end (the SLO span start) when known.
func (s *StageSubscriber) turnLogAttrs(turnID string, t *turnState, outcome string) []any {
	attrs := []any{
		"turn_id", turnID,
		"outcome", outcome,
		"role", string(t.role),
	}
	if !t.speechEndAt.IsZero() {
		attrs = append(attrs, "speech_end", t.speechEndAt)
		if !t.routedAt.IsZero() {
			attrs = append(attrs, "routed_after", t.routedAt.Sub(t.speechEndAt))
		}
		if !t.firstAudioAt.IsZero() {
			attrs = append(attrs, "first_audio_after", t.firstAudioAt.Sub(t.speechEndAt))
		}
		if !t.firstOpusAt.IsZero() {
			attrs = append(attrs, "first_opus_after", t.firstOpusAt.Sub(t.speechEndAt))
		}
	}
	if t.firstAudioAt.IsZero() {
		// The debuggable signal the thin Sprint-2 logs lacked: a turn that opened
		// and never produced audio (the invisible 20s self-cancel).
		attrs = append(attrs, "no_audio", true)
	}
	if t.yieldedText != "" {
		// The dropped late-segment transcript: the residual a yielded turn loses
		// until real utterance coalescing routes it into the surviving turn.
		attrs = append(attrs, "yielded_text", t.yieldedText)
	}
	return attrs
}

// flushLog emits a per-turn log line for attrs built under the lock, if any. It
// MUST be called after s.mu is released — register it as a deferred call BEFORE
// the deferred Unlock so LIFO ordering runs Unlock first, then this. A nil attrs
// (no log was due) is a no-op.
func (s *StageSubscriber) flushLog(attrs []any) {
	if attrs != nil {
		s.log.Info("voice turn end", attrs...)
	}
}

// Start runs the TTL [StageSubscriber.Sweep] on a ticker until ctx is cancelled,
// so abandoned turns are reaped on a live node without the caller wiring its own
// loop. The run wiring calls Subscribe (attach handlers) AND Start (reap) — the
// leak guard the no-barge design relies on is only real once Start runs. The
// sweep interval is the TTL itself: reaping at most one TTL late is fine for a
// liveness guard. Mirrors [MetricsServer.Start].
func (s *StageSubscriber) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(s.ttl)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.Sweep()
			}
		}
	}()
}

// Sweep reaps per-turn state untouched for longer than the TTL, so abandoned
// turns (barged before audio, errored, never synthesized) don't accumulate.
// [StageSubscriber.Start] calls it on a ticker; it is also callable directly
// (tests). Safe for concurrent use. Returns the number of turns reaped (for
// tests / a debug gauge).
func (s *StageSubscriber) Sweep() int {
	// Collect the per-turn log lines for reaped turns under the lock, emit them
	// after releasing it — the per-turn log write must not happen under s.mu.
	var pending [][]any
	defer func() {
		for _, attrs := range pending {
			s.flushLog(attrs)
		}
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := s.now().Add(-s.ttl)
	reaped := 0
	for id, t := range s.turns {
		if t.lastSeen.Before(cutoff) {
			// A turn reaped without ever recording a terminal outcome never reached
			// first audio: it was cancelled (barge/supersede), errored in TTS, or
			// never synthesized — the exact failures response_latency cannot see.
			// Count it as abandoned (outcomeDone gates the success turns, which keep
			// their state alive for later-sentence ttfb pairing and are reaped here
			// without re-counting).
			if !t.outcomeDone {
				s.rec.TurnOutcome(TurnAbandoned, ReasonNoFirstAudio)
				pending = append(pending, s.turnLogAttrs(id, t, "abandoned"))
			}
			delete(s.turns, id)
			reaped++
		}
	}
	return reaped
}

// normalizeRole maps the wire AgentRole string ("butler"/"character") to the
// bounded [AgentRole] label; an unknown value collapses to the empty role rather
// than leaking an unbounded value into a series.
func normalizeRole(s string) AgentRole {
	switch AgentRole(s) {
	case RoleButler:
		return RoleButler
	case RoleCharacter:
		return RoleCharacter
	default:
		return ""
	}
}

// ttsProvider is the TTS provider label for tts_ttfb. The voice slice synthesizes
// exclusively via ElevenLabs; when a second TTS provider lands, the publishing
// stage must carry the provider on the bus and this constant goes away.
const ttsProvider = ProviderElevenLabs
