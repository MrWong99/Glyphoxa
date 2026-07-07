package orchestrator

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// StreamManager is the streaming-STT transport that replaces the serial batch
// POST worker with a persistent per-Voice-Session websocket (ADR-0042). It owns
// one [stt.StreamingRecognizer] session at a time, opened eagerly at [bind] and
// re-established with bounded backoff when it drops. The local VAD stays the sole
// endpointing authority: the [Segmenter] drives the manager's per-utterance
// lifecycle (idle pre-roll → begin → voiced sends → end/commit), so the manual
// commit fires only from a VAD speech_end (or Flush), never provider-side.
//
// It is additive, never authoritative: the Segmenter keeps buffering the same
// locally-segmented frames, so a stream that is down, dies mid-utterance, or
// commits too slowly degrades to the batch adapter WITHOUT losing the utterance's
// transcription. Streamed [voiceevent.STTPartial]s are a live-view signal only;
// exactly one [voiceevent.STTFinal] per utterance reaches Address Detection and
// the Transcript, from the commit on the happy path or the batch fallback.
//
// A nil StreamManager wired into a [Conversation] ([WithStreamingSTT]) is zero
// behaviour change — the byte-for-byte no-streaming default.
type StreamManager struct {
	rec stt.StreamingRecognizer

	backoffInitial time.Duration
	backoffMax     time.Duration
	commitTimeout  time.Duration
	preRoll        int

	metrics  observe.StageRecorder
	provider observe.Provider

	// sleep/now are the deterministic-test seams, copied from wirenpc.reconnectPolicy:
	// sleep blocks for d or until ctx is cancelled; now stamps the commit-latency
	// span and partial timestamps. Production uses real time.
	sleep func(ctx context.Context, d time.Duration) error
	now   func() time.Time

	// poke nudges the maintainer to (re)establish the stream after a death. Buffered
	// depth 1: a coalesced nudge is enough because the maintainer re-reads state.
	poke chan struct{}

	mu  sync.Mutex
	bus *voiceevent.Bus // captured at bind; where STTPartial is published
	// stream is the current live session, nil while down (the maintainer is dialing).
	stream stt.Stream
	// backoff is the current re-establish delay; grows on dial failure, jumps to the
	// cap on an auth-class death, resets to the initial on the first successful commit.
	backoff time.Duration
	// per-utterance streaming state, all set from the audio-loop goroutine:
	curUtterID  string // minted at beginUtterance; stamped on this utterance's partials
	utterOpen   bool   // between beginUtterance and endUtterance — partials publish only then
	utterDead   bool   // stream was down at begin, or a send failed → this utterance is batch
	lastPartial string // last published partial text, for consecutive-duplicate dedupe
	// streamedDur accumulates the wall-clock duration of frames actually sent to the
	// live session this utterance (pre-roll flush + voiced), reset at beginUtterance
	// and metered at endUtterance (#127, ADR-0045/0042): audio sent is audio billed.
	streamedDur time.Duration

	// ring is the pre-roll buffer of the most recent idle (pre-speech) frames,
	// streamed as context at beginUtterance. Audio-loop-only (idleFrame/beginUtterance
	// run on the same goroutine), so it needs no lock. Wire-only: it is never added to
	// the Segmenter's fallback segment, so batch cassette hashes stay stable.
	ring []audio.Frame
}

// streamSampleRate is the PCM rate every streamed session declares. It matches the
// VAD/STT frame geometry (wirenpc.vadSampleRate) and the only rate the ElevenLabs
// realtime adapter accepts; frames the Segmenter sends carry this rate.
const streamSampleRate = 16000

// authClassCodes are the provider fatal codes that reflect a durable
// configuration/quota problem rather than a transient blip (ADR-0042). Observing
// one jumps the re-establish backoff straight to the cap so the manager does not
// hammer a session that will keep being rejected.
var authClassCodes = map[string]bool{
	"auth_error":       true,
	"unaccepted_terms": true,
	"quota_exceeded":   true,
}

// StreamManagerOption configures a [StreamManager] at construction.
type StreamManagerOption func(*StreamManager)

// WithStreamBackoff overrides the re-establish backoff schedule (default 1s,
// doubling to a 30s cap, no jitter — one session to one provider has no
// thundering herd to spread).
func WithStreamBackoff(initial, max time.Duration) StreamManagerOption {
	return func(m *StreamManager) {
		m.backoffInitial = initial
		m.backoffMax = max
	}
}

