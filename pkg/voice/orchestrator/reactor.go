package orchestrator

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// Reactor is one self-contained bus interaction in the voice pipeline: it turns
// events the call-driven stages publish (VAD, STT, TTS) into the next stage's
// call. The address detector (STTFinal → AddressRouted), the [Segmenter]
// (speech transitions → STT), and the [Replier] (AddressRouted → TTS) are the
// reactors that wire slice 1 (ADR-0019, ADR-0026).
//
// Bind installs the reactor's subscriptions on bus and returns a function that
// removes them; nothing happens until Bind is called. The ctx governs the
// reactions' lifetime and is the context handed to any STT/TTS call the reactor
// triggers — cancelling it lets an in-flight provider call unwind — but
// teardown stays explicit: cancelling ctx does not unsubscribe, only the
// returned cancel does. This mirrors [voiceevent.Bus.Subscribe], which also
// hands back its own unsubscribe func.
type Reactor interface {
	Bind(ctx context.Context, bus *voiceevent.Bus) (cancel func())
}

// Bind installs every reactor on bus and returns a single function that tears
// them all down, in reverse order. It is the "in parts" entry point: compose
// any hand-picked subset of reactors without the [Conversation] facade.
func Bind(ctx context.Context, bus *voiceevent.Bus, reactors ...Reactor) (cancel func()) {
	if bus == nil {
		panic("orchestrator.Bind: bus must not be nil")
	}
	cancels := make([]func(), len(reactors))
	for i, r := range reactors {
		cancels[i] = r.Bind(ctx, bus)
	}
	return func() {
		for i := len(cancels) - 1; i >= 0; i-- {
			cancels[i]()
		}
	}
}

// ErrorFunc reports an error from a stage call a reactor fires off the audio
// loop. The [Replier]'s [TTS.Dispatch] runs inside a bus callback, which cannot
// return an error; the [Segmenter]'s [STT.Transcribe] runs on a worker goroutine
// (#24), which likewise has no caller to return to. Both surface their failures
// here instead. A nil ErrorFunc drops the error silently.
type ErrorFunc func(error)

// Segmenter turns the VAD stage's frame-level transitions into utterance-sized
// batches for STT. It is both a frame sink and a [Reactor]: callers feed PCM
// via [Segmenter.Process], which drives the wrapped VAD stage, and
// [Segmenter.Bind] subscribes to the VADSpeechStart / VADSpeechEnd events that
// stage publishes. Frames that arrive while speech is active are buffered; when
// speech ends the completed batch is handed to a single transcription worker
// goroutine so the network-bound [STT.Transcribe] call never stalls the audio
// loop (#24). The worker is serial: it transcribes utterances in the order they
// were segmented, so STTFinal — and the turns it fans out — stay in speech order
// (positional cassette replay and downstream turn-taking both rely on this).
//
// This is the bus-driven form of the accumulate-between-VAD-events loop the
// slice-1 pipeline test used to spell out inline (ADR-0026).
type Segmenter struct {
	stt *STT

	// onError surfaces a recognizer error from the transcription worker (see
	// [Segmenter.Process]). Bus callbacks — and now the off-loop STT call — cannot
	// return an error to the audio loop, so it is reported here instead. A nil
	// onError drops the error silently. Set by [Conversation.Register] from
	// [WithErrorHandler].
	onError ErrorFunc
	// jobs carries flushed segments from the audio loop to the single transcription
	// worker (#24). ONE shared serial worker drains every lane's utterances, so STT
	// cost per utterance is unchanged vs single-lane (ADR-0050). Created and drained
	// per [Segmenter.Bind] lifetime; buffered deeply enough that a normal call never
	// backs the audio loop up on the send.
	jobs chan transcribeJob
	// inflight counts segments enqueued but not yet transcribed, so
	// [Segmenter.Flush] can wait for the backlog (incl. the final utterance) to
	// drain before the reactors tear down — otherwise the final STTFinal could fire
	// after its downstream subscribers are gone. worker tracks the worker goroutine
	// itself so Bind's cancel can stop it.
	inflight sync.WaitGroup
	worker   sync.WaitGroup
	// senders counts dispatchTranscription calls that have committed to a jobs send —
	// incremented under mu (only when NOT closed), Done after the send returns. Bind's
	// teardown flips closed under mu (blocking any new sender), then waits senders to
	// zero BEFORE close(jobs), so no goroutine can send on the closed channel (#343
	// residual 2 — the panic race the bare closed flag did not cover).
	senders sync.WaitGroup

	// laneVADFactory builds a fresh per-Speaker-Lane VAD on first frame from a new
	// speaker (ADR-0050). Nil = single-lane forever: every frame funnels to the
	// default lane, byte-identical to the pre-lane pipeline. Set via
	// [WithSpeakerLanes].
	laneVADFactory LaneVADFactory
	// laneStreamFactory builds a per-lane streaming-STT [StreamManager] at lane
	// creation, capped at maxStreamLanes concurrent lane streams (past the cap a
	// lane is pure batch). Nil = no per-lane streaming (the default lane keeps its
	// own [WithStreamingSTT] manager). Set via [WithLaneStreamingSTT].
	laneStreamFactory func(speakerID string) *StreamManager
	maxStreamLanes    int
	streamLanes       int // live non-default lane streams, for the cap

	// laneIdleTTL is how long a Speaker Lane may sit unused before the reap sweep
	// closes it (ADR-0050 "leave OR idle timeout"). nowFn is the injectable clock
	// the TTL is measured against (real time in prod); both are test-overridable via
	// [Segmenter.SetLaneReap]. sweepCalls counts Process calls toward the next sweep.
	laneIdleTTL time.Duration
	nowFn       func() time.Time
	sweepCalls  int
	sweepEvery  int // reap-sweep cadence in Process calls; [laneReapSweepEvery] in prod

	mu sync.Mutex
	// closed is the terminal teardown flag (same defect class as the #157 Manager
	// closed flag). Bind's returned cancel sets it FIRST under mu; laneFor and
	// dispatchTranscription check it. Once set, a still-running Process (an audio loop
	// that has not yet stopped) can no longer resurrect a reaped lane — its frames
	// funnel to the default lane — so every factory-built lane's ONNX session is closed
	// exactly once by teardown and none leaks (#343).
	closed bool
	bus    *voiceevent.Bus
	// ctx is the context handed to STT.Transcribe when a segment flushes and to each
	// per-lane stream's maintainer. It is the conversation's lifetime context,
	// captured at Bind and cleared by the returned cancel; storing it lets Process
	// stay frame-only (ctx-free) so the audio loop reads as a plain range over frames.
	ctx context.Context
	// lanes maps SpeakerID → its [lane]. The default lane (key "") is ALWAYS present,
	// wraps the ctor-supplied *VAD, and is never reaped nor factory-built — it is the
	// single-lane code path, byte-identical for an unattributed (Speaker()=="") frame.
	// laneOrder is the non-default lanes in first-seen order, so [Segmenter.Flush]
	// drains them deterministically after the default lane.
	lanes     map[string]*lane
	laneOrder []string
	// degraded records speakers whose lane VAD factory failed once: their frames fall
	// to the default lane for the rest of the session and the error is reported ONCE
	// (not per frame at ~31/s). Single-shot fail-closed — a bounded map keyed by the
	// session's participants (ADR-0050 risk (c)).
	degraded map[string]bool
}

