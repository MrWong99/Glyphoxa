// Package grpcbridge provides an [audio.Connection] implementation backed by
// a gRPC bidirectional stream. It is used by workers in distributed mode to
// receive and send opus audio frames via the gateway's AudioBridgeService,
// without needing a direct Discord voice connection.
//
// The gateway forwards raw opus frames from Discord; this package decodes them
// to PCM for the audio pipeline, and encodes outgoing PCM to opus for Discord
// playback via the gateway.
package grpcbridge

import (
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"layeh.com/gopus"

	pb "github.com/MrWong99/glyphoxa/gen/glyphoxa/v1"
	"github.com/MrWong99/glyphoxa/pkg/audio"
)

// Compile-time interface assertions.
var (
	_ audio.Connection = (*Connection)(nil)
	_ audio.Flusher    = (*Connection)(nil)
)

const (
	inputChannelBuffer  = 64
	outputChannelBuffer = 64

	// opusSampleRate and opusChannels match Discord's audio format.
	opusSampleRate  = 48000
	opusChannels    = 2
	opusFrameSizeMs = 20
	// opusFrameSize is samples per channel per 20 ms frame.
	opusFrameSize = opusSampleRate * opusFrameSizeMs / 1000 // 960
	// opusFrameBytes is the PCM byte size for one frame: 960 * 2 channels * 2 bytes = 3840.
	opusFrameBytes = opusFrameSize * opusChannels * 2
)

// frameLogInterval controls how often periodic frame-flow log messages are
// emitted. One log line per this many frames (~5 s at 50 fps).
const frameLogInterval = 250

// Connection implements [audio.Connection] by streaming opus frames over a
// gRPC AudioBridgeService bidirectional stream. Incoming frames from the
// gateway (Discord audio) are decoded from opus to PCM and demuxed by user_id
// into per-participant input channels. Outgoing frames (NPC audio from the
// mixer) are encoded from PCM to opus and sent to the gateway for playback.
//
// Connection is safe for concurrent use.
type Connection struct {
	stream    grpc.BidiStreamingClient[pb.AudioFrame, pb.AudioFrame]
	sessionID string

	// botUserID is the bot's own user ID. When set, frames from this user are
	// silently dropped in recvLoop to prevent the NPC from hearing its own
	// TTS output (echo/feedback loop).
	botUserID string

	inputsMu   sync.RWMutex
	inputs     map[string]chan audio.AudioFrame
	decodersMu sync.Mutex
	decoders   map[string]*gopus.Decoder

	output  chan audio.AudioFrame
	encoder *gopus.Encoder
	conv    audio.FormatConverter
	buf     []byte

	changeCb func(audio.Event)
	changeMu sync.Mutex

	// flushCh signals sendLoop to clear its partial buffer and send a flush
	// frame. Buffered so Flush() never blocks.
	flushCh chan struct{}

	recvFrames atomic.Uint64
	sendFrames atomic.Uint64

	done      chan struct{}
	closeOnce sync.Once
}

// New creates a Connection for the given session and gRPC stream. It sends an
// initial handshake frame containing the session_id so the gateway routes the
// stream to the correct SessionAudioBridge.
//
// The caller must call Disconnect when done.
func New(sessionID string, stream grpc.BidiStreamingClient[pb.AudioFrame, pb.AudioFrame]) (*Connection, error) {
	enc, err := gopus.NewEncoder(opusSampleRate, opusChannels, gopus.Audio)
	if err != nil {
		return nil, fmt.Errorf("grpcbridge: create opus encoder: %w", err)
	}

	c := &Connection{
		stream:    stream,
		sessionID: sessionID,
		inputs:    make(map[string]chan audio.AudioFrame),
		decoders:  make(map[string]*gopus.Decoder),
		output:    make(chan audio.AudioFrame, outputChannelBuffer),
		encoder:   enc,
		conv:      audio.FormatConverter{Target: audio.Format{SampleRate: opusSampleRate, Channels: opusChannels}},
		flushCh:   make(chan struct{}, 1),
		done:      make(chan struct{}),
	}

	// Send handshake frame with session_id.
	if err := stream.Send(&pb.AudioFrame{SessionId: sessionID}); err != nil {
		return nil, fmt.Errorf("grpcbridge: handshake: %w", err)
	}

	go c.recvLoop()
	go c.sendLoop()

	slog.Info("grpcbridge: connection established", "session_id", sessionID)
	return c, nil
}

