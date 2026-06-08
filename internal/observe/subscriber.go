package observe

import (
	"context"
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

	mu    sync.Mutex
	turns map[string]*turnState
}

// turnState is the per-TurnID timing state, opened at STTFinal and closed when
// the headline sample lands (or reaped by TTL). lastSeen drives expiry.
type turnState struct {
	speechEndAt  time.Time // STTFinal.SpeechEndAt; zero ⇒ no headline sample
	sttFinalAt   time.Time
	role         AgentRole
	roleKnown    bool // set at AddressRouted; gates the headline (needs the role label)
	headlineDone bool // first FirstAudio consumed for response_latency

	// pendingTTS is the arrival-ordered queue of TTSInvoked times awaiting their
	// matching FirstAudio for per-sentence tts_ttfb.
	pendingTTS []time.Time

	lastSeen time.Time
}

// defaultTurnTTL bounds how long a turn's state lives without progress before
// [StageSubscriber.Sweep] reaps it — long enough to outlast a slow LLM + TTS turn
// (the SLO tail is seconds), short enough that abandoned turns (barged, errored,
// or never-synthesized) don't accumulate.
const defaultTurnTTL = 60 * time.Second

// NewStageSubscriber builds a subscriber recording onto rec. A nil rec is
// replaced with [Discard] so it is always safe to construct.
func NewStageSubscriber(rec StageRecorder) *StageSubscriber {
	if rec == nil {
		rec = Discard{}
	}
	return &StageSubscriber{
		rec:   rec,
		now:   time.Now,
		ttl:   defaultTurnTTL,
		turns: make(map[string]*turnState),
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
		voiceevent.On(bus, s.onTTSInvoked),
		voiceevent.On(bus, s.onFirstAudio),
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
	t.lastSeen = s.now()

	// address_detect = route decision − transcript. Only when we have the
	// STTFinal anchor (sttFinalAt set); otherwise we can't compute the span.
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
	t.pendingTTS = append(t.pendingTTS, e.At)
	t.lastSeen = s.now()
}

func (s *StageSubscriber) onFirstAudio(e voiceevent.FirstAudio) {
	if e.TurnID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.turns[e.TurnID]
	if t == nil {
		// FirstAudio with no opened turn (e.g. STTFinal lost): nothing to pair.
		return
	}
	t.lastSeen = s.now()

	// Per-sentence TTS time-to-first-byte: pair against the oldest unmatched
	// TTSInvoked for this turn (arrival order; dispatch is sequential). The
	// provider isn't on the bus; the voice slice synthesizes exclusively via
	// ElevenLabs, so the label is that fixed known provider (not a guess).
	if len(t.pendingTTS) > 0 {
		ttsAt := t.pendingTTS[0]
		t.pendingTTS = t.pendingTTS[1:]
		s.rec.TTSTimeToFirstByte(ttsProvider, e.At.Sub(ttsAt))
	}

	// Headline response latency: the FIRST FirstAudio of the turn only, and only
	// when the span start is valid (non-zero SpeechEndAt) and the role is known.
	if t.headlineDone {
		return
	}
	t.headlineDone = true
	if t.speechEndAt.IsZero() || !t.roleKnown {
		return
	}
	s.rec.ResponseLatency(t.role, e.At.Sub(t.speechEndAt))
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
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := s.now().Add(-s.ttl)
	reaped := 0
	for id, t := range s.turns {
		if t.lastSeen.Before(cutoff) {
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