// lane is one Speaker Lane (ADR-0050): a VAD session fed only its speaker's frame
// stream, emitting per-speaker utterance windows into the shared transcription
// worker. The default lane (SpeakerID "") wraps the ctor-supplied VAD; every other
// lane is factory-built on first frame and reaped on idle. All fields are guarded
// by the [Segmenter]'s mu except vad (driven from the audio loop) and closeVAD.
type lane struct {
	id       string // the lane's SpeakerID ("" = default lane)
	vad      *VAD
	closeVAD func() // releases the factory-built VAD's ONNX session; nil for the default lane

	listening bool
	buf       []audio.Frame
	// speechEndAt is the [voiceevent.VADSpeechEnd.At] of this lane's most recent
	// speech-end, carried onto the flushed utterance's STTFinal (A3).
	speechEndAt time.Time
	// curUtteranceID / pendCommit / pendCommitSentAt carry this lane's current
	// utterance's streaming state from the VAD callbacks to the flush (ADR-0042).
	curUtteranceID   string
	pendCommit       <-chan stt.CommitResult
	pendCommitSentAt time.Time
	// stream is this lane's optional streaming-STT transport; nil = pure batch.
	stream       *StreamManager
	streamCancel func()
	// lastSeen is the wall (nowFn) time of the last frame routed to this lane, for
	// idle-TTL reaping.
	lastSeen time.Time
}

// LaneVADFactory builds a fresh Speaker Lane VAD plus its close func (which
// releases the ONNX session so a reaped lane does not leak an inferencer, ADR-0050
// risk (b)). It returns an error the [Segmenter] degrades on: the speaker's frames
// fall back to the default lane rather than dropping.
type LaneVADFactory func() (v *VAD, close func(), err error)

// defaultLaneIdleTTL is how long a Speaker Lane may sit unused before the reap
// sweep closes it (ADR-0050 "leave OR idle timeout" — idle subsumes leave). Sized
// generously so a mid-session pause never reaps an active participant's lane.
const defaultLaneIdleTTL = 2 * time.Minute

// laneReapSweepEvery is the reap-sweep cadence, counted in [Segmenter.Process]
// calls — the same amortised-sweep precedent as the codec's pruneEvery, cheap
// enough to keep the sweep off the hot path's tail.
const laneReapSweepEvery = 1024

// transcribeJob is one flushed utterance handed to the transcription worker: the
// segment frames plus the per-segment state STT needs (the lifetime ctx, the turn's
// speech-end time, and the lane's SpeakerID stamped on the STTFinal), snapshotted at
// enqueue so the worker never reads mutable Segmenter/lane state.
//
// On the streaming path (ADR-0042) it additionally carries the utterance's manual
// commit handle (nil = pure batch), the moment that commit was sent (for the
// stt_request span), the utterance id (joining the STTFinal to its partials), and
// the lane's [StreamManager] (whose awaitCommit resolves the commit — carried on the
// job so a reaped lane's commit still resolves on the shared worker). The worker
// prefers the committed text and falls back to the batch recognizer — with these
// SAME locally-buffered seg frames — on any commit failure.
type transcribeJob struct {
	ctx          context.Context
	seg          []audio.Frame
	speechEndAt  time.Time
	commit       <-chan stt.CommitResult
	commitSentAt time.Time
	utteranceID  string
	speakerID    string
	stream       *StreamManager
}

// transcribeQueueDepth is the buffer on the worker's job channel. A flush enqueues
// at most one job per utterance, so this comfortably outlasts any real backlog
// (the recognizer keeps up with speech on average); a send only blocks the audio
// loop if this many transcriptions are outstanding, a pathological overload far
// past the inbound-buffer drop the decoupling exists to prevent.
const transcribeQueueDepth = 64

// NewSegmenter wires vad and stt together. Both must be non-nil; passing nil
// for either panics. The caller owns the wrapped stages. vad becomes the default
// lane (SpeakerID ""), which is always present and never reaped — so with no
// [WithSpeakerLanes] factory the segmenter is single-lane, byte-identical to the
// pre-lane pipeline.
func NewSegmenter(vad *VAD, stt *STT) *Segmenter {
	if vad == nil {
		panic("orchestrator.NewSegmenter: vad must not be nil")
	}
	if stt == nil {
		panic("orchestrator.NewSegmenter: stt must not be nil")
	}
	return &Segmenter{
		stt:         stt,
		laneIdleTTL: defaultLaneIdleTTL,
		nowFn:       time.Now,
		sweepEvery:  laneReapSweepEvery,
		lanes:       map[string]*lane{"": {id: "", vad: vad}},
		degraded:    map[string]bool{},
	}
}

// SetLaneReap overrides the idle-TTL and clock the reap sweep uses (test seam;
// production uses [defaultLaneIdleTTL] and time.Now). A non-positive ttl leaves the
// default; a nil now leaves the current clock.
func (s *Segmenter) SetLaneReap(ttl time.Duration, now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ttl > 0 {
		s.laneIdleTTL = ttl
	}
	if now != nil {
		s.nowFn = now
	}
}

// now reads the segmenter's clock (real time in prod, faked in reap tests). Callers
// must hold mu, or use it where a slightly stale read is harmless.
func (s *Segmenter) now() time.Time { return s.nowFn() }