// SetBotUserID sets the bot's own user ID so that its audio is filtered out.
// This prevents echo/feedback loops where the NPC hears its own TTS output.
// Must be called before audio starts flowing.
func (c *Connection) SetBotUserID(id string) {
	c.botUserID = id
	slog.Debug("grpcbridge: bot user ID set for self-hearing guard",
		"bot_user_id", id, "session_id", c.sessionID)
}

// recvLoop reads opus frames from the gRPC stream, decodes them to PCM, and
// demuxes by user_id into per-participant input channels. On exit it closes
// all input channels so that consumers ranging over them terminate.
func (c *Connection) recvLoop() {
	defer func() {
		c.inputsMu.Lock()
		for id, ch := range c.inputs {
			close(ch)
			delete(c.inputs, id)
		}
		c.inputsMu.Unlock()
	}()

	for {
		frame, err := c.stream.Recv()
		if err != nil {
			select {
			case <-c.done:
			default:
				slog.Debug("grpcbridge: recv loop ended", "session_id", c.sessionID, "err", err)
			}
			return
		}

		userID := frame.GetUserId()
		if userID == "" || len(frame.GetOpusData()) == 0 {
			continue
		}

		// Self-hearing guard: drop frames from the bot's own user ID.
		if c.botUserID != "" && userID == c.botUserID {
			continue
		}

		// Get or create opus decoder for this user.
		c.decodersMu.Lock()
		dec, exists := c.decoders[userID]
		if !exists {
			var decErr error
			dec, decErr = gopus.NewDecoder(opusSampleRate, opusChannels)
			if decErr != nil {
				c.decodersMu.Unlock()
				slog.Error("grpcbridge: create opus decoder", "user_id", userID, "err", decErr)
				continue
			}
			c.decoders[userID] = dec
		}
		c.decodersMu.Unlock()

		// Ensure an input channel exists for this user.
		c.inputsMu.Lock()
		ch, chExists := c.inputs[userID]
		if !chExists {
			ch = make(chan audio.AudioFrame, inputChannelBuffer)
			c.inputs[userID] = ch
		}
		c.inputsMu.Unlock()

		if !chExists {
			slog.Debug("grpcbridge: new participant", "user_id", userID, "session_id", c.sessionID)
			c.emitEvent(audio.Event{
				Type:   audio.EventJoin,
				UserID: userID,
			})
		}

		// Decode opus → PCM.
		pcmSamples, decErr := dec.Decode(frame.GetOpusData(), opusFrameSize, false)
		if decErr != nil {
			slog.Warn("grpcbridge: opus decode error", "user_id", userID, "err", decErr)
			continue
		}
		pcmBytes := int16sToBytes(pcmSamples)

		af := audio.AudioFrame{
			Data:       pcmBytes,
			SampleRate: opusSampleRate,
			Channels:   opusChannels,
			Timestamp:  time.Duration(frame.GetSsrc()) * time.Second / time.Duration(opusSampleRate),
		}

		n := c.recvFrames.Add(1)
		if n%frameLogInterval == 1 {
			slog.Info("grpcbridge: receiving gateway→worker audio",
				"session_id", c.sessionID,
				"frames_total", n,
				"participants", len(c.inputs),
			)
		}

		select {
		case ch <- af:
		default:
			// Drop frame — consumer is behind.
		}
	}
}

