package orchestrator

import (
	"context"
	"errors"
	"log/slog"
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
// re-established when it drops: at once after a session that had been healthy,
// with bounded backoff after a failed dial or a session that died young. The
// adapter reports a death the moment it happens ([stt.StreamConfig.OnClosed]), so
// the redial normally completes before the speaker's next utterance. The local VAD stays the sole
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
	log      *slog.Logger

	// speakerID is the Speaker Lane this manager serves (ADR-0050), stamped on every
	// [voiceevent.STTPartial] it publishes. "" for the default (unattributed) lane —
	// the single-lane MVP path, unchanged. Set at construction via
	// [WithStreamSpeakerID].
	speakerID string

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
	// bindCtx is the maintainer's context, captured at bind. Once it is done the
	// manager is shutting down: a session's end is then the teardown itself, not a
	// death to recover from (see dropSession).
	bindCtx context.Context
	// stream is the current live session, nil while down (the maintainer is dialing).
	stream stt.Stream
	// backoff is the current re-establish delay. It grows on a failed dial and on
	// the death of a session younger than [streamHealthyLifetime], jumps to the cap
	// on an auth-class failure, and resets to the initial when a session proves
	// healthy: a commit resolves, or it lived at least that long before it ended.
	// A session retired over a failed or unanswered commit grows it at any age.
	backoff time.Duration
	// upSince is when the current session was installed (zero when a test installs
	// one directly, which reads as "long ago").
	upSince time.Time
	// redialNow is set when a healthy session is dropped: the maintainer redials it
	// without sleeping the backoff first. Consumed by the maintainer.
	redialNow bool
	// utterWhy is why the open utterance left the stream (set with utterDead); it is
	// the reason label of its batch_fallback count.
	utterWhy observe.STTFinalReason
	// per-utterance streaming state, all set from the audio-loop goroutine:
	curUtterID  string // minted at beginUtterance; stamped on this utterance's partials
	utterOpen   bool   // between beginUtterance and endUtterance — partials publish only then
	utterDead   bool   // stream was down at begin, or a send failed → this utterance is batch
	lastPartial string // last published partial text, for consecutive-duplicate dedupe
	// streamedDur accumulates the wall-clock duration of frames actually sent to the
	// live session this utterance (pre-roll flush + voiced), reset at beginUtterance
	// and metered at endUtterance (#127, ADR-0045/0042): audio sent is audio billed.
	streamedDur time.Duration

	// onDemand defers the first dial until the lane first speaks
	// ([WithStreamDialOnDemand]).
	onDemand bool

	// dialFailures / lastDialCode rate-limit the dial-failure log to one line per
	// run of same-coded failures. Maintainer-goroutine only.
	dialFailures int
	lastDialCode string

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

// streamHealthyLifetime is how long a session must have been up for its death to
// count as an ordinary end of a healthy session rather than a flap. A healthy
// session is redialed at once and forgives the backoff; a younger one is redialed
// after the (growing) backoff, so a provider that accepts the dial and then
// drops the session straight away is not redialed in a tight loop. It sits below
// the provider's 15 s idle window on purpose: an idle close — should the keepalive
// ever fail to prevent one — must not ratchet the backoff (#702).
const streamHealthyLifetime = 10 * time.Second

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

// WithStreamLogger sets where the manager reports what would otherwise be
// invisible (#702): a dial that fails (one line per run of same-coded failures —
// a missing or rejected API key lands here), the reconnect that ends such a run,
// and the unexpected end of a live session. A nil log keeps the silent default.
func WithStreamLogger(log *slog.Logger) StreamManagerOption {
	return func(m *StreamManager) {
		if log != nil {
			m.log = log
		}
	}
}

// WithStreamDialOnDemand defers the first dial from [bind] to the lane's first
// speech. That first utterance is transcribed by the batch adapter (there is no
// mid-utterance catch-up) and every later one streams.
//
// It is for a lane that may never speak — the default (unattributed) lane of a
// pipeline whose inbound frames are speaker-stamped. Dialing such a lane eagerly
// was harmless while the provider closed the idle session after 15 s; with the
// idle keepalive it would hold one realtime connection open for the whole Voice
// Session for nothing (ADR-0042 amendment 2026-11-22).
func WithStreamDialOnDemand() StreamManagerOption {
	return func(m *StreamManager) { m.onDemand = true }
}

// WithStreamSpeakerID sets the Speaker Lane id this manager serves (ADR-0050),
// stamped on every [voiceevent.STTPartial] it publishes. The per-lane stream factory
// ([WithLaneStreamingSTT]) sets it to the lane's speaker; the default lane leaves it
// "" (the single-lane MVP path).
func WithStreamSpeakerID(id string) StreamManagerOption {
	return func(m *StreamManager) { m.speakerID = id }
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
		log:            slog.New(slog.DiscardHandler),
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

// bind starts the maintainer goroutine (which dials eagerly so utterance 1 can
// stream, or on the first speech under [WithStreamDialOnDemand] — what a lane
// uses) and captures bus for STTPartial publishing. The returned func stops the
// maintainer and closes the live session; the [Segmenter] calls it from its own
// Bind teardown.
func (m *StreamManager) bind(ctx context.Context, bus *voiceevent.Bus) func() {
	m.mu.Lock()
	m.bus = bus
	m.mu.Unlock()

	mctx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.bindCtx = mctx
	m.mu.Unlock()
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

// maintain keeps one live session available (ADR-0042). The first dial is eager
// (no leading sleep) so the first utterance can stream — unless the manager dials
// on demand ([WithStreamDialOnDemand]), which waits for the first speech. After that:
//
//   - a failed dial sleeps the current backoff, which then doubles toward the cap
//     (an auth-class rejection jumps straight to the cap);
//   - the death of a session that had been healthy ([streamHealthyLifetime]) is
//     redialed at once, with the backoff forgiven — the provider's idle close and
//     an ordinary network drop land here;
//   - the death of a younger session sleeps the growing backoff like a failed
//     dial, and so does a session of any age that the manager retired over a
//     commit that failed or was never answered ([StreamManager.dropSession]).
func (m *StreamManager) maintain(ctx context.Context) {
	if m.onDemand {
		// Nothing to keep alive until the lane speaks: beginUtterance pokes.
		select {
		case <-ctx.Done():
			return
		case <-m.poke:
		}
	}
	var delay time.Duration // the first dial is eager
	for {
		if ctx.Err() != nil {
			return
		}
		if delay > 0 {
			if err := m.sleep(ctx, delay); err != nil {
				return
			}
		}
		if serr := m.dial(ctx); serr != nil {
			if ctx.Err() != nil {
				return
			}
			delay = m.dialFailed(serr)
			continue
		}
		// The session is up: serve until it is dropped. The state is re-read after
		// every poke rather than draining the channel first — a death that lands
		// between the install and this loop has already poked, and a drained poke
		// would park the maintainer with no session forever. A poke that finds the
		// session still up is a stale nudge and is ignored.
		for m.sessionUp() {
			select {
			case <-ctx.Done():
				return
			case <-m.poke:
			}
		}
		delay = m.redialDelay()
	}
}

// sessionUp reports whether a live session is installed.
func (m *StreamManager) sessionUp() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stream != nil
}

// dialFailed books one failed dial: it returns the delay to sleep before the next
// attempt, advances the backoff, and logs the failure — the first of a run and
// any change of error code, not every retry.
func (m *StreamManager) dialFailed(serr *stt.StreamError) time.Duration {
	m.mu.Lock()
	// An auth-class rejection (401/403 on the handshake, or no API key at all) is
	// durable: jump to the cap so it is not hammered with fast redials.
	if authClassCodes[serr.Code] {
		m.backoff = m.backoffMax
	}
	delay := m.backoff
	m.backoff = nextStreamBackoff(m.backoff, m.backoffMax)
	m.mu.Unlock()

	m.dialFailures++
	if m.dialFailures == 1 || serr.Code != m.lastDialCode {
		m.log.Warn("stt stream: dial failed; utterances use batch STT until it reconnects (repeats of this failure are not logged)",
			"code", serr.Code, "err", serr, "retry_in", delay)
	}
	m.lastDialCode = serr.Code
	return delay
}

// redialDelay is how long to wait before redialing a session that was just
// dropped: nothing when it had been healthy, else the current backoff (which then
// grows).
func (m *StreamManager) redialDelay() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.redialNow {
		m.redialNow = false
		return 0
	}
	delay := m.backoff
	m.backoff = nextStreamBackoff(m.backoff, m.backoffMax)
	return delay
}

// dialHandle ties one dial's OnClosed callback to the session that dial produced.
// The adapter may report the death before OpenStream has even returned, so the
// handle records it for [StreamManager.dial] to find. Guarded by StreamManager.mu.
type dialHandle struct {
	stream stt.Stream
	closed bool
	cause  error
}

// dial opens one fresh session and installs it. It returns the *StreamError on a
// dial failure (always transport-fatal from the adapter) so the maintainer backs
// off; nil means the session is live.
func (m *StreamManager) dial(ctx context.Context) *stt.StreamError {
	h := &dialHandle{}
	s, err := m.rec.OpenStream(ctx, stt.StreamConfig{
		SampleRate: streamSampleRate,
		OnPartial:  m.onPartial,
		OnClosed:   func(cause error) { m.sessionClosed(h, cause) },
	})
	if err != nil {
		return asStreamError(err)
	}
	m.mu.Lock()
	if h.closed {
		// Dead on arrival: it ended before it could be installed.
		cause := h.cause
		m.mu.Unlock()
		_ = s.Close()
		return asStreamError(cause)
	}
	h.stream = s
	m.stream = s
	m.upSince = m.now()
	m.mu.Unlock()

	if m.dialFailures > 0 {
		m.log.Info("stt stream: reconnected", "failed_dials", m.dialFailures)
		m.dialFailures, m.lastDialCode = 0, ""
	}
	return nil
}

// asStreamError returns err as a *StreamError, wrapping anything else as a fatal
// transport failure.
func asStreamError(err error) *stt.StreamError {
	var se *stt.StreamError
	if errors.As(err, &se) {
		return se
	}
	return &stt.StreamError{Code: stt.CodeTransport, Fatal: true, Err: err}
}

// sessionClosed is the adapter's [stt.StreamConfig.OnClosed] for the session of
// dial h: the death notification that lets the maintainer redial NOW instead of
// when the next utterance trips over the corpse (#702). It runs on an adapter
// goroutine. A session the manager already dropped itself (a retire, the bind
// teardown) is no longer current, so its close is a no-op here.
func (m *StreamManager) sessionClosed(h *dialHandle, cause error) {
	m.mu.Lock()
	h.closed, h.cause = true, cause
	s := h.stream
	m.mu.Unlock()
	if s != nil {
		m.dropSession(s, cause, true)
	}
}

// dropSession uninstalls s if it is still the live session, and wakes the
// maintainer to redial. It is the one place a live session is given up, so the
// consequences are decided once:
//
//   - an utterance that is open right now can no longer be committed on s, and
//     must not continue on whatever session the redial installs (that session
//     would hold only its tail): it is marked batch;
//   - an auth-class cause jumps the backoff to the cap;
//   - a session that ENDED (ended=true: the adapter reported its close, or a send
//     found it dead) after a healthy lifetime is redialed at once and forgives
//     the backoff, while a younger one leaves the backoff to grow;
//   - a session the manager RETIRED (ended=false: a commit failed or was never
//     answered, or a send poisoned it) leaves the backoff to grow however old it
//     is. Its age is not evidence that it worked — it just proved it did not —
//     and a provider that accepts the handshake but answers no commit would
//     otherwise cost every turn the full commit timeout for the whole incident
//     (#702). Only a resolved commit forgives ([StreamManager.resetBackoff]).
//
// It reports whether s was the live session. Safe from any goroutine.
func (m *StreamManager) dropSession(s stt.Stream, cause error, ended bool) bool {
	m.mu.Lock()
	if s == nil || m.stream != s {
		m.mu.Unlock()
		return false
	}
	if m.bindCtx != nil && m.bindCtx.Err() != nil {
		// Shutting down: the adapter is reporting the close the cancelled context
		// asked for. Leave the session installed for bind's stop func to Close;
		// there is nothing to redial and nothing worth logging.
		m.mu.Unlock()
		return false
	}
	m.stream = nil
	lived := m.now().Sub(m.upSince)
	auth := isAuthClass(cause)
	switch {
	case auth:
		m.backoff = m.backoffMax
		m.redialNow = false
	case ended && lived >= streamHealthyLifetime:
		m.backoff = m.backoffInitial
		m.redialNow = true
	default:
		m.redialNow = false
	}
	now := m.redialNow
	if m.utterOpen && !m.utterDead {
		m.utterDead = true
		m.utterWhy = observe.STTFinalStreamDied
	}
	m.mu.Unlock()

	m.log.Info("stt stream: session ended; reconnecting",
		"cause", cause, "lived", lived.Round(time.Millisecond), "immediate", now)
	m.pokeMaintainer()
	return true
}

// isAuthClass reports whether err is a provider error of a durable
// configuration/quota kind ([authClassCodes]).
func isAuthClass(err error) bool {
	var se *stt.StreamError
	return errors.As(err, &se) && authClassCodes[se.Code]
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
	m.utterWhy = ""
	if s == nil {
		m.utterWhy = observe.STTFinalStreamDown
	}
	m.mu.Unlock()

	preRoll := m.ring
	m.ring = m.ring[:0]

	if s == nil {
		m.pokeMaintainer()
		return id
	}
	for _, f := range preRoll {
		if err := s.Send(f); err != nil {
			m.streamFailed(s, err, observe.STTFinalStreamDied)
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
// and, if the error is fatal — or leaves already-streamed audio uncommitted, see
// [StreamManager.streamFailed] — the session is dropped and the maintainer nudged.
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
		m.utterWhy = observe.STTFinalStreamDied
		m.mu.Unlock()
		m.pokeMaintainer()
		return
	}
	if err := s.Send(f); err != nil {
		m.streamFailed(s, err, observe.STTFinalStreamDied)
		return
	}
	m.addStreamed(f) // voiced frame accepted → bill it (#127)
}

// streamCommit is one utterance's in-flight manual commit: the channel it resolves
// on, when it was sent (the stt_request span start), and the session that issued
// it — so a commit that never resolves can retire exactly that session (#639).
type streamCommit struct {
	ch      <-chan stt.CommitResult
	sentAt  time.Time
	session stt.Stream
}

// endUtterance closes the utterance. When it streamed live to a healthy session it
// requests the manual commit and returns its handle; ok is false when the
// utterance never streamed (session down at begin, or a send died), meaning the
// caller transcribes it via the pure batch path — counted here as a
// batch_fallback with the reason the utterance left the stream (#702).
func (m *StreamManager) endUtterance() (commit *streamCommit, ok bool) {
	m.mu.Lock()
	m.utterOpen = false
	dead := m.utterDead
	why := m.utterWhy
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
		if why == "" {
			why = observe.STTFinalStreamDied
		}
		m.metrics.STTFinal(m.provider, observe.STTTransportBatchFallback, why)
		return nil, false
	}
	ch, err := s.Commit()
	if err != nil {
		m.streamFailed(s, err, observe.STTFinalCommitError)
		m.metrics.STTFinal(m.provider, observe.STTTransportBatchFallback, observe.STTFinalCommitError)
		return nil, false
	}
	return &streamCommit{ch: ch, sentAt: m.now(), session: s}, true
}

// awaitCommit waits up to commitTimeout for a streamed commit to resolve. On a
// committed transcript it records the stt_request span, resets the backoff (a
// healthy session forgives past failures), and returns ok=true — the authoritative
// streamed text. On a provider error or a timeout it returns ok=false so the caller
// falls back to the batch adapter with the same locally-buffered frames. Either
// way the utterance is counted once on stt_final (#702).
//
// A failed or unanswered commit also retires the session that issued it, by
// identity. A timeout (#639): the adapter resolves commits strictly FIFO, so an
// unanswered commit left at the head of its queue would make every later commit
// resolve one utterance behind. An error (#702): the audio streamed for this
// utterance was not committed, so on a session that lived on it would fold into
// the next utterance's commit — and the one commit error the provider is known to
// send, commit_throttled, closes the session anyway.
func (m *StreamManager) awaitCommit(c *streamCommit) (stt.Transcript, bool) {
	timer := time.NewTimer(m.commitTimeout)
	defer timer.Stop()
	select {
	case res := <-c.ch:
		m.metrics.STTRequest(m.provider, m.now().Sub(c.sentAt))
		if res.Err != nil {
			// Provider health (#125): a resolved-but-failed commit is a stt-stage error.
			m.metrics.ProviderCall(observe.StageSTT, m.provider, observe.OutcomeError)
			m.metrics.ProviderError(observe.StageSTT, m.provider)
			m.metrics.STTFinal(m.provider, observe.STTTransportBatchFallback, observe.STTFinalCommitError)
			m.retireSession(c.session, res.Err)
			m.noteAuthBackoff(res.Err)
			return stt.Transcript{}, false
		}
		m.metrics.ProviderCall(observe.StageSTT, m.provider, observe.OutcomeOK)
		m.metrics.STTFinal(m.provider, observe.STTTransportStream, observe.STTFinalCommitted)
		m.resetBackoff()
		return res.Transcript, true
	case <-timer.C:
		// Record the span on timeout too: a stalled provider is exactly what this
		// series exists to surface, and the batch adapter records its call on failure
		// as well (batch parity). The provider-call counter mirrors it with a
		// timeout outcome plus the error-only sibling (#125).
		m.metrics.STTRequest(m.provider, m.now().Sub(c.sentAt))
		m.metrics.ProviderCall(observe.StageSTT, m.provider, observe.OutcomeTimeout)
		m.metrics.ProviderError(observe.StageSTT, m.provider)
		m.metrics.STTFinal(m.provider, observe.STTTransportBatchFallback, observe.STTFinalCommitTimeout)
		m.retireSession(c.session, errCommitTimeout)
		return stt.Transcript{}, false
	}
}

// errCommitTimeout is the cause recorded when a session is retired because a
// commit it issued never resolved.
var errCommitTimeout = errors.New("commit did not resolve within the commit timeout")

// retireSession gives up on s — a session that may still be alive but can no
// longer be trusted: it is dropped if it still IS the live one (so a stale
// timeout after a redial never kills the fresh session, see
// [StreamManager.dropSession]) and closed off the caller's goroutine (Close waits
// for the adapter's pumps, which may be mid-write). A nil s is a no-op.
func (m *StreamManager) retireSession(s stt.Stream, cause error) {
	if s == nil {
		return
	}
	m.dropSession(s, cause, false)
	go func() { _ = s.Close() }()
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
	bus.Publish(voiceevent.STTPartial{At: m.now(), Text: text, UtteranceID: id, SpeakerID: m.speakerID})
}

// streamFailed records a send/commit failure against session s: the utterance is
// marked batch (why is its fallback reason), an auth-class code jumps the backoff
// to the cap, and a fatal error on the still-current session drops it and nudges
// the maintainer to re-establish.
//
// A RECOVERABLE error after this utterance already streamed audio poisons the
// session too (#639 B): the audio it accepted is never committed (the utterance
// now goes batch), so the provider would fold it into the NEXT utterance's
// commit. The session is retired like a fatal one, and closed off the audio loop.
func (m *StreamManager) streamFailed(s stt.Stream, err error, why observe.STTFinalReason) {
	var se *stt.StreamError
	isSE := errors.As(err, &se)
	fatal := !isSE || se.Fatal

	m.mu.Lock()
	if !m.utterDead {
		m.utterDead = true
		m.utterWhy = why
	}
	poisoned := !fatal && m.streamedDur > 0
	m.mu.Unlock()

	switch {
	case poisoned:
		m.retireSession(s, err)
	case fatal:
		m.dropSession(s, err, true)
	}
	// After the drop, so an auth-class code holds the cap even when s was no longer
	// the live session (dropSession then decided nothing).
	m.noteAuthBackoff(err)
}

// noteAuthBackoff jumps the backoff to the cap when err is an auth-class provider
// error observed off the audio loop (a commit resolving fatal on the worker). It
// does not touch the session pointer — the worker may be resolving an old
// utterance's commit while the maintainer has already moved on (the commit
// timeout path retires by identity instead, see [StreamManager.retireSession]).
func (m *StreamManager) noteAuthBackoff(err error) {
	if isAuthClass(err) {
		m.mu.Lock()
		m.backoff = m.backoffMax
		m.redialNow = false
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