// WithCommitTimeout bounds how long a streamed commit may take to resolve before
// the manager falls back to the batch adapter for that utterance (default 3s). It
// guards a rate_limited-stalled pending commit from wedging the transcription
// worker — the streaming analogue of the batch [WithSTTTimeout].
func WithCommitTimeout(d time.Duration) StreamManagerOption {
	return func(m *StreamManager) { m.commitTimeout = d }
}

// WithPreRoll overrides the number of pre-speech idle frames streamed as context
// at the start of an utterance (default 10 frames ≈ 320 ms at the 32 ms cadence).
func WithPreRoll(n int) StreamManagerOption {
	return func(m *StreamManager) { m.preRoll = n }
}

// WithStreamMetrics injects the stt_request instrumentation for the streaming
// path: rec receives one [observe.StageRecorder.STTRequest] span per resolved
// commit (commit sent → committed), labelled provider. A nil rec keeps the no-op
// default. Per ADR-0032 UtteranceID/TurnID are never metric labels.
func WithStreamMetrics(rec observe.StageRecorder, p observe.Provider) StreamManagerOption {
	return func(m *StreamManager) {
		if rec != nil {
			m.metrics = rec
		}
		m.provider = p
	}
}

// NewStreamManager wires rec as the streaming recognizer. rec must be non-nil;
// passing nil panics. The manager does nothing until [bind] starts its maintainer.
func NewStreamManager(rec stt.StreamingRecognizer, opts ...StreamManagerOption) *StreamManager {
	if rec == nil {
		panic("orchestrator.NewStreamManager: rec must not be nil")
	}
	m := &StreamManager{
		rec:            rec,
		backoffInitial: time.Second,
		backoffMax:     30 * time.Second,
		commitTimeout:  3 * time.Second,
		preRoll:        10,
		metrics:        observe.Discard{},
		sleep:          sleepCtx,
		now:            time.Now,
		poke:           make(chan struct{}, 1),
	}
	for _, o := range opts {
		o(m)
	}
	m.backoff = m.backoffInitial
	return m
}

// sleepCtx blocks for d or until ctx is cancelled, returning ctx.Err() on cancel.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// bind starts the maintainer goroutine (which dials EAGERLY, so utterance 1 can
// stream) and captures bus for STTPartial publishing. The returned func stops the
// maintainer and closes the live session; the [Segmenter] calls it from its own
// Bind teardown.
func (m *StreamManager) bind(ctx context.Context, bus *voiceevent.Bus) func() {
	m.mu.Lock()
	m.bus = bus
	m.mu.Unlock()

	mctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		m.maintain(mctx)
	}()

	return func() {
		cancel()
		wg.Wait()
		m.mu.Lock()
		s := m.stream
		m.stream = nil
		m.mu.Unlock()
		if s != nil {
			_ = s.Close()
		}
	}
}

// maintain keeps one live session available, re-establishing it with bounded
// backoff after a death (ADR-0042). The first dial is eager (no leading sleep) so
// the first utterance can stream; every later attempt sleeps the current backoff
// first. A successful dial serves until a death pokes the maintainer awake.
func (m *StreamManager) maintain(ctx context.Context) {
	first := true
	for {
		if ctx.Err() != nil {
			return
		}
		if !first {
			m.mu.Lock()
			delay := m.backoff
			m.backoff = nextStreamBackoff(m.backoff, m.backoffMax)
			m.mu.Unlock()
			if err := m.sleep(ctx, delay); err != nil {
				return
			}
		}
		first = false

		if serr := m.dial(ctx); serr != nil {
			if ctx.Err() != nil {
				return
			}
			// An auth-class dial rejection (401/403 → auth_error) is durable: jump the
			// backoff to the cap so a revoked key is not hammered with fast redials.
			if authClassCodes[serr.Code] {
				m.mu.Lock()
				m.backoff = m.backoffMax
				m.mu.Unlock()
			}
			continue // dial failure: back off and retry
		}
		if ctx.Err() != nil {
			return
		}
		// The session is up. Drain any stale poke (the death we just healed, or a send
		// that raced the redial), then block until a fresh death or shutdown.
		select {
		case <-m.poke:
		default:
		}
		select {
		case <-ctx.Done():
			return
		case <-m.poke:
		}
	}
}