// sendLoop reads PCM frames from the output channel, converts them to 48 kHz
// stereo, buffers until a full opus frame, encodes, and sends via gRPC.
func (c *Connection) sendLoop() {
	for {
		select {
		case <-c.done:
			return

		case <-c.flushCh:
			// Barge-in or mute: drain the output channel, discard partial
			// opus buffer, and tell the gateway to flush its own buffer.
			// All output channel reads happen here (on the sendLoop goroutine)
			// to avoid a data race with the normal frame read path.
			drained := 0
			for {
				select {
				case <-c.output:
					drained++
				default:
					goto drainDone
				}
			}
		drainDone:
			c.buf = c.buf[:0]
			if drained > 0 {
				slog.Info("grpcbridge: flushed local output buffer",
					"session_id", c.sessionID,
					"drained_frames", drained,
				)
			}
			if err := c.stream.Send(&pb.AudioFrame{
				SessionId: c.sessionID,
				Flush:     true,
			}); err != nil {
				slog.Debug("grpcbridge: flush send failed", "session_id", c.sessionID, "err", err)
			}

		case frame, ok := <-c.output:
			if !ok {
				return
			}
			// Convert to Discord's target format (48 kHz stereo).
			frame = c.conv.Convert(frame)
			if len(frame.Data) == 0 {
				continue
			}
			c.buf = append(c.buf, frame.Data...)

			// Encode full opus frames as they accumulate.
			for len(c.buf) >= opusFrameBytes {
				pcm := bytesToInt16s(c.buf[:opusFrameBytes])
				c.buf = c.buf[opusFrameBytes:]

				opus, encErr := c.encoder.Encode(pcm, opusFrameSize, opusFrameBytes)
				if encErr != nil {
					slog.Warn("grpcbridge: opus encode error", "err", encErr)
					continue
				}

				sn := c.sendFrames.Add(1)
				if sn == 1 {
					slog.Info("grpcbridge: first NPC audio frame sent to gateway",
						"session_id", c.sessionID,
					)
				}
				if sn%frameLogInterval == 0 {
					slog.Info("grpcbridge: sending worker→gateway audio",
						"session_id", c.sessionID,
						"frames_total", sn,
					)
				}

				err := c.stream.Send(&pb.AudioFrame{
					SessionId: c.sessionID,
					OpusData:  opus,
				})
				if err != nil {
					select {
					case <-c.done:
					default:
						slog.Debug("grpcbridge: send failed", "session_id", c.sessionID, "err", err)
					}
					return
				}
			}
		}
	}
}

// InputStreams returns a snapshot of the current per-participant audio channels.
func (c *Connection) InputStreams() map[string]<-chan audio.AudioFrame {
	c.inputsMu.RLock()
	defer c.inputsMu.RUnlock()
	snap := make(map[string]<-chan audio.AudioFrame, len(c.inputs))
	for id, ch := range c.inputs {
		snap[id] = ch
	}
	return snap
}

// OutputStream returns the write-only channel for NPC audio output.
func (c *Connection) OutputStream() chan<- audio.AudioFrame {
	return c.output
}

// OnParticipantChange registers cb as the callback for participant join/leave events.
func (c *Connection) OnParticipantChange(cb func(audio.Event)) {
	c.changeMu.Lock()
	defer c.changeMu.Unlock()
	c.changeCb = cb
}

// Flush signals sendLoop to drain the output channel, clear its partial opus
// buffer, and send a flush control frame to the gateway. This ensures that
// after a barge-in or mute, stale NPC audio stops playing immediately on both
// the worker and gateway sides.
//
// The output channel drain is performed by sendLoop (not here) to avoid a
// data race between Flush() and sendLoop both reading from c.output.
//
// Safe for concurrent use.
func (c *Connection) Flush() {
	// Signal sendLoop to drain the output channel, clear its partial buf,
	// and send a flush frame. All output channel reads happen in sendLoop's
	// goroutine to avoid data races.
	select {
	case c.flushCh <- struct{}{}:
	default:
		// Already signalled.
	}
}

// Disconnect cleanly tears down the gRPC stream. Input channels are closed by
// the recv loop when it exits (triggered by CloseSend unblocking Recv).
// Safe to call more than once.
func (c *Connection) Disconnect() error {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.stream.CloseSend()
		slog.Info("grpcbridge: disconnected", "session_id", c.sessionID)
	})
	return nil
}

// emitEvent safely invokes the registered participant change callback.
func (c *Connection) emitEvent(ev audio.Event) {
	c.changeMu.Lock()
	cb := c.changeCb
	c.changeMu.Unlock()
	if cb != nil {
		go cb(ev)
	}
}

// int16sToBytes converts a slice of int16 PCM samples to little-endian bytes.
func int16sToBytes(pcm []int16) []byte {
	b := make([]byte, len(pcm)*2)
	for i, s := range pcm {
		b[i*2] = byte(s)
		b[i*2+1] = byte(s >> 8)
	}
	return b
}

// bytesToInt16s converts little-endian bytes to a slice of int16 PCM samples.
func bytesToInt16s(b []byte) []int16 {
	pcm := make([]int16, len(b)/2)
	for i := range pcm {
		pcm[i] = int16(b[i*2]) | int16(b[i*2+1])<<8
	}
	return pcm
}
