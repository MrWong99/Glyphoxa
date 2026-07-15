package orchestrator

import (
	"context"
	"errors"
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
// supplied by the caller, who owns their lifetime. Co-dependent features are
// declared together (#453): the reply mode is one choice ([ReplyStrategy]) and
// everything that rides the barge-in floor is one group ([Barge]), so their
// interaction rules are validated once, inside [NewConversation] — never as a
// mid-[Conversation.Register] panic.
type Conversation struct {
	bus *voiceevent.Bus
	tts *TTS

	seg      *Segmenter
	detector *AddressDetector
	reply    ReplyStrategy
	onError  ErrorFunc

	// barge is the barge-in group ([WithBargeIn]): nil = no floor, synchronous
	// replies. When set, Register builds the floor from its windows and wires
	// every floor-riding feature it carries (see [Barge] for the interaction
	// matrix). floor is that per-Register floor; the floor-sharing reactors
	// (DirectSpeech, ClipReplay) read it after the reply path built it.
	barge *Barge
	floor *Floor

	// voiceOf is the /say direct-speech voice lookup ([WithDirectSpeech], #295): nil
	// = feature off. When set, Register binds a [DirectSpeech] reactor on
	// SpeakRequested, sharing the barge-in floor (so a barge cancels a /say) and the
	// barge group's turn gate. Deliberately independent of the mute view (GM
	// puppeteering bypasses mute).
	voiceOf VoiceLookup

	// clipReplayLoad + clipReplaySink are the Highlight voice-replay seam
	// ([WithClipReplay], #310): both nil = feature off. When set, Register binds a
	// [ClipReplay] reactor on [voiceevent.ReplayRequested], sharing the barge-in
	// floor (so a human barge cancels a replay) but deliberately WITHOUT the turn
	// gate — a replay spends no provider money.
	clipReplayLoad ClipLoader
	clipReplaySink ClipSink
}

// ReplyStrategy is the reply-mode choice: HOW the conversation answers a routed
// utterance. Exactly one of Whole or Stream may be set — the two are mutually
// exclusive, validated at [NewConversation] — and the zero value is "no reply"
// (the conversation transcribes and routes but never speaks). Either mode
// requires a non-nil TTS stage.
type ReplyStrategy struct {
	// Whole dispatches the turn's completion as a single reply once it is fully
	// produced, wiring AddressRouted → TTS in one shot.
	Whole ReplyFunc
	// Stream dispatches the turn's sentences to TTS as they are produced (B1),
	// so first audio begins after the first sentence rather than the whole
	// completion. The production strategy is agent.Cast's ReplyStream.
	Stream StreamReplyFunc
}

// enabled reports whether a reply mode was chosen at all.
func (r ReplyStrategy) enabled() bool { return r.Whole != nil || r.Stream != nil }

// Barge groups the barge-in floor (ADR-0027) with every feature whose wiring
// rides it, so the whole interaction matrix lives — and is validated — in one
// declared place (#453) instead of being scattered across independent options:
//
//   - Confirm/Coalesce shape the per-turn [Floor] the replier runs turns on;
//     a [BargeIn] reactor yields it on a confirmed human interruption,
//     cancelling the turn's TTS and playback.
//   - Mutes gates the replier's routes (a muted addressee opens no turn) and —
//     because the floor always exists inside this group — additionally binds a
//     [MuteCut] reactor beside [BargeIn], so muting the Agent that is SPEAKING
//     cuts its turn. Outside a barge group there is no floor to cut, which is
//     why the mute view lives here.
//   - Gate is installed on the replier (a new turn is refused once the spend
//     soft cap is crossed, #130) and shared with [WithDirectSpeech] (a /say
//     opens a turn too). [WithClipReplay] deliberately bypasses it — a replay
//     spends no provider money.
//   - Ensemble runs a multi-Agent route as a speculative fan-out + Lead race
//     (ADR-0025, #301). An ensemble is ONE floor-holding unit, so the speaker
//     exists only inside this group: ensemble-without-barge is unrepresentable.
//     nil degrades an EnsembleRouted to the top-scored single route.
//   - Lookahead pre-renders the Cross-talk Reaction's first sentence during
//     the Lead's playback (#375). Only the ensemble path consumes it, so
//     setting it without Ensemble is a construction error rather than a silent
//     no-op.
//
// A barge group requires a reply strategy ([ReplyStrategy]): the floor and
// everything above wire through the replier, so without one none of it can
// take effect — validated at [NewConversation].
type Barge struct {
	// Confirm is how long continuous inbound speech must persist before it
	// counts as a barge and yields the floor (0 yields instantly on onset). It
	// must be > 0 against a live mic: with a shared VAD session the addressing
	// user's own continued speech fires a fresh speech_start while the Agent
	// holds the floor, and a zero window cancels the very turn it triggered
	// (the 20s self-cancel of the latency investigation).
	Confirm time.Duration
	// Coalesce is the floor's same-utterance coalesce window (root cause #2 of
	// the latency investigation): a [Floor.Take] arriving within it of the
	// previous one AND routed to the same target agent is treated as the SAME
	// utterance continuing and yields to the in-flight turn instead of
	// superseding it, so a VAD over-split of one utterance cannot self-cancel
	// its own first turn mid-synthesis; a take for a different agent inside the
	// window supersedes as normal (#146). 0 keeps plain supersession.
	Coalesce time.Duration
	// Mutes is the live authoritative mute view (#211); nil = feature off (the
	// replier gates nothing, no MuteCut is bound).
	Mutes MuteView
	// Gate is the live spend turn gate (#130, ADR-0046); nil = feature off (no
	// caps configured — every new turn is allowed).
	Gate TurnGate
	// Ensemble is the Ensemble Turn speaker (#301); nil = feature off (an
	// EnsembleRouted degrades to the top-scored single route). The production
	// speaker is agent.Cast.
	Ensemble EnsembleSpeaker
	// Lookahead is the pump look-ahead seam (#375); nil = feature off (the
	// Reaction pre-renders TEXT only — onset gap = one cold TTS TTFB, the #302
	// legacy path). Requires Ensemble. The production pump is
	// [wire.PlaybackPump].
	Lookahead LookaheadPump
}

// LookaheadPump is the pump pre-render seam the Cross-talk Reaction coordinator
// drives (#375, ADR-0025): a queued Reaction's first sentence is synthesized and
// HELD in the pump's look-ahead lane during the Lead's playback (keyed by turn id),
// then either released to play at the Lead's end for a near-zero onset gap, or
// discarded on a barge/yield (its pre-rendered-but-unplayed audio dropped, ADR-0012/
// 0027). Both methods are non-blocking latch operations — they never wait for the
// sentence to be primed — and a keyed discard for an unknown turn is a harmless
// no-op, so the coordinator can defer it on every exit path. The production
// implementation is [wire.PlaybackPump].
type LookaheadPump interface {
	ReleaseLookahead(turnID string)
	DiscardLookahead(turnID string)
}

// Option configures a [Conversation] at construction.
type Option func(*Conversation)

// WithDetector adds an address detector to the conversation, wiring
// STTFinal → AddressRouted. Without it the conversation transcribes but never
// routes.
func WithDetector(d *AddressDetector) Option {
	return func(c *Conversation) { c.detector = d }
}

// WithReply chooses the reply mode, wiring AddressRouted → TTS. Exactly one of
// s.Whole or s.Stream may be set ([ReplyStrategy]); either requires the
// conversation to have been given a non-nil TTS stage. Both rules are validated
// by [NewConversation]. Without this option the conversation routes but never
// speaks.
func WithReply(s ReplyStrategy) Option {
	return func(c *Conversation) { c.reply = s }
}

// WithBargeIn enables human barge-in (ADR-0027) and the floor-riding features
// grouped on b: replies run on their own goroutine under a cancelable per-turn
// floor, and a [BargeIn] reactor yields that floor when a participant speaks
// while the Agent is talking — cancelling the turn's TTS and playback. See
// [Barge] for the windows, the mute/gate/ensemble/lookahead wiring, and the
// group's validation rules. It requires a reply strategy ([WithReply]); without
// a replier there is no turn to interrupt.
func WithBargeIn(b Barge) Option {
	return func(c *Conversation) { c.barge = &b }
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
// Voice, looked up via voiceOf. It requires a non-nil TTS stage (validated at
// [NewConversation]). Its floor dependency is explicit and optional: it shares the
// barge-in floor built for the reply path when a [Barge] group is set (so a human
// barge cancels a /say) and runs floorless otherwise, and it honors the barge
// group's [Barge.Gate] but deliberately bypasses the mute view — /say is a GM
// override. A nil voiceOf is the feature-off default. It is independent of
// [WithReply]: /say publishes SpeakRequested, never AddressRouted, so it never
// wakes the LLM Replier (ADR-0024).
func WithDirectSpeech(voiceOf VoiceLookup) Option {
	return func(c *Conversation) { c.voiceOf = voiceOf }
}

// WithClipReplay enables the Highlight voice-replay path (#310, ADR-0051): a
// [ClipReplay] reactor plays a promoted Session Highlight's clip into the live
// voice channel on a [voiceevent.ReplayRequested], loading the clip via load and
// pushing its chunks to sink (the outbound playback path). Its floor dependency is
// explicit and optional: it shares the barge-in floor when a [Barge] group is set
// (so a human barge cancels a replay) and runs floorless otherwise, and it
// deliberately carries NO turn gate — a replay is pre-recorded audio, zero
// provider spend. Both load and sink must be non-nil for the feature; leaving
// either nil is the feature-off default. Like [WithDirectSpeech] it is independent
// of the reply path: ReplayRequested is its own event, so it never wakes the LLM
// Replier.
func WithClipReplay(load ClipLoader, sink ClipSink) Option {
	return func(c *Conversation) {
		c.clipReplayLoad = load
		c.clipReplaySink = sink
	}
}

// WithErrorHandler sets the [ErrorFunc] used to report failures from stage calls
// the reactors fire inside bus callbacks (currently the replier's TTS dispatch).
// Without it such failures are dropped silently.
func WithErrorHandler(fn ErrorFunc) Option {
	return func(c *Conversation) { c.onError = fn }
}

// NewConversation wires the stages into a conversation on bus. bus, vad, and stt
// must be non-nil; ttsStage may be nil only when neither [WithReply] nor
// [WithDirectSpeech] is given. All non-nil arguments are owned by the caller.
//
// It is the single validation point for the option interaction rules (#453):
// an invalid combination — both reply modes, a reply strategy or direct speech
// without a TTS stage, a barge group without a reply strategy, a look-ahead
// pump without an ensemble speaker — fails here with a descriptive error,
// never as a [Conversation.Register]-time panic. (Combinations the groups make
// unrepresentable, like ensemble-without-barge, cannot reach it at all.)
func NewConversation(bus *voiceevent.Bus, vad *VAD, stt *STT, ttsStage *TTS, opts ...Option) (*Conversation, error) {
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
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// validate enforces the option interaction matrix at construction (#453). Every
// rule here used to be a Register-time panic or per-option prose; the grouped
// configs ([ReplyStrategy], [Barge]) make the rest unrepresentable.
func (c *Conversation) validate() error {
	if c.reply.Whole != nil && c.reply.Stream != nil {
		return errors.New("orchestrator: ReplyStrategy.Whole and ReplyStrategy.Stream are mutually exclusive; set exactly one")
	}
	if c.reply.enabled() && c.tts == nil {
		return errors.New("orchestrator: a reply strategy was set but no TTS stage was provided")
	}
	if c.voiceOf != nil && c.tts == nil {
		return errors.New("orchestrator: WithDirectSpeech was set but no TTS stage was provided")
	}
	if c.barge != nil {
		if !c.reply.enabled() {
			return errors.New("orchestrator: WithBargeIn requires a reply strategy (WithReply); without a replier there is no turn to interrupt")
		}
		if c.barge.Lookahead != nil && c.barge.Ensemble == nil {
			return errors.New("orchestrator: Barge.Lookahead requires Barge.Ensemble; only the ensemble Cross-talk Reaction consumes the look-ahead")
		}
	}
	return nil
}

// Register installs the conversation's reactors on the bus and returns a single
// teardown func. ctx governs the bound reactions and is the context handed to
// the STT/TTS calls they trigger; teardown stays explicit via the returned
// cancel (ADR-0026). Register must be called before [Conversation.Feed]. The
// option interaction rules were already validated by [NewConversation], so
// Register only wires.
func (c *Conversation) Register(ctx context.Context) (cancel func()) {
	// The segmenter transcribes off the audio loop (#24), so its recognizer errors
	// have no caller to return to; route them to the same handler the replier uses.
	c.seg.onError = c.onError

	reactors := []Reactor{c.seg}
	if c.detector != nil {
		reactors = append(reactors, c.detector)
	}
	if c.reply.enabled() {
		var replier *Replier
		if c.reply.Stream != nil {
			replier = NewStreamReplier(c.tts, c.reply.Stream, c.onError)
		} else {
			replier = NewReplier(c.tts, c.reply.Whole, c.onError)
		}
		if c.barge != nil {
			// The barge group ([Barge]) wires as one unit: the live mute view (#211,
			// route gating), the turn gate (#130, spend soft cap), the Ensemble Turn
			// speaker (#301) and its look-ahead pump (#375) all land on the replier,
			// and the floor is built from the group's windows. Bind BargeIn before
			// the replier so a speech_start is evaluated for a yield ahead of any new
			// turn it might otherwise route.
			replier.mutes = c.barge.Mutes
			replier.gate = c.barge.Gate
			replier.ensemble = c.barge.Ensemble
			replier.lookahead = c.barge.Lookahead
			if c.barge.Coalesce > 0 {
				c.floor = NewFloorWithCoalesce(c.barge.Coalesce)
			} else {
				c.floor = NewFloor()
			}
			replier.floor = c.floor
			reactors = append(reactors, NewBargeIn(c.floor, c.barge.Confirm))
			// Mute cut (#211): muting the Agent that is speaking cuts its floor. Bound
			// beside BargeIn (before the replier) on the same floor; only when a mute
			// view is wired.
			if c.barge.Mutes != nil {
				reactors = append(reactors, NewMuteCut(c.floor))
			}
		}
		reactors = append(reactors, replier)
	}
	// GM /say direct speech (#295): a DirectSpeech reactor on SpeakRequested, sharing
	// the barge-in floor (built above for the reply path, so a barge cancels a /say)
	// and the barge group's turn gate. Bound AFTER the replier so a SpeakRequested
	// and an AddressRouted never contend — they are distinct events on distinct
	// turns. nil voiceOf is the feature-off default.
	if c.voiceOf != nil {
		ds := NewDirectSpeech(c.tts, c.voiceOf, c.onError)
		ds.floor = c.floor // shared with the barge path (nil when barge-in is off)
		if c.barge != nil {
			ds.gate = c.barge.Gate
		}
		reactors = append(reactors, ds)
	}
	// Highlight voice replay (#310): a ClipReplay reactor on ReplayRequested, sharing
	// the barge-in floor (built above, so a barge cancels a replay) but no turn gate
	// (zero provider spend). Bound AFTER the direct-speech reactor — ReplayRequested
	// is a distinct event on its own turn, so it never contends with a /say or an
	// AddressRouted. Needs no TTS stage (it plays pre-recorded audio). Both load and
	// sink must be set; either nil is the feature-off default.
	if c.clipReplayLoad != nil && c.clipReplaySink != nil {
		cr := NewClipReplay(c.clipReplayLoad, c.clipReplaySink, c.onError)
		cr.floor = c.floor // shared with the barge path (nil when barge-in is off)
		reactors = append(reactors, cr)
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
