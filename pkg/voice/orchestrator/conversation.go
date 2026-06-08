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
	floor        *Floor
	bargeConfirm time.Duration
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
func WithBargeIn(confirmWindow time.Duration) Option {
	return func(c *Conversation) {
		c.floor = NewFloor()
		c.bargeConfirm = confirmWindow
	}
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
		if c.floor != nil {
			// Barge-in mode: the replier runs turns on the floor, and the BargeIn
			// reactor yields it on a human interruption. Bind BargeIn before the
			// replier so a speech_start is evaluated for a yield ahead of any new
			// turn it might otherwise route.
			replier.floor = c.floor
			reactors = append(reactors, NewBargeIn(c.floor, c.bargeConfirm))
		}
		reactors = append(reactors, replier)
	}
	return Bind(ctx, c.bus, reactors...)
}

// Feed pushes one PCM frame into the conversation. It drives the VAD stage and
// segments utterances to STT (see [Segmenter.Process]); the rest of the pipeline
// — routing and reply — follows on the bus. Errors originate in the VAD or STT
// stage and are returned to the audio loop.
func (c *Conversation) Feed(frame audio.Frame) error {
	return c.seg.Process(frame)
}

// Flush transcribes any utterance still buffered because the audio stream ended
// while speech was active (see [Segmenter.Flush]). Call it once after the last
// [Conversation.Feed] — at end of call, or when a clip is exhausted mid-speech —
// so the final turn is not silently lost. With nothing buffered it is a no-op.
func (c *Conversation) Flush() error {
	return c.seg.Flush()
}
