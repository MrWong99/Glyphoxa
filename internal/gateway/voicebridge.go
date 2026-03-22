package gateway

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
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

// voiceBridgeReceiver implements [voice.OpusFrameReceiver] by forwarding raw
// opus frames to a [audiobridge.SessionBridge]. This is set on the gateway's
// voice.Conn so incoming Discord audio is bridged to the worker.
type voiceBridgeReceiver struct {
	bridge    *audiobridge.SessionBridge
	sessionID string

	done      chan struct{}
	closeOnce sync.Once
}

// ReceiveOpusFrame receives a raw opus packet from Discord and forwards it to
// the worker via the audio bridge.
func (r *voiceBridgeReceiver) ReceiveOpusFrame(userID snowflake.ID, packet *voice.Packet) error {
	if packet == nil || len(packet.Opus) == 0 {
		return nil
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

	done      chan struct{}
	closeOnce sync.Once
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

// daveReadyTimeout is the maximum time to wait for the DAVE E2EE handshake
// to complete after the voice connection is opened.
const daveReadyTimeout = 10 * time.Second

// waitForDAVEReady registers an event handler on the voice connection and
// blocks until an OpcodeDaveExecuteTransition event is received, signaling
// that the DAVE MLS key exchange has completed and packets can be
// encrypted/decrypted. Returns an error if ctx is canceled first.
func waitForDAVEReady(ctx context.Context, voiceConn voice.Conn, sessionID string) error {
	ready := make(chan struct{}, 1)
	voiceConn.SetEventHandlerFunc(func(_ voice.Gateway, op voice.Opcode, _ int, _ voice.GatewayMessageData) {
		if op == voice.OpcodeDaveExecuteTransition {
			select {
			case ready <- struct{}{}:
			default:
			}
		}
	})

	select {
	case <-ready:
		slog.Info("voicebridge: DAVE handshake complete", "session_id", sessionID)
		return nil
	case <-ctx.Done():
		return fmt.Errorf("voicebridge: DAVE ready timeout for session %s: %w", sessionID, ctx.Err())
	}
}

// setupVoiceBridge configures a voice.Conn to bridge audio to/from a
// SessionBridge. The gateway joins voice normally via VoiceManager, then this
// function wires the bridge receiver/provider onto the Conn.
func setupVoiceBridge(
	voiceConn voice.Conn,
	bridge *audiobridge.SessionBridge,
	sessionID string,
) (cleanup func()) {
	receiver := &voiceBridgeReceiver{
		bridge:    bridge,
		sessionID: sessionID,
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
		receiver.Close()
		provider.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		voiceConn.Close(ctx)
	}
}
