package discord

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/MrWong99/glyphoxa/pkg/audio"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
)

// Compile-time interface assertions.
var (
	_ audio.Connection        = (*Connection)(nil)
	_ audio.SelfHearingGuard  = (*Connection)(nil)
	_ voice.OpusFrameProvider = (*Connection)(nil)
	_ voice.OpusFrameReceiver = (*Connection)(nil)
)

const (
	inputChannelBuffer  = 64
	outputChannelBuffer = 64
)

// Connection wraps a disgo voice.Conn and adapts it to the [audio.Connection]
// interface. It implements [voice.OpusFrameReceiver] to demux incoming Opus
// packets by user ID into per-participant PCM input streams, and
// [voice.OpusFrameProvider] to encode outgoing PCM frames to Opus for
// transmission.
//
// Connection is safe for concurrent use.
type Connection struct {
	conn    voice.Conn
	guildID snowflake.ID

	// botUserID is the bot's own Discord user ID. When set, frames from this
	// user are silently dropped in ReceiveOpusFrame to prevent the NPC from
	// hearing its own TTS output (echo/feedback loop).
	botUserID snowflake.ID

	inputsMu sync.RWMutex
	inputs   map[snowflake.ID]chan audio.AudioFrame

	output chan audio.AudioFrame

	// decoders maintains a per-user Opus decoder so that decoder state is
	// preserved across consecutive frames for the same user.
	decodersMu sync.Mutex
	decoders   map[snowflake.ID]*opusDecoder

	// encoder is used by ProvideOpusFrame to encode outgoing PCM to Opus.
	encoder *opusEncoder
	conv    audio.FormatConverter
	buf     []byte

	changeCb func(audio.Event)
	changeMu sync.Mutex

	done      chan struct{}
	closeOnce sync.Once

	// disconnectConn is called during Disconnect to tear down the voice
	// connection. Defaults to conn.Close; overridden in tests.
	disconnectConn func()
}

// newConnection initialises a Connection for an already-joined voice channel.
// It registers itself as the OpusFrameProvider and OpusFrameReceiver on the
// disgo voice.Conn.
func newConnection(conn voice.Conn, guildID snowflake.ID) *Connection {
	c := &Connection{
		conn:     conn,
		guildID:  guildID,
		inputs:   make(map[snowflake.ID]chan audio.AudioFrame),
		output:   make(chan audio.AudioFrame, outputChannelBuffer),
		decoders: make(map[snowflake.ID]*opusDecoder),
		done:     make(chan struct{}),
		conv:     audio.FormatConverter{Target: audio.Format{SampleRate: opusSampleRate, Channels: opusChannels}},
		disconnectConn: func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn.Close(ctx)
		},
	}

	// Create the encoder for sending audio.
	enc, err := newOpusEncoder()
	if err != nil {
		slog.Error("discord: failed to create opus encoder", "error", err)
	}
	c.encoder = enc

	// Wire ourselves up as both the frame provider and receiver.
	conn.SetOpusFrameProvider(c)
	conn.SetOpusFrameReceiver(c)

	return c
}

// SetBotUserID sets the bot's own user ID so that its audio is filtered out.
// This prevents echo/feedback loops where the NPC hears its own TTS output.
// Must be called before audio starts flowing.
//
// The id parameter is a Discord snowflake string (e.g., "123456789012345678").
// Invalid IDs are logged and ignored. Implements [audio.SelfHearingGuard].
func (c *Connection) SetBotUserID(id string) {
	parsed, err := snowflake.Parse(id)
	if err != nil {
		slog.Warn("discord: invalid bot user ID for self-hearing guard", "id", id, "err", err)
		return
	}
	c.botUserID = parsed
	slog.Debug("discord: bot user ID set for self-hearing guard", "bot_user_id", parsed)
}

// ── voice.OpusFrameReceiver implementation ──────────────────��───────────────

// ReceiveOpusFrame receives an Opus packet from a specific user, decodes it
// to PCM, and delivers the resulting [audio.AudioFrame] on the per-user input
// channel. If this is the first frame from a user, a new channel is created and
// an [audio.EventJoin] is emitted.
//
// Frames from the bot's own user ID (set via [SetBotUserID]) are silently
// dropped to prevent echo/feedback loops.
func (c *Connection) ReceiveOpusFrame(userID snowflake.ID, packet *voice.Packet) error {
	if packet == nil {
		return nil
	}

	// Self-hearing guard: drop frames from the bot's own user ID.
	if c.botUserID != 0 && userID == c.botUserID {
		return nil
	}

	// Get or create decoder for this user.
	c.decodersMu.Lock()
	dec, exists := c.decoders[userID]
	if !exists {
		var err error
		dec, err = newOpusDecoder()
		if err != nil {
			c.decodersMu.Unlock()
			slog.Error("discord: failed to create opus decoder", "userID", userID, "error", err)
			return nil
		}
		c.decoders[userID] = dec
	}
	c.decodersMu.Unlock()

	pcm, err := dec.decode(packet.Opus)
	if err != nil {
		slog.Warn("discord: opus decode error", "userID", userID, "error", err)
		return nil
	}

	frame := audio.AudioFrame{
		Data:       pcm,
		SampleRate: opusSampleRate,
		Channels:   opusChannels,
		Timestamp:  time.Duration(packet.Timestamp) * time.Second / time.Duration(opusSampleRate),
	}

	// Fast path: send to an existing channel while holding the read lock.
	// The RLock prevents concurrent CleanupUser/Disconnect from closing the
	// channel between our map lookup and the send, avoiding a panic on
	// send-to-closed-channel.
	c.inputsMu.RLock()
	ch, chExists := c.inputs[userID]
	if chExists {
		select {
		case ch <- frame:
		default:
			// Channel full — drop frame rather than block.
		}
		c.inputsMu.RUnlock()
		return nil
	}
	c.inputsMu.RUnlock()

	// Slow path: create channel under write lock. Double-check after
	// acquiring the write lock in case another goroutine created it.
	c.inputsMu.Lock()
	ch, chExists = c.inputs[userID]
	if !chExists {
		ch = make(chan audio.AudioFrame, inputChannelBuffer)
		c.inputs[userID] = ch
	}
	select {
	case ch <- frame:
	default:
	}
	c.inputsMu.Unlock()

	if !chExists {
		slog.Debug("discord: new participant", "userID", userID)
		c.emitEvent(audio.Event{
			Type:   audio.EventJoin,
			UserID: userID.String(),
		})
	}

	return nil
}

