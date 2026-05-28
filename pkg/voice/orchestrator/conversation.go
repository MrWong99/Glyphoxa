package orchestrator

import (
	"context"

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

	seg      *Segmenter
	detector *AddressDetector
	reply    ReplyFunc
	onError  ErrorFunc
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
func WithReply(fn ReplyFunc) Option {
	return func(c *Conversation) { c.reply = fn }
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
	if c.reply != nil {
		if c.tts == nil {
			panic("orchestrator.Conversation.Register: WithReply set but no TTS stage was provided")
		}
		reactors = append(reactors, NewReplier(c.tts, c.reply, c.onError))
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