// dial opens one fresh session and installs it. It returns the *StreamError on a
// dial failure (always transport-fatal from the adapter) so the maintainer backs
// off; nil means the session is live.
func (m *StreamManager) dial(ctx context.Context) *stt.StreamError {
	s, err := m.rec.OpenStream(ctx, stt.StreamConfig{
		SampleRate: streamSampleRate,
		OnPartial:  m.onPartial,
	})
	if err != nil {
		var se *stt.StreamError
		if errors.As(err, &se) {
			return se
		}
		return &stt.StreamError{Code: stt.CodeTransport, Fatal: true, Err: err}
	}
	m.mu.Lock()
	m.stream = s
	m.mu.Unlock()
	return nil
}

// nextStreamBackoff doubles d toward max, clamping at the cap (and guarding
// overflow).
func nextStreamBackoff(d, max time.Duration) time.Duration {
	n := d * 2
	if n <= 0 || n > max {
		return max
	}
	return n
}

// pokeMaintainer nudges the maintainer to re-establish the session, without
// blocking the caller (a coalesced nudge suffices).
func (m *StreamManager) pokeMaintainer() {
	select {
	case m.poke <- struct{}{}:
	default:
	}
}

// idleFrame pushes one pre-speech frame into the bounded pre-roll ring. Idle
// frames are NEVER streamed to the provider (silence is not billed) — they only
// become context if speech starts, when [beginUtterance] streams the ring.
func (m *StreamManager) idleFrame(f audio.Frame) {
	if m.preRoll <= 0 {
		return
	}
	if len(m.ring) < m.preRoll {
		m.ring = append(m.ring, f)
		return
	}
	copy(m.ring, m.ring[1:])
	m.ring[m.preRoll-1] = f
}

// beginUtterance mints this utterance's id and, if the session is up, streams the
// pre-roll ring as leading context (voiced frames then follow via [send]). If the
// session is down at speech_start the utterance is marked pure-batch — there is no
// mid-utterance catch-up — and the maintainer is nudged to heal in the background.
// at is the VAD speech_start time (reserved for future speculation anchoring).
func (m *StreamManager) beginUtterance(at time.Time) string {
	id := voiceevent.NewUtteranceID()

	m.mu.Lock()
	m.curUtterID = id
	m.utterOpen = true
	m.lastPartial = ""
	m.streamedDur = 0 // reset the per-utterance billed-audio accumulator (#127)
	s := m.stream
	m.utterDead = s == nil
	m.mu.Unlock()

	preRoll := m.ring
	m.ring = m.ring[:0]

	if s == nil {
		m.pokeMaintainer()
		return id
	}
	for _, f := range preRoll {
		if err := s.Send(f); err != nil {
			m.streamFailed(s, err)
			break
		}
		m.addStreamed(f) // pre-roll frame accepted → bill it (#127)
	}
	return id
}

// addStreamed adds one accepted frame's duration to the per-utterance billed-audio
// accumulator (#127, ADR-0045/0042). Called only after a successful Send, so a frame
// the provider rejected is never billed.
func (m *StreamManager) addStreamed(f audio.Frame) {
	m.mu.Lock()
	m.streamedDur += frameDuration(f)
	m.mu.Unlock()
}

// send streams one voiced frame to the session. On any send error the utterance is
// marked dead (it falls back to batch with the Segmenter's locally-buffered frames)
// and, if the error is fatal, the dead session is dropped and the maintainer nudged.
// A no-op once the utterance is dead or the session is down.
func (m *StreamManager) send(f audio.Frame) {
	m.mu.Lock()
	dead := m.utterDead
	s := m.stream
	m.mu.Unlock()
	if dead {
		return
	}
	if s == nil {
		m.mu.Lock()
		m.utterDead = true
		m.mu.Unlock()
		m.pokeMaintainer()
		return
	}
	if err := s.Send(f); err != nil {
		m.streamFailed(s, err)
		return
	}
	m.addStreamed(f) // voiced frame accepted → bill it (#127)
}

// endUtterance closes the utterance. When it streamed live to a healthy session it
// requests the manual commit and returns the resolving channel plus the sent-at
// time; ok is false when the utterance never streamed (session down at begin, or a
// send died), meaning the caller transcribes it via the pure batch path.
func (m *StreamManager) endUtterance() (commit <-chan stt.CommitResult, sentAt time.Time, ok bool) {
	m.mu.Lock()
	m.utterOpen = false
	dead := m.utterDead
	s := m.stream
	streamed := m.streamedDur
	m.mu.Unlock()
	// Meter the streamed audio once per utterance, regardless of the commit outcome
	// below (#127, ADR-0045/0042): audio sent is audio billed. A pure-batch utterance
	// streamed nothing (streamed == 0), so nothing is billed here — the batch path
	// bills its clip; a fatal mid-utterance death bills only what streamed, and the
	// batch fallback then bills its own clip (both calls truthfully billed).
	if streamed > 0 {
		m.metrics.STTAudioSeconds(m.provider, streamed)
	}
	if dead || s == nil {
		return nil, time.Time{}, false
	}
	ch, err := s.Commit()
	if err != nil {
		m.streamFailed(s, err)
		return nil, time.Time{}, false
	}
	return ch, m.now(), true
}