// CleanupUser removes the input channel and decoder for the given user and
// emits an [audio.EventLeave].
func (c *Connection) CleanupUser(userID snowflake.ID) {
	c.inputsMu.Lock()
	ch, exists := c.inputs[userID]
	if exists {
		close(ch)
		delete(c.inputs, userID)
	}
	c.inputsMu.Unlock()

	c.decodersMu.Lock()
	delete(c.decoders, userID)
	c.decodersMu.Unlock()

	if exists {
		slog.Debug("discord: participant left", "userID", userID)
		c.emitEvent(audio.Event{
			Type:   audio.EventLeave,
			UserID: userID.String(),
		})
	}
}

// ── voice.OpusFrameProvider implementation ──────────────────────────────────

// ProvideOpusFrame reads PCM frames from the output channel, converts them to
// Discord's target format (48 kHz stereo), buffers until a full Opus frame is
// available, and encodes it. Returns io.EOF when the connection is closed.
func (c *Connection) ProvideOpusFrame() ([]byte, error) {
	// opusFrameBytes is the exact PCM input size for one Opus frame:
	// 960 samples/channel * 2 channels * 2 bytes/sample = 3840 bytes.
	const opusFrameBytes = opusFrameSize * opusChannels * 2

	for {
		// If we have enough buffered data, encode and return immediately.
		if len(c.buf) >= opusFrameBytes {
			if c.encoder == nil {
				c.buf = c.buf[opusFrameBytes:]
				return nil, nil
			}
			opus, err := c.encoder.encode(c.buf[:opusFrameBytes])
			c.buf = c.buf[opusFrameBytes:]
			if err != nil {
				slog.Warn("discord: opus encode error", "error", err)
				continue
			}
			return opus, nil
		}

		// Read the next frame from the output channel. The default case
		// makes this non-blocking: when no audio is queued, we return nil
		// so the audio sender can run its silence-frame logic. This is
		// critical at connection startup — the sender sends a few silence
		// frames over UDP which signals Discord to start routing incoming
		// audio to the bot.
		select {
		case <-c.done:
			return nil, io.EOF
		case frame, ok := <-c.output:
			if !ok {
				return nil, io.EOF
			}

			// Convert to Discord's target format (48 kHz stereo).
			frame = c.conv.Convert(frame)
			if len(frame.Data) == 0 {
				continue
			}

			c.buf = append(c.buf, frame.Data...)
		default:
			return nil, nil
		}
	}
}

// ── audio.Connection interface ─────────────────────────────────────────────

// InputStreams returns a snapshot of the current per-participant audio channels.
// The map key is the Discord user ID as a string.
func (c *Connection) InputStreams() map[string]<-chan audio.AudioFrame {
	c.inputsMu.RLock()
	defer c.inputsMu.RUnlock()
	snap := make(map[string]<-chan audio.AudioFrame, len(c.inputs))
	for id, ch := range c.inputs {
		snap[id.String()] = ch
	}
	return snap
}

// OutputStream returns the write-only channel for NPC audio output.
// Frames written here are encoded to Opus and sent to Discord.
func (c *Connection) OutputStream() chan<- audio.AudioFrame {
	return c.output
}

// OnParticipantChange registers cb as the callback for participant join/leave events.
// Only one callback may be registered; subsequent calls replace the previous one.
func (c *Connection) OnParticipantChange(cb func(audio.Event)) {
	c.changeMu.Lock()
	defer c.changeMu.Unlock()
	c.changeCb = cb
}

// Disconnect cleanly tears down the voice connection and stops all background
// goroutines. It is safe to call more than once; subsequent calls return nil.
func (c *Connection) Disconnect() error {
	c.closeOnce.Do(func() {
		close(c.done)

		if c.disconnectConn != nil {
			c.disconnectConn()
		}

		// Close all input channels so downstream consumers see EOF.
		c.inputsMu.Lock()
		for id, ch := range c.inputs {
			close(ch)
			delete(c.inputs, id)
		}
		c.inputsMu.Unlock()
	})
	return nil
}

// Close implements voice.OpusFrameProvider and voice.OpusFrameReceiver.
// It delegates to Disconnect.
func (c *Connection) Close() {
	_ = c.Disconnect()
}

// ── internal helpers ───────────────────────────────────────────────────────

// emitEvent safely invokes the registered participant change callback.
func (c *Connection) emitEvent(ev audio.Event) {
	c.changeMu.Lock()
	cb := c.changeCb
	c.changeMu.Unlock()
	if cb != nil {
		go cb(ev)
	}
}