// Bind subscribes the segmenter to the VAD speech transitions on bus and records
// ctx as the context handed to STT.Transcribe on flush. It implements
// [Reactor]; bus must be non-nil. The subscriptions only flip the speech-active
// flag — the actual buffering happens in [Segmenter.Process], which on speech-end
// hands the batch to a transcription worker goroutine (so the recognizer call
// never blocks the audio loop) and surfaces any recognizer error via onError.
func (s *Segmenter) Bind(ctx context.Context, bus *voiceevent.Bus) (cancel func()) {
	if bus == nil {
		panic("orchestrator.Segmenter.Bind: bus must not be nil")
	}
	s.mu.Lock()
	// Bind is re-usable: clear the terminal flag a prior teardown set, alongside the
	// other fresh-per-lifetime state (jobs/ctx/bus). Without this a re-Bound Segmenter
	// would silently funnel every frame to the default lane and transcribe inline on the
	// audio loop (#343 finding 3).
	s.closed = false
	s.ctx = ctx
	s.bus = bus
	jobs := make(chan transcribeJob, transcribeQueueDepth)
	s.jobs = jobs
	def := s.lanes[""]
	s.mu.Unlock()

	// Start the default lane's streaming transport (eager dial) so utterance 1 can
	// stream; a nil stream = no-op (byte-for-byte batch path). Non-default lanes bind
	// their own stream at lane creation (laneFor).
	if def.stream != nil {
		def.streamCancel = def.stream.bind(ctx, bus)
	}

	// One serial worker drains the queue, so transcriptions stay in speech order.
	s.worker.Go(func() {
		for job := range jobs {
			if err := s.transcribe(job); err != nil && s.onError != nil {
				s.onError(err)
			}
			s.inflight.Done()
		}
	})

	// The speech transitions of EVERY lane's VAD land on this one bus; route each to
	// its lane by SpeakerID ("" → default lane). A transition for an unknown lane (it
	// was reaped between the frame and the synchronous publish) is dropped.
	unsubStart := voiceevent.On(bus, func(start voiceevent.VADSpeechStart) {
		s.mu.Lock()
		ln := s.lanes[start.SpeakerID]
		if ln == nil {
			s.mu.Unlock()
			return
		}
		ln.listening = true
		stream := ln.stream
		s.mu.Unlock()
		// Mint this utterance's id and, if the stream is up, flush the pre-roll ring —
		// voiced frames then follow from Process (see [Segmenter.Process]).
		if stream != nil {
			id := stream.beginUtterance(start.At)
			s.mu.Lock()
			ln.curUtteranceID = id
			s.mu.Unlock()
		}
	})
	unsubEnd := voiceevent.On(bus, func(end voiceevent.VADSpeechEnd) {
		s.mu.Lock()
		ln := s.lanes[end.SpeakerID]
		if ln == nil {
			s.mu.Unlock()
			return
		}
		ln.listening = false
		// Remember this turn's true speech-end so the flushed utterance's STTFinal
		// can carry it (A3); the next Process call for this lane flushes.
		ln.speechEndAt = end.At
		stream := ln.stream
		s.mu.Unlock()
		// Local VAD is the sole endpointing authority (ADR-0042): the manual commit
		// fires HERE, at the VAD speech-end, never provider-side. The handle is stashed
		// for the imminent flush to attach to the job.
		if stream != nil {
			commit, sentAt, ok := stream.endUtterance()
			s.mu.Lock()
			if ok {
				ln.pendCommit = commit
				ln.pendCommitSentAt = sentAt
			} else {
				ln.pendCommit = nil
			}
			s.mu.Unlock()
		}
	})
	return func() {
		unsubStart()
		unsubEnd()
		// Tear down every lane's stream and every FACTORY-built lane's VAD (the ONNX
		// session — ADR-0050 risk (b)). The default lane's VAD is caller-owned and is
		// NOT closed here. Under mu we CAPTURE each lane's cancel/close funcs, null them,
		// and drop the non-default lanes from the map — so a concurrent reap sweep (still
		// on the audio-loop goroutine) can never double-close the same lane (finding 6).
		// The captured funcs are then invoked outside the lock.
		type teardown struct {
			streamCancel func()
			closeVAD     func()
		}
		s.mu.Lock()
		// Flip the terminal flag FIRST (mirrors #157: Shutdown sets closed before it
		// unwinds), so a Process racing this teardown sees closed in laneFor and cannot
		// re-open a lane we are about to close — no resurrection, no leaked ONNX session.
		s.closed = true
		tds := make([]teardown, 0, len(s.lanes))
		for id, ln := range s.lanes {
			tds = append(tds, teardown{streamCancel: ln.streamCancel, closeVAD: ln.closeVAD})
			ln.streamCancel = nil
			ln.closeVAD = nil
			if id != "" {
				delete(s.lanes, id)
			}
		}
		s.laneOrder = nil
		s.streamLanes = 0
		s.ctx = nil
		s.jobs = nil
		s.mu.Unlock()
		for _, td := range tds {
			if td.streamCancel != nil {
				td.streamCancel()
			}
			if td.closeVAD != nil {
				td.closeVAD()
			}
		}
		// Wait for every dispatch that enlisted as a sender (under mu, before closed was
		// set) to finish its send, THEN close — so close(jobs) can never race a live
		// `jobs <- job` into a panic (#343 residual 2). New dispatches see closed and go
		// inline, so this barrier drains a bounded, non-growing set.
		s.senders.Wait()
		// Closing the queue lets the worker drain any still-buffered jobs and exit;
		// in the normal path Flush already emptied it, so this just stops the worker.
		close(jobs)
		s.worker.Wait()
	}
}

// Process feeds one PCM frame through the wrapped VAD stage and accumulates
// utterance audio. The VAD stage publishes the speech transitions synchronously,
// so by the time it returns the speech-active flag is up to date: while active
// the frame is buffered; on the first frame after speech ends the buffered
// utterance is handed to [STT.Transcribe] on a worker goroutine. The frame that
// ends speech is not part of the utterance and is not buffered.
//
// The transcription runs OFF the audio loop (#24): the recognizer call is
// network-bound (~1-2s) and running it inline here would stall the inbound loop,
// so frames arriving during that window would be dropped at the bounded inbound
// buffer and whole utterances lost. Process therefore returns as soon as the
// segment is handed off, keeping the loop draining; only a VAD error is returned.
// The buffer is cleared synchronously so a failed utterance does not bleed into
// the next, and a recognizer error surfaces via onError, not this return value.
func (s *Segmenter) Process(frame audio.Frame) error {
	s.reapIdleLanes()

	sp := frame.Speaker()
	if sp == "" {
		// Unattributed INBOUND audio (an unresolved SSRC still voiced, a mixed/test
		// frame): DEFAULT LANE ONLY — byte-identical to the pre-lane single-lane path,
		// no re-stamp, barge key "" only. It transcribes unattributed
		// (STTFinal.SpeakerID == "" → Butler fail-closed) and NEVER touches a Speaker
		// Lane, so a not-yet-resolved speaker can never phantom-misattribute onto
		// someone else's lane. The silence CLOCK uses [Segmenter.ProcessSilence]
		// instead — the caller distinguishes the two "" sources at source (ADR-0050).
		s.mu.Lock()
		def := s.lanes[""]
		s.mu.Unlock()
		return s.processLane(def, frame)
	}

	// Attributed frame: route to its Speaker Lane, created on first frame. A nil or
	// (once) erroring factory degrades to the default lane (still transcribed) and
	// reports the error ONCE — the audio loop stays up (ADR-0050 risk (c)).
	ln, err := s.laneFor(sp)
	if err != nil && s.onError != nil {
		s.onError(err)
	}
	// On the degrade path the lane is the default ("") but the frame is still stamped
	// sp; re-stamp it to the lane's id so its VAD transition routes back to the default
	// lane (and its STTFinal.SpeakerID is "" → Butler fail-closed). The happy path is a
	// no-op (frame already == lane id).
	f := frame
	if f.Speaker() != ln.id {
		f = f.WithSpeaker(ln.id)
	}
	return s.processLane(ln, f)
}