// awaitCommit waits up to commitTimeout for a streamed commit to resolve. On a
// committed transcript it records the stt_request span, resets the backoff (a
// healthy session forgives past failures), and returns ok=true — the authoritative
// streamed text. On a provider error or a timeout it returns ok=false so the caller
// falls back to the batch adapter with the same locally-buffered frames.
func (m *StreamManager) awaitCommit(ch <-chan stt.CommitResult, sentAt time.Time) (stt.Transcript, bool) {
	timer := time.NewTimer(m.commitTimeout)
	defer timer.Stop()
	select {
	case res := <-ch:
		m.metrics.STTRequest(m.provider, m.now().Sub(sentAt))
		if res.Err != nil {
			// Provider health (#125): a resolved-but-failed commit is a stt-stage error.
			m.metrics.ProviderCall(observe.StageSTT, m.provider, observe.OutcomeError)
			m.metrics.ProviderError(observe.StageSTT, m.provider)
			m.noteAuthBackoff(res.Err)
			return stt.Transcript{}, false
		}
		m.metrics.ProviderCall(observe.StageSTT, m.provider, observe.OutcomeOK)
		m.resetBackoff()
		return res.Transcript, true
	case <-timer.C:
		// Record the span on timeout too: a stalled provider is exactly what this
		// series exists to surface, and the batch adapter records its call on failure
		// as well (batch parity). The provider-call counter mirrors it with a
		// timeout outcome plus the error-only sibling (#125).
		m.metrics.STTRequest(m.provider, m.now().Sub(sentAt))
		m.metrics.ProviderCall(observe.StageSTT, m.provider, observe.OutcomeTimeout)
		m.metrics.ProviderError(observe.StageSTT, m.provider)
		return stt.Transcript{}, false
	}
}

// onPartial publishes a streamed interim hypothesis as [voiceevent.STTPartial],
// stamped with the current utterance id. It drops partials that arrive with no open
// utterance, and dedupes a partial identical to the one just published (the adapter
// can repeat a stabilized hypothesis). Runs on the adapter's read goroutine, so it
// may fan out concurrently with the audio loop.
func (m *StreamManager) onPartial(text string) {
	m.mu.Lock()
	if !m.utterOpen || text == m.lastPartial {
		m.mu.Unlock()
		return
	}
	m.lastPartial = text
	id := m.curUtterID
	bus := m.bus
	m.mu.Unlock()
	if bus == nil {
		return
	}
	bus.Publish(voiceevent.STTPartial{At: m.now(), Text: text, UtteranceID: id})
}

// streamFailed records a send/commit failure against session s: the utterance is
// marked batch, an auth-class code jumps the backoff to the cap, and a fatal error
// on the still-current session drops it and nudges the maintainer to re-establish.
func (m *StreamManager) streamFailed(s stt.Stream, err error) {
	var se *stt.StreamError
	isSE := errors.As(err, &se)
	fatal := !isSE || se.Fatal

	m.mu.Lock()
	m.utterDead = true
	if isSE && authClassCodes[se.Code] {
		m.backoff = m.backoffMax
	}
	poke := false
	if fatal && m.stream == s {
		m.stream = nil
		poke = true
	}
	m.mu.Unlock()
	if poke {
		m.pokeMaintainer()
	}
}

// noteAuthBackoff jumps the backoff to the cap when err is an auth-class provider
// error observed off the audio loop (a commit resolving fatal on the worker). It
// does not touch the session pointer — the worker may be resolving an old
// utterance's commit while the maintainer has already moved on.
func (m *StreamManager) noteAuthBackoff(err error) {
	var se *stt.StreamError
	if errors.As(err, &se) && authClassCodes[se.Code] {
		m.mu.Lock()
		m.backoff = m.backoffMax
		m.mu.Unlock()
	}
}

// resetBackoff forgives past failures after a healthy commit, so a later drop
// re-establishes promptly at the initial delay.
func (m *StreamManager) resetBackoff() {
	m.mu.Lock()
	m.backoff = m.backoffInitial
	m.mu.Unlock()
}
