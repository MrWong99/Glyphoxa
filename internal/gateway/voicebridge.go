package gateway

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	pb "github.com/MrWong99/glyphoxa/gen/glyphoxa/v1"
	"github.com/MrWong99/glyphoxa/internal/gateway/audiobridge"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
)

// Compile-time interface assertions.
var (
	_ voice.OpusFrameReceiver = (*voiceBridgeReceiver)(nil)
	_ voice.OpusFrameProvider = (*voiceBridgeProvider)(nil)
)

// frameLogInterval controls how often periodic frame-flow log messages are
// emitted. One log line per this many frames (~5 s at 50 fps).
const frameLogInterval = 250

// voiceBridgeReceiver implements [voice.OpusFrameReceiver] by forwarding raw
// opus frames to a [audiobridge.SessionBridge]. This is set on the gateway's
// voice.Conn so incoming Discord audio is bridged to the worker.
type voiceBridgeReceiver struct {
	bridge    *audiobridge.SessionBridge
	sessionID string

	// botUserID is the bot's own Discord user ID. When set, frames from this
	// user are silently dropped to prevent echo/feedback loops at the gateway
	// layer (defense-in-depth — the worker also filters by bot user ID).
	botUserID snowflake.ID

	frameCount atomic.Uint64
	done       chan struct{}
	closeOnce  sync.Once
}

// ReceiveOpusFrame receives a raw opus packet from Discord and forwards it to
// the worker via the audio bridge.
//
// Frames from the bot's own user ID (set via botUserID) are silently dropped
// to prevent echo/feedback loops where the NPC hears its own TTS output.
func (r *voiceBridgeReceiver) ReceiveOpusFrame(userID snowflake.ID, packet *voice.Packet) error {
	if packet == nil || len(packet.Opus) == 0 {
		return nil
	}

	// Self-hearing guard: drop frames from the bot's own user ID.
	if r.botUserID != 0 && userID == r.botUserID {
		return nil
	}

	n := r.frameCount.Add(1)
	if n%frameLogInterval == 1 {
		slog.Info("voicebridge: forwarding Discord→worker audio",
			"session_id", r.sessionID,
			"frames_total", n,
			"user_id", userID,
		)
	}

	r.bridge.SendToWorker(&pb.AudioFrame{
		SessionId: r.sessionID,
		OpusData:  packet.Opus,
		Ssrc:      packet.SSRC,
		UserId:    userID.String(),
	})
	return nil
}

// CleanupUser handles a participant leaving the voice channel.
func (r *voiceBridgeReceiver) CleanupUser(userID snowflake.ID) {
	slog.Debug("voicebridge: user left voice", "user_id", userID, "session_id", r.sessionID)
}

// Close tears down the receiver.
func (r *voiceBridgeReceiver) Close() {
	r.closeOnce.Do(func() {
		close(r.done)
	})
}

// voiceBridgeProvider implements [voice.OpusFrameProvider] by reading opus
// frames from the worker via the audio bridge. This is set on the gateway's
// voice.Conn so NPC audio from the worker is sent to Discord.
type voiceBridgeProvider struct {
	bridge    *audiobridge.SessionBridge
	sessionID string

	frameCount atomic.Uint64
	gotFirst   atomic.Bool
	done       chan struct{}
	closeOnce  sync.Once
}

// ProvideOpusFrame returns the next opus frame to send to Discord. It reads
// from the bridge's fromWorker channel. Returns nil (silence) when no audio
// is available, which lets the audio sender emit silence frames.
func (p *voiceBridgeProvider) ProvideOpusFrame() ([]byte, error) {
	select {
	case <-p.done:
		return nil, io.EOF
	case frame := <-p.bridge.ReceiveFromWorker():
		if frame == nil {
			return nil, nil
		}
		n := p.frameCount.Add(1)
		if p.gotFirst.CompareAndSwap(false, true) {
			slog.Info("voicebridge: first worker audio frame received",
				"session_id", p.sessionID,
			)
		}
		if n%frameLogInterval == 0 {
			slog.Info("voicebridge: forwarding worker→Discord audio",
				"session_id", p.sessionID,
				"frames_total", n,
			)
		}
		return frame.GetOpusData(), nil
	default:
		// No audio available — return nil so disgo sends silence.
		return nil, nil
	}
}

// Close tears down the provider.
func (p *voiceBridgeProvider) Close() {
	p.closeOnce.Do(func() {
		close(p.done)
	})
}

// setupVoiceBridge configures a voice.Conn to bridge audio to/from a
// SessionBridge. The gateway joins voice normally via VoiceManager, then this
// function wires the bridge receiver/provider onto the Conn.
//
// botUserID is the bot's own Discord user ID. When non-zero, the receiver
// drops frames from this user to prevent echo/feedback loops at the gateway
// layer (defense-in-depth).
func setupVoiceBridge(
	voiceConn voice.Conn,
	bridge *audiobridge.SessionBridge,
	sessionID string,
	botUserID snowflake.ID,
) (cleanup func()) {
	receiver := &voiceBridgeReceiver{
		bridge:    bridge,
		sessionID: sessionID,
		botUserID: botUserID,
		done:      make(chan struct{}),
	}
	provider := &voiceBridgeProvider{
		bridge:    bridge,
		sessionID: sessionID,
		done:      make(chan struct{}),
	}

	voiceConn.SetOpusFrameReceiver(receiver)
	voiceConn.SetOpusFrameProvider(provider)

	slog.Info("voicebridge: bridge wired to voice conn",
		"session_id", sessionID,
		"guild_id", voiceConn.GuildID(),
	)

	return func() {
		slog.Info("voicebridge: stopping audio bridge",
			"session_id", sessionID,
			"discord_to_worker_frames", receiver.frameCount.Load(),
			"worker_to_discord_frames", provider.frameCount.Load(),
		)
		receiver.Close()
		provider.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		voiceConn.Close(ctx)
	}
}