// ProcessSilence feeds one silence-CLOCK frame (issue #91) to EVERY lane so each
// lane's VAD hangover advances toward its speech_end — the silence clock is
// speaker-agnostic (ADR-0050). It is the [Segmenter.Process] sibling for the ONE
// unattributed source that must reach every lane; the caller (the wire tick branch)
// routes clock frames here and real inbound audio through Process, so the two ""
// sources are distinguished at source rather than by sniffing zero PCM (Opus can
// legally decode an all-zero frame). Each lane's copy is re-stamped to that lane's id
// so the resulting speech_end routes back to it; zero PCM never onsets speech, so a
// silence frame only ever ADVANCES a listening lane, never opens one. A silence frame
// DOES enter a listening lane's buffer (parity with the pre-lane single-lane path,
// where clock frames were buffered). It also drives the reap sweep, so a fully-idle
// table still ages out departed speakers' lanes.
func (s *Segmenter) ProcessSilence(frame audio.Frame) error {
	s.reapIdleLanes()

	s.mu.Lock()
	order := append([]string{""}, s.laneOrder...)
	lanes := make([]*lane, 0, len(order))
	for _, id := range order {
		if ln := s.lanes[id]; ln != nil {
			lanes = append(lanes, ln)
		}
	}
	s.mu.Unlock()

	var firstErr error
	for _, ln := range lanes {
		f := frame
		if ln.id != "" {
			f = frame.WithSpeaker(ln.id)
		}
		if err := s.processLane(ln, f); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// processLane drives one lane's VAD with the (already correctly-stamped) frame and
// accumulates its utterance audio, mirroring the original single-lane body per lane.
// The VAD publishes its transitions synchronously, so ln.listening is up to date by
// the time this returns from vad.Process. It does NOT refresh ln.lastSeen: idle-reap
// aging keys off attributed frames (via [Segmenter.laneFor]) only, so a lane still
// ages while the speaker-agnostic silence clock ticks (risk: reap dead in prod).
func (s *Segmenter) processLane(ln *lane, f audio.Frame) error {
	if err := ln.vad.Process(f); err != nil {
		return err
	}

	s.mu.Lock()
	if ln.listening {
		ln.buf = append(ln.buf, f)
		stream := ln.stream
		s.mu.Unlock()
		// Mirror the voiced frame onto the lane's stream (additive; the local buffer
		// above stays authoritative for the batch fallback). No-op when no stream.
		if stream != nil {
			stream.send(f)
		}
		return nil
	}
	seg := ln.buf
	ln.buf = nil
	ctx := s.ctx
	speechEndAt := ln.speechEndAt
	commit := ln.pendCommit
	commitSentAt := ln.pendCommitSentAt
	utteranceID := ln.curUtteranceID
	stream := ln.stream
	ln.pendCommit = nil
	s.mu.Unlock()

	// A non-listening frame is pre-speech silence: fill the pre-roll ring (never
	// streamed until the next utterance begins, so silence is not billed).
	if stream != nil {
		stream.idleFrame(f)
	}
	s.dispatchTranscription(ctx, seg, speechEndAt, commit, commitSentAt, utteranceID, ln.id, stream)
	return nil
}

// laneFor returns the Speaker Lane a speaker's frames feed, creating it on first
// sight. With no [WithSpeakerLanes] factory (or a "" speaker) it funnels to the
// default lane. A factory error — or a panic (ADR-0050 risk (c)) — is returned and
// the frames degrade to the default lane; the returned *lane is never nil.
func (s *Segmenter) laneFor(sp string) (*lane, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// A speaker whose factory already failed once is degraded for the session: skip
	// the retry AND the repeat onError (finding 3 — the factory was retried and
	// onError fired ~31×/s before this). Once teardown has flipped closed, funnel to
	// the default lane too — a stopping audio loop must not resurrect a reaped lane and
	// leak a fresh ONNX session teardown will never close (#343).
	if sp == "" || s.laneVADFactory == nil || s.degraded[sp] || s.closed {
		return s.lanes[""], nil
	}
	if ln, ok := s.lanes[sp]; ok {
		ln.lastSeen = s.now()
		return ln, nil
	}
	v, closeVAD, err := s.newLaneVAD()
	if err != nil {
		s.degraded[sp] = true // single-shot fail-closed: report once, then default silently
		return s.lanes[""], err
	}
	ln := &lane{id: sp, vad: v, closeVAD: closeVAD, lastSeen: s.now()}
	// Per-lane streaming under the concurrency cap (ADR-0050): the first maxStreamLanes
	// lanes open a stream at creation (bound to the conversation ctx, reaped with the
	// lane); past the cap a lane is pure batch. Only when a factory is wired AND the
	// conversation is bound (s.ctx set).
	if s.laneStreamFactory != nil && s.streamLanes < s.maxStreamLanes && s.ctx != nil && s.bus != nil {
		if sm := s.laneStreamFactory(sp); sm != nil {
			ln.stream = sm
			ln.streamCancel = sm.bind(s.ctx, s.bus)
			s.streamLanes++
		}
	}
	s.lanes[sp] = ln
	s.laneOrder = append(s.laneOrder, sp)
	return ln, nil
}

// newLaneVAD calls the lane VAD factory, converting a panic into an error so a
// misbehaving factory degrades one speaker to the default lane rather than taking
// the audio loop down (ADR-0050 risk (c)). Called under mu.
func (s *Segmenter) newLaneVAD() (v *VAD, closeVAD func(), err error) {
	defer func() {
		if r := recover(); r != nil {
			v, closeVAD, err = nil, nil, fmt.Errorf("orchestrator.Segmenter: lane VAD factory panicked: %v", r)
		}
	}()
	return s.laneVADFactory()
}

// reapIdleLanes closes every non-default lane idle past laneIdleTTL, on the
// amortised [laneReapSweepEvery] cadence (ADR-0050 lane lifecycle). A reaped lane's
// still-buffered utterance is FLUSHED (dispatched, not dropped), its stream cancelled
// and its VAD's ONNX session closed (risk (b)). The default lane is never reaped.
func (s *Segmenter) reapIdleLanes() {
	s.mu.Lock()
	s.sweepCalls++
	if s.sweepEvery <= 0 || s.sweepCalls < s.sweepEvery {
		s.mu.Unlock()
		return
	}
	s.sweepCalls = 0
	cutoff := s.now().Add(-s.laneIdleTTL)
	ctx := s.ctx

	// Snapshot everything a reaped lane needs — the flush state AND its cancel/close
	// funcs — UNDER the lock, nulling the funcs and dropping the lane, so a concurrent
	// Bind teardown can never also close it (finding 6). The heavy work (dispatch +
	// close) then runs outside the lock.
	type reaped struct {
		id           string
		seg          []audio.Frame
		speechEndAt  time.Time
		commit       <-chan stt.CommitResult
		commitSentAt time.Time
		utteranceID  string
		stream       *StreamManager
		streamCancel func()
		closeVAD     func()
	}
	var reap []reaped
	for id, ln := range s.lanes {
		if id == "" || !ln.lastSeen.Before(cutoff) {
			continue
		}
		reap = append(reap, reaped{
			id:           ln.id,
			seg:          ln.buf,
			speechEndAt:  ln.speechEndAt,
			commit:       ln.pendCommit,
			commitSentAt: ln.pendCommitSentAt,
			utteranceID:  ln.curUtteranceID,
			stream:       ln.stream,
			streamCancel: ln.streamCancel,
			closeVAD:     ln.closeVAD,
		})
		ln.buf = nil
		ln.pendCommit = nil
		ln.streamCancel = nil
		ln.closeVAD = nil
		if ln.stream != nil {
			s.streamLanes--
		}
		delete(s.lanes, id)
		s.removeLaneOrder(id)
	}
	s.mu.Unlock()

	for _, r := range reap {
		// Flush a still-buffered utterance so a reaped mid-utterance lane is not lost;
		// dispatchTranscription is a no-op on an empty buffer.
		s.dispatchTranscription(ctx, r.seg, r.speechEndAt, r.commit, r.commitSentAt, r.utteranceID, r.id, r.stream)
		if r.streamCancel != nil {
			r.streamCancel()
		}
		if r.closeVAD != nil {
			r.closeVAD()
		}
	}
}

// removeLaneOrder drops id from the first-seen order slice. Called under mu.
func (s *Segmenter) removeLaneOrder(id string) {
	for i, v := range s.laneOrder {
		if v == id {
			s.laneOrder = append(s.laneOrder[:i], s.laneOrder[i+1:]...)
			return
		}
	}
}

// dispatchTranscription enqueues a flushed segment for the transcription worker so
// the audio intake loop is never blocked by the network-bound recognizer call
// (#24); an empty segment is a no-op. The enqueue is counted on inflight so
// [Segmenter.Flush] can drain the backlog before teardown. Each segment is owned
// by the job (processLane cleared the lane's buf before handing it over, so a
// subsequent append cannot mutate it) and mints its own TurnID at STTFinal (stt.go).
// The send blocks
// only if the queue is full (see [transcribeQueueDepth]). A recognizer error is
// reported by the worker through onError — the side channel that replaces the
// inline call's return value.
func (s *Segmenter) dispatchTranscription(ctx context.Context, seg []audio.Frame, speechEndAt time.Time, commit <-chan stt.CommitResult, commitSentAt time.Time, utteranceID, speakerID string, stream *StreamManager) {
	if len(seg) == 0 {
		return
	}
	job := transcribeJob{
		ctx:          ctx,
		seg:          seg,
		speechEndAt:  speechEndAt,
		commit:       commit,
		commitSentAt: commitSentAt,
		utteranceID:  utteranceID,
		speakerID:    speakerID,
		stream:       stream,
	}
	// Decide-and-enlist under mu: if teardown has begun (closed) or we were never bound
	// (jobs nil), transcribe inline; otherwise register as a pending sender BEFORE
	// releasing the lock, so teardown's senders.Wait() cannot race past us to close the
	// channel we are about to send on (#343 residual 2). The Add MUST be under the same
	// lock that sets closed, or the barrier has a hole.
	s.mu.Lock()
	jobs := s.jobs
	if s.closed || jobs == nil {
		s.mu.Unlock()
		// Not bound, or teardown in progress: transcribe inline so a late flush still
		// completes rather than dropping the utterance (or panicking on a closing chan).
		if err := s.transcribe(job); err != nil && s.onError != nil {
			s.onError(err)
		}
		return
	}
	s.senders.Add(1)
	s.mu.Unlock()
	defer s.senders.Done()
	s.inflight.Add(1)
	jobs <- job
}

// Flush transcribes any buffered utterance audio immediately, regardless of
// whether speech is still active, then waits for every in-flight transcription to
// complete. It is the end-of-stream counterpart to [Segmenter.Process]: when the
// audio loop stops while the speaker is still mid-utterance (the call ends, a clip
// is cut off before its trailing silence), the wrapped VAD never observes a
// speech-end transition, so the buffered final utterance would otherwise be
// dropped. Call Flush once after the last [Segmenter.Process], before tearing the
// reactors down.
//
// Because transcription is now off-loop (see [Segmenter.Process]), Flush is also
// the drain barrier: it blocks until the worker has transcribed every queued
// utterance (including the final one) and published its STTFinal, so the final
// turn's downstream stages run while their subscribers are still bound. With no
// buffered audio and nothing in flight it is a no-op. Recognizer errors surface
// via onError, so Flush always returns nil (the error return is retained for the
// audio loop's call-site symmetry).
func (s *Segmenter) Flush() error {
	// Default lane first, then the non-default lanes in first-seen order, so the
	// end-of-stream drain is deterministic (positional cassette replay relies on
	// speech order within a lane; across lanes the default lane leads).
	s.mu.Lock()
	order := append([]string{""}, s.laneOrder...)
	lanes := make([]*lane, 0, len(order))
	for _, id := range order {
		if ln := s.lanes[id]; ln != nil {
			lanes = append(lanes, ln)
		}
	}
	s.mu.Unlock()

	for _, ln := range lanes {
		s.flushLane(ln)
	}
	s.inflight.Wait()
	return nil
}

// flushLane dispatches one lane's still-buffered utterance regardless of whether
// speech is still active (the end-of-stream counterpart to [Segmenter.processLane]).
func (s *Segmenter) flushLane(ln *lane) {
	s.mu.Lock()
	seg := ln.buf
	ln.buf = nil
	wasListening := ln.listening
	ln.listening = false
	ctx := s.ctx
	commit := ln.pendCommit
	commitSentAt := ln.pendCommitSentAt
	utteranceID := ln.curUtteranceID
	stream := ln.stream
	ln.pendCommit = nil
	s.mu.Unlock()

	// If the stream ended while speech was still active there was no VAD speech-end,
	// so the open utterance never got its manual commit — request it now (ADR-0042),
	// or fall to batch if the stream is down. A speech-end already emitted a handle
	// captured above, so this only fires for a truly mid-utterance end-of-stream.
	if wasListening && stream != nil {
		if c, sentAt, ok := stream.endUtterance(); ok {
			commit = c
			commitSentAt = sentAt
		}
	}

	// A Flush has no speech-end transition (end-of-stream), so it carries the
	// zero time — the STTFinal's SpeechEndAt is unset for a flushed final turn.
	s.dispatchTranscription(ctx, seg, time.Time{}, commit, commitSentAt, utteranceID, ln.id, stream)
}

// transcribe produces one utterance's authoritative STTFinal, carrying the turn's
// speech-end time and (on the streaming path) utterance id via ctx so STT stamps
// them on the published event (A3, ADR-0042). An empty segment is a no-op (a
// speech-end with nothing buffered, or a redundant Flush). A nil ctx — the
// segmenter was never bound, or was already torn down — falls back to a background
// context so a late flush still completes rather than panicking.
//
// When the utterance streamed (job.commit != nil), it waits for the manual commit
// and publishes the committed text directly, skipping the batch POST. On ANY commit
// error or timeout it falls back to the batch recognizer with these SAME
// locally-buffered frames — streaming is additive, never authoritative — so no
// utterance is lost when the stream degrades.
func (s *Segmenter) transcribe(job transcribeJob) error {
	if len(job.seg) == 0 {
		return nil
	}
	ctx := job.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = withSpeechEndAt(ctx, job.speechEndAt)
	ctx = withUtteranceID(ctx, job.utteranceID)
	ctx = withSpeakerID(ctx, job.speakerID)

	if job.commit != nil && job.stream != nil {
		if t, ok := job.stream.awaitCommit(job.commit, job.commitSentAt); ok {
			// Committed text is authoritative: publish it as this utterance's STTFinal
			// with the SAME TurnID minting the batch path uses.
			s.stt.PublishFinal(ctx, t)
			return nil
		}
		// Commit errored or timed out: degrade to the batch adapter below.
	}
	return s.stt.Transcribe(ctx, job.seg)
}

// Reply is one thing an addressed Agent should say: a single sentence and the
// Voice to render it with. A [ReplyFunc] returns zero or more Replies per
// routing decision.
type Reply struct {
	Sentence string
	Voice    tts.Voice

	// OnDelivered, when non-nil, is the producer's per-Reply commit hook
	// (deliver-then-commit at the granularity of the Replies the [ReplyFunc]
	// returns). The [turnRun] module (#444) — which every dispatch site delegates
	// to — invokes it EXACTLY ONCE iff this Reply is delivered: the ADR-0012
	// commit point, synth returned nil AND the post-drain ctx is still live. A
	// start-error or a mid-drain cut leaves it uninvoked, so an undelivered
	// sentence is never committed. Nil is a no-op. (The Ensemble producers —
	// [EnsembleSpeaker.Speak] / [CrossTalker.SpeakReaction] — commit via their
	// own return-value drain and pass hook-less Replies, so they simply never
	// set it.)
	OnDelivered func()
}

// ReplyFunc decides what an addressed Agent says in response to one
// [voiceevent.AddressRouted] decision. Returning nil (or an empty slice) says
// nothing — the right answer when the route is not for this Agent or the turn
// has already been answered. Swapping the ReplyFunc swaps the pipeline's entire
// "what do we say back" behaviour without touching any other stage: it is the
// strategy seam of the reply reactor.
//
// ctx is the turn's context: with barge-in wired ([WithBargeIn]) it is the
// per-turn floor context, so a human reclaiming the floor cancels any LLM call
// in flight, not just the TTS/playback downstream of it. Implementations must
// honor it (and should derive their own deadline — a hung provider must not
// hold the turn forever).
//
// In v1.0 the production ReplyFunc is the Agent loop (Hot Context assembly +
// Persona injection + LLM dispatch, ADR-0019 slice 1); tests supply a closure
// returning a canned line. Per ADR-0025 a multi-Agent address can yield an
// Ensemble Turn — the slice return type leaves room for that to grow behind the
// same seam.
type ReplyFunc func(ctx context.Context, e voiceevent.AddressRouted) []Reply

// StreamReplyFunc is the streaming counterpart to [ReplyFunc] (B1): instead of
// returning all of a turn's [Reply]s up front, it produces them incrementally —
// calling dispatch with each sentence the moment it is ready — so the first
// sentence reaches TTS (and audio begins) before the whole completion is
// generated. dispatch sends one sentence through the TTS stage and blocks until
// that sentence is synthesized (the serial, one-at-a-time contract the
// [PlaybackPump] depends on).
//
// dispatch's return is the deliver-then-commit signal (ADR-0012, #362): classify
// it with [OutcomeOf] — [SentenceDelivered] (commit it), [SentenceNotDelivered]
// (skip it, keep producing), or [SentenceCut] (stop producing, commit nothing
// more). The dispatch callback is the [turnRun] module's (#444), which owns the
// ctx-timing and error mapping behind that contract.
//
// ctx is the per-turn context (the barge-in floor's, under [WithBargeIn]); the
// producer must thread it into its LLM call so a cancel tears generation down.
// Returning an error reports a turn-level failure via the [ErrorFunc]; a nil
// error with no dispatch calls says nothing.
type StreamReplyFunc func(ctx context.Context, e voiceevent.AddressRouted, dispatch func(Reply) error) error

// Replier is the [Reactor] that runs a reply strategy on every
// [voiceevent.AddressRouted] and dispatches each resulting [Reply] through the
// TTS stage. It drives the streaming strategy ([StreamReplyFunc]) when one is
// set, else the batch [ReplyFunc].
type Replier struct {
	tts         *TTS
	reply       ReplyFunc
	replyStream StreamReplyFunc
	onError     ErrorFunc

	// floor, when non-nil, makes each turn run on its own goroutine under a
	// cancelable per-turn context taken from the floor — so the inbound loop is
	// not blocked for the turn's real-time playback and a [BargeIn] can cancel it
	// mid-sentence (ADR-0027). Nil keeps the default synchronous dispatch. Set
	// only via the orchestrator wiring ([WithBargeIn]); not part of [NewReplier].
	floor *Floor

	// mutes, when non-nil, is the live mute view (#211, [Barge.Mutes]): a route whose
	// target Agent is muted is discarded before the floor is taken, so an
	// addressed-but-muted Agent opens no turn — no floor churn, no LLM call, no
	// TTS, no transcript line, and it can never supersede whoever holds the floor
	// (AC3). Set only via the orchestrator wiring; not part of [NewReplier].
	mutes MuteView

	// gate, when non-nil, is the live turn gate (#130, [Barge.Gate]): once the
	// session's estimated spend crosses the soft cap it refuses a NEW turn before
	// the floor is taken — exactly like a muted route, but for the whole session. A
	// SINGLE pre-check suffices (spend is monotonic, so no post-Take re-check like
	// the mute race closure). Set only via the orchestrator wiring; not part of
	// [NewReplier].
	gate TurnGate

	// ensemble, when non-nil, is the Ensemble Turn speaker ([Barge.Ensemble], #301,
	// ADR-0025): on a [voiceevent.EnsembleRouted] the replier fans the candidates
	// out into parallel [EnsembleSpeaker.Draft]s, races them, and lets the first
	// complete non-empty draft (the Lead) take the floor and speak. Nil degrades an
	// EnsembleRouted to the top-scored single route ([Replier.handleRouted]). Set
	// only via the orchestrator wiring; not part of [NewReplier]. It requires the
	// barge-in floor (it lives on [Barge], so it cannot exist without one) — the whole
	// ensemble is ONE floor-holding unit.
	ensemble EnsembleSpeaker

	// lookahead, when non-nil, is the pump pre-render seam ([Barge.Lookahead],
	// #375, ADR-0025): the Cross-talk Reaction's first sentence is synthesized during
	// the Lead's playback and HELD in this pump's look-ahead lane, released at the
	// Lead's end for a near-zero onset gap (or discarded on a barge). Nil is the
	// feature-off default — the Reaction pre-renders TEXT only (the #302 legacy
	// path). Set only via the orchestrator wiring; not part of [NewReplier].
	lookahead LookaheadPump
}

// NewReplier wires ttsStage and reply together. Both must be non-nil; passing
// nil for either panics. onError may be nil, in which case a [TTS.Dispatch]
// failure is dropped silently.
func NewReplier(ttsStage *TTS, reply ReplyFunc, onError ErrorFunc) *Replier {
	if ttsStage == nil {
		panic("orchestrator.NewReplier: tts must not be nil")
	}
	if reply == nil {
		panic("orchestrator.NewReplier: reply must not be nil")
	}
	return &Replier{tts: ttsStage, reply: reply, onError: onError}
}

// NewStreamReplier wires ttsStage and a streaming reply strategy together (B1).
// Both must be non-nil; onError may be nil. It is the streaming twin of
// [NewReplier]: the strategy dispatches sentences as they are produced rather
// than returning them all at once.
func NewStreamReplier(ttsStage *TTS, reply StreamReplyFunc, onError ErrorFunc) *Replier {
	if ttsStage == nil {
		panic("orchestrator.NewStreamReplier: tts must not be nil")
	}
	if reply == nil {
		panic("orchestrator.NewStreamReplier: reply must not be nil")
	}
	return &Replier{tts: ttsStage, replyStream: reply, onError: onError}
}

// Bind subscribes the replier to [voiceevent.AddressRouted] AND
// [voiceevent.EnsembleRouted] on bus and returns a function that removes both
// subscriptions. It implements [Reactor]; bus must be non-nil. Each [Reply] the
// [ReplyFunc] returns is dispatched in order under ctx; a dispatch failure is
// reported through the ErrorFunc (callbacks cannot return errors) and does not stop
// the remaining replies. An EnsembleRouted runs the speculative fan-out + Lead race
// ([Replier.handleEnsemble], #301) when an [EnsembleSpeaker] is wired, else degrades
// to the top-scored single route.
func (r *Replier) Bind(ctx context.Context, bus *voiceevent.Bus) (cancel func()) {
	if bus == nil {
		panic("orchestrator.Replier.Bind: bus must not be nil")
	}
	// The shared floor publishes the cut turn's TurnEnded{superseded} when a Take
	// supersedes a live holder (#443).
	if r.floor != nil {
		r.floor.bindSupersedeTerminal(bus)
	}
	unsubRouted := voiceevent.On(bus, func(e voiceevent.AddressRouted) {
		r.handleRouted(ctx, bus, e)
	})
	unsubEnsemble := voiceevent.On(bus, func(e voiceevent.EnsembleRouted) {
		r.handleEnsemble(ctx, bus, e)
	})
	return func() {
		unsubEnsemble()
		unsubRouted()
	}
}

// handleRouted runs one single-target routing decision: the pre-#301 reply-reactor
// body, unchanged. It is a method (not an inline closure) so [Replier.handleEnsemble]
// can degrade a one-survivor / no-ensemble-speaker EnsembleRouted onto it.
func (r *Replier) handleRouted(ctx context.Context, bus *voiceevent.Bus, e voiceevent.AddressRouted) {
	{
		// Carry the turn correlation id (A3) into the dispatch context so the TTS
		// stage and the wire tee stamp the same id on TTSInvoked / FirstAudio.
		// Installed before the floor is taken so both the sync and barge-in
		// branches inherit it.
		ctx := voiceevent.WithTurnID(ctx, e.TurnID)

		// Default (no floor): dispatch synchronously on the bus goroutine — the
		// behaviour every non-barge-in caller relies on. Announce a turn that died of
		// its own error (a real TTS/provider failure) before producing audio so the
		// metrics subscriber records the precise reason, not the coarse no-first-audio
		// TTL reap (#20) — mirroring the floor branch below. The sync path has no
		// barge/supersede, so a non-cancelled ctx is the only guard needed. NOTE: the
		// mute gate (#211, #225) below lives in the floor branch only. Since #225 the
		// matcher keeps a muted addressee routable by name (silencing is the reactor
		// gate's job, not the matcher's), so a route CAN name a muted Agent. Prod
		// always wires the barge-in floor together with the MuteView — mute needs the
		// floor to cut a speaker — so that gate always runs; this no-floor path is
		// reached only by configs wiring neither, and relies on that pairing rather
		// than on the matcher de-routing muted Agents.
		if r.floor == nil {
			if reason := r.dispatchAll(ctx, e); reason != "" && ctx.Err() == nil {
				bus.Publish(voiceevent.TurnEnded{At: time.Now(), TurnID: e.TurnID, Reason: reason})
			}
			return
		}
		// Muted addressee (#211, AC3): discard the route BEFORE taking the floor, so
		// a muted Agent opens no turn (no floor churn, no LLM call, no TTS, no
		// transcript line) and can never supersede whoever holds the floor. The
		// Manager writes its mute set before publishing MuteChanged, so this read is
		// authoritative. Announce a mute-reason TurnEnded for the routed TurnID so
		// the metrics subscriber records the precise cause.
		if r.mutes != nil && r.mutes.Muted(e.Target.AgentID) {
			bus.Publish(voiceevent.TurnEnded{At: time.Now(), TurnID: e.TurnID, Reason: voiceevent.TurnEndMute})
			return
		}
		// Spend soft cap (#130, AC3): once the session's estimated spend crosses the
		// soft cap the gate refuses a NEW turn — discard the route BEFORE taking the
		// floor, so no floor churn, no LLM call, no TTS, no transcript line, and it
		// never supersedes whoever holds the floor. A SINGLE pre-check is airtight:
		// spend is monotonic, so unlike mute the gate can never flip back to allowed
		// between here and the Take. Announce a spend_cap-reason TurnEnded for the
		// routed TurnID so the metrics subscriber records the precise cause.
		if r.gate != nil && !r.gate.AllowTurn() {
			bus.Publish(voiceevent.TurnEnded{At: time.Now(), TurnID: e.TurnID, Reason: voiceevent.TurnEndSpendCap})
			return
		}
		// Barge-in: take the floor and run the turn on its own goroutine so the
		// inbound loop keeps feeding VAD during playback. A barge cancels turnCtx,
		// which unwinds TTS synthesis and playback and breaks the dispatch loop.
		// The take carries the route's target agent so the floor's coalesce window
		// only folds same-target re-takes (#146): a segment routed to a DIFFERENT
		// agent inside the window ("Bart, hold the door. Greta, run!") supersedes
		// instead of vanishing.
		turnCtx, release, coalesced := r.floor.Take(ctx, e.Target.AgentID)
		if coalesced {
			// The floor's same-utterance grace window folded this late same-target
			// segment into the turn already speaking (one utterance VAD-split in
			// two). The segment is NOT spoken; announce it so the metrics subscriber
			// records a distinct `yielded` outcome (not `abandoned`) and logs the
			// dropped transcript — the known residual until real utterance
			// coalescing routes this text into the surviving turn. No goroutine is
			// spawned.
			release() // no-op on the floor, but keeps the take/release pairing honest
			bus.Publish(voiceevent.TurnEnded{At: time.Now(), TurnID: e.TurnID, Reason: voiceevent.TurnEndSupersedeCoalesced, Text: e.Text})
			return
		}
		// Race closure (#211): the mute view can flip to muted between the pre-Take
		// check and this Take (a Discord/web mute landing exactly then). Re-check now
		// that this turn holds the floor: if muted, release it and end the turn with
		// the mute reason before any goroutine, TTS or transcript — airtight because
		// the Manager writes the set before publishing MuteChanged.
		if r.mutes != nil && r.mutes.Muted(e.Target.AgentID) {
			release()
			bus.Publish(voiceevent.TurnEnded{At: time.Now(), TurnID: e.TurnID, Reason: voiceevent.TurnEndMute})
			return
		}
		go func() {
			defer release()
			reason := r.dispatchAll(turnCtx, e)
			// Announce a turn that died of its own error (a real TTS/provider
			// failure) before producing audio — so the metrics subscriber records
			// the precise reason, not the coarse no-first-audio catch-all. A turn
			// cancelled by a barge or a supersede is NOT reported here (ctx.Err() !=
			// nil): the barge publishes its own TurnEnded, and the subscriber's
			// first-audio/TTL guards handle the rest. A clean turn reports nothing.
			if reason != "" && turnCtx.Err() == nil {
				bus.Publish(voiceevent.TurnEnded{At: time.Now(), TurnID: e.TurnID, Reason: reason})
			}
		}()
	}
}

// dispatchAll renders one routing decision under ctx, returning the turn-end
// reason if the turn failed of its own error (empty on a clean turn or a
// ctx-cancel). With a streaming strategy it drives the producer, dispatching each
// sentence as it arrives; otherwise it renders every [Reply] the batch
// [ReplyFunc] returns, in order. Both delegate the deliver-then-commit protocol
// to the [turnRun] module (#444) and stop early if ctx is cancelled (a barge-in
// yielded the floor mid-turn).
func (r *Replier) dispatchAll(ctx context.Context, e voiceevent.AddressRouted) voiceevent.TurnEndReason {
	if r.replyStream != nil {
		return r.dispatchStream(ctx, e)
	}
	t := r.newTurn(ctx)
	for _, rep := range r.reply(ctx, e) {
		// A start-error skips the sentence but keeps draining (the sticky
		// ttsFailed becomes the terminal reason via finish); a cut stops the loop
		// with no reason of its own (the cutter publishes its own TurnEnded).
		if OutcomeOf(t.dispatch(rep)) == SentenceCut {
			return ""
		}
	}
	return t.finish(nil)
}

// dispatchStream drives the streaming reply strategy for one routing decision
// (B1), returning the turn-end reason if the turn failed of its own error (empty
// on a clean turn or a ctx-cancel). It hands the producer the [turnRun]'s
// dispatch callback, which synthesizes one sentence at a time under ctx —
// serially, so the [PlaybackPump]'s single-in-flight contract and the
// per-sentence FirstAudio ordering both hold — and the module's finish maps the
// producer's return to the terminal reason.
func (r *Replier) dispatchStream(ctx context.Context, e voiceevent.AddressRouted) voiceevent.TurnEndReason {
	t := r.newTurn(ctx)
	return t.finish(r.replyStream(ctx, e, t.dispatch))
}
