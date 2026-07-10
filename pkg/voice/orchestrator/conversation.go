package orchestrator

import (
	"context"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// Conversation bundles the slice-1 reactive wiring behind one façade: the
// segmenter (VAD speech transitions → STT), an optional address detector
// (STTFinal → AddressRouted), and an optional replier (AddressRouted → TTS).
// It is the "all at once" knob — [Conversation.Register] installs the whole
// reactive layer with a single call and [Conversation.Feed] is the audio loop's
// entry point. To change one interaction in isolation, drop to the [Reactor]
// layer and compose with [Bind] instead (ADR-0026).
//
// Behaviour is configured with functional options at construction
// ([WithDetector], [WithReply], [WithErrorHandler]); the stages themselves are
// supplied by the caller, who owns their lifetime.
type Conversation struct {
	bus *voiceevent.Bus
	tts *TTS

	seg         *Segmenter
	detector    *AddressDetector
	reply       ReplyFunc
	replyStream StreamReplyFunc
	onError     ErrorFunc

	// floor is non-nil when barge-in is enabled ([WithBargeIn]): the replier runs
	// turns on it (async, cancelable) and a [BargeIn] reactor yields it on a human
	// interruption. bargeConfirm is that reactor's confirm window (0 = instant).
	// floorCoalesce is the floor's same-utterance coalesce window (0 = plain
	// supersession); see [Floor] and [WithBargeInCoalesce].
	floor         *Floor
	bargeConfirm  time.Duration
	floorCoalesce time.Duration
	bargeEnabled  bool

	// mutes is the live mute view ([WithMute], #211): nil = feature off. When set,
	// Register gates the replier's routes on it and — when barge-in built the floor
	// — binds a [MuteCut] reactor beside [BargeIn].
	mutes MuteView

	// gate is the live turn gate ([WithTurnGate], #130): nil = feature off. When
	// set, Register installs it on the replier, which refuses a route whose new turn
	// the gate denies (the spend soft cap) beside the mute pre-check.
	gate TurnGate

	// voiceOf is the /say direct-speech voice lookup ([WithDirectSpeech], #295): nil
	// = feature off. When set, Register binds a [DirectSpeech] reactor on
	// SpeakRequested, sharing the barge-in floor (so a barge cancels a /say) and the
	// turn gate. Deliberately independent of the mute view (GM puppeteering bypasses
	// mute).
	voiceOf VoiceLookup
}

// Option configures a [Conversation] at construction.
type Option func(*Conversation)

// WithDetector adds an address detector to the conversation, wiring
// STTFinal → AddressRouted. Without it the conversation transcribes but never
// routes.
func WithDetector(d *AddressDetector) Option {
	return func(c *Conversation) { c.detector = d }
}

// WithReply adds a reply reactor driven by fn, wiring AddressRouted → TTS. It
// requires the conversation to have been given a non-nil TTS stage; Register
// panics otherwise. Without it the conversation routes but never speaks.
//
// Mutually exclusive with [WithReplyStream]; setting both panics at Register.
func WithReply(fn ReplyFunc) Option {
	return func(c *Conversation) { c.reply = fn }
}

// WithReplyStream adds a streaming reply reactor (B1): the strategy dispatches a
// turn's sentences to TTS as they are produced, so first audio begins after the
// first sentence rather than the whole completion. Like [WithReply] it requires
// a non-nil TTS stage. Mutually exclusive with [WithReply]; setting both panics
// at Register.
func WithReplyStream(fn StreamReplyFunc) Option {
	return func(c *Conversation) { c.replyStream = fn }
}

// WithBargeIn enables human barge-in (ADR-0027): replies run on their own
// goroutine under a cancelable per-turn floor, and a [BargeIn] reactor yields
// that floor when a participant speaks while the Agent is talking — cancelling
// the turn's TTS and playback. confirmWindow is how long continuous speech must
// persist before it counts as a barge (0 yields instantly on onset). It requires
// [WithReply]; without a replier there is no turn to interrupt.
//
// The floor uses plain supersession (no coalesce window); for the live loop,
// where one utterance can VAD-split into several turns, prefer
// [WithBargeInCoalesce] to keep one utterance mapped to one turn.
func WithBargeIn(confirmWindow time.Duration) Option {
	return func(c *Conversation) {
		c.bargeConfirm = confirmWindow
		c.floorCoalesce = 0
		c.floor = nil // built in Register from the configured windows
		c.bargeEnabled = true
	}
}

// WithBargeInCoalesce is [WithBargeIn] plus a floor coalesce window (root cause
// #2 of the latency investigation): a per-turn [Floor.Take] arriving within
// coalesceWindow of the previous one AND routed to the same target agent is
// treated as the SAME utterance continuing and yields to the in-flight turn
// instead of superseding it (see [Floor]). This stops a VAD over-split of one
// utterance from self-cancelling its own first turn mid-synthesis; a take for a
// different agent inside the window supersedes as normal (#146). A zero
// coalesceWindow is identical to [WithBargeIn].
func WithBargeInCoalesce(confirmWindow, coalesceWindow time.Duration) Option {
	return func(c *Conversation) {
		c.bargeConfirm = confirmWindow
		c.floorCoalesce = coalesceWindow
		c.floor = nil // built in Register from the configured windows
		c.bargeEnabled = true
	}
}

// WithStreamingSTT wires a streaming-STT transport (ADR-0042) into the segmenter:
// each utterance is mirrored onto the persistent websocket (pre-roll + voiced
// frames) and finalized by a manual commit at the local VAD speech-end, with the
// batch recognizer as the automatic fallback. A nil sm is ZERO behaviour change —
// the byte-for-byte no-streaming default — so callers can wire it unconditionally.
func WithStreamingSTT(sm *StreamManager) Option {
	return func(c *Conversation) { c.seg.lanes[""].stream = sm }
}

// WithSpeakerLanes enables per-speaker utterance segmentation (ADR-0050): an
// attributed frame ([audio.Frame.Speaker] != "") opens a Speaker Lane — a VAD
// session built by f, fed only that speaker's frames — so each speaker's utterances
// are transcribed and attributed independently. A nil f (or leaving this option
// unset) keeps the segmenter single-lane forever, byte-identical to the pre-lane
// pipeline. The default (unattributed) lane always exists; f builds only the
// non-default lanes and its close func releases each lane's ONNX session on reap.
func WithSpeakerLanes(f LaneVADFactory) Option {
	return func(c *Conversation) { c.seg.laneVADFactory = f }
}

// WithLaneStreamingSTT wires a per-Speaker-Lane streaming-STT transport (ADR-0042 ×
// ADR-0050): f builds a [StreamManager] for a lane at its creation, capped at
// maxLanes concurrent lane streams — past the cap a lane transcribes pure batch, so
// concurrent sockets track concurrent speakers, not channel size. It complements
// [WithStreamingSTT] (which wires the default lane's stream); a nil f leaves the
// non-default lanes batch-only.
func WithLaneStreamingSTT(f func(speakerID string) *StreamManager, maxLanes int) Option {
	return func(c *Conversation) {
		c.seg.laneStreamFactory = f
		c.seg.maxStreamLanes = maxLanes
	}
}

// WithDirectSpeech enables the GM /say direct-speech path (#295, ADR-0010): a
// [DirectSpeech] reactor renders a [voiceevent.SpeakRequested] to TTS in the Agent's
// Voice, looked up via voiceOf. It requires a non-nil TTS stage; Register panics
// otherwise. The reactor shares the barge-in floor built for [WithReply] (so a human
// barge cancels a /say) and honors [WithTurnGate], but deliberately bypasses the
// mute view — /say is a GM override. A nil voiceOf is the feature-off default. It is
// independent of [WithReply]: /say publishes SpeakRequested, never AddressRouted, so
// it never wakes the LLM Replier (ADR-0024).
func WithDirectSpeech(voiceOf VoiceLookup) Option {
	return func(c *Conversation) { c.voiceOf = voiceOf }
}

// WithErrorHandler sets the [ErrorFunc] used to report failures from stage calls
// the reactors fire inside bus callbacks (currently the replier's TTS dispatch).
// Without it such failures are dropped silently.
func WithErrorHandler(fn ErrorFunc) Option {
	return func(c *Conversation) { c.onError = fn }
}

// NewConversation wires the stages into a conversation on bus. bus, vad, and stt
// must be non-nil; ttsStage may be nil only when no [WithReply] option is given.
// All non-nil arguments are owned by the caller.
func NewConversation(bus *voiceevent.Bus, vad *VAD, stt *STT, ttsStage *TTS, opts ...Option) *Conversation {
	if bus == nil {
		panic("orchestrator.NewConversation: bus must not be nil")
	}
	c := &Conversation{
		bus: bus,
		tts: ttsStage,
		seg: NewSegmenter(vad, stt),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Register installs the conversation's reactors on the bus and returns a single
// teardown func. ctx governs the bound reactions and is the context handed to
// the STT/TTS calls they trigger; teardown stays explicit via the returned
// cancel (ADR-0026). Register must be called before [Conversation.Feed].
func (c *Conversation) Register(ctx context.Context) (cancel func()) {
	// The segmenter transcribes off the audio loop (#24), so its recognizer errors
	// have no caller to return to; route them to the same handler the replier uses.
	c.seg.onError = c.onError

	reactors := []Reactor{c.seg}
	if c.detector != nil {
		reactors = append(reactors, c.detector)
	}
	if c.reply != nil && c.replyStream != nil {
		panic("orchestrator.Conversation.Register: WithReply and WithReplyStream are mutually exclusive")
	}
	if c.reply != nil || c.replyStream != nil {
		if c.tts == nil {
			panic("orchestrator.Conversation.Register: a reply strategy was set but no TTS stage was provided")
		}
		var replier *Replier
		if c.replyStream != nil {
			replier = NewStreamReplier(c.tts, c.replyStream, c.onError)
		} else {
			replier = NewReplier(c.tts, c.reply, c.onError)
		}
		// Live mute view (#211): the replier discards a muted addressee's route
		// before taking the floor, so an addressed-but-muted Agent opens no turn.
		// Independent of barge-in; nil is the feature-off default.
		replier.mutes = c.mutes
		// Live turn gate (#130): the replier refuses a NEW turn once the session's
		// estimated spend crosses the soft cap. Independent of barge-in and mute; nil
		// is the feature-off default.
		replier.gate = c.gate
		if c.bargeEnabled {
			// Barge-in mode: the replier runs turns on the floor, and the BargeIn
			// reactor yields it on a human interruption. Bind BargeIn before the
			// replier so a speech_start is evaluated for a yield ahead of any new
			// turn it might otherwise route. The floor is built here (not at option
			// time) so the coalesce window — possibly set by a later option — is in
			// effect.
			if c.floorCoalesce > 0 {
				c.floor = NewFloorWithCoalesce(c.floorCoalesce)
			} else {
				c.floor = NewFloor()
			}
			replier.floor = c.floor
			reactors = append(reactors, NewBargeIn(c.floor, c.bargeConfirm))
			// Mute cut (#211): muting the Agent that is speaking cuts its floor. Bound
			// beside BargeIn (before the replier) on the same floor; only when a mute
			// view is wired.
			if c.mutes != nil {
				reactors = append(reactors, NewMuteCut(c.floor))
			}
		}
		reactors = append(reactors, replier)
	}
	// GM /say direct speech (#295): a DirectSpeech reactor on SpeakRequested, sharing
	// the barge-in floor (built above for the reply path, so a barge cancels a /say)
	// and the turn gate. Bound AFTER the replier so a SpeakRequested and an
	// AddressRouted never contend — they are distinct events on distinct turns. It
	// requires a TTS stage; nil voiceOf is the feature-off default.
	if c.voiceOf != nil {
		if c.tts == nil {
			panic("orchestrator.Conversation.Register: WithDirectSpeech was set but no TTS stage was provided")
		}
		ds := NewDirectSpeech(c.tts, c.voiceOf, c.onError)
		ds.floor = c.floor // shared with the barge path (nil when barge-in is off)
		ds.gate = c.gate
		reactors = append(reactors, ds)
	}
	return Bind(ctx, c.bus, reactors...)
}

// Feed pushes one PCM frame into the conversation. It drives the VAD stage and,
// on speech-end, hands the utterance to STT on a worker goroutine (see
// [Segmenter.Process]); the rest of the pipeline — routing and reply — follows on
// the bus. Feed returns as soon as the segment is handed off so the audio loop
// keeps draining during the network-bound recognizer call (#24); only a VAD error
// is returned. A recognizer error surfaces via [WithErrorHandler], not here.
func (c *Conversation) Feed(frame audio.Frame) error {
	return c.seg.Process(frame)
}

// FeedSilence pushes one silence-CLOCK frame (issue #91) into the conversation. It is
// the sibling of [Conversation.Feed] for the ONE unattributed source that must reach
// every Speaker Lane: the wire tick branch (a paused speaker's packet gap) routes
// synthesized silence here so every lane's VAD hangover advances toward its
// speech_end (ADR-0050's speaker-agnostic silence clock), while real inbound audio —
// including a not-yet-resolved SSRC — stays on [Conversation.Feed] and its own lane
// (or the default lane). Distinguishing the two "" sources at source avoids sniffing
// zero PCM (Opus can legally decode an all-zero frame).
func (c *Conversation) FeedSilence(frame audio.Frame) error {
	return c.seg.ProcessSilence(frame)
}

// Flush transcribes any utterance still buffered because the audio stream ended
// while speech was active, then waits for every in-flight transcription to finish
// (see [Segmenter.Flush]). Call it once after the last [Conversation.Feed] — at
// end of call, or when a clip is exhausted mid-speech — so the final turn is not
// silently lost and all STTFinals land before the reactors tear down. With nothing
// buffered or in flight it is a no-op.
func (c *Conversation) Flush() error {
	return c.seg.Flush()
}
