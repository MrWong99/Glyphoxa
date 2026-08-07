package voice

// This file pins the fix for issue #579 against the REAL disgo audio sender.
// disgo's conn.Close tears down the gateway/UDP but never closes the
// AudioSender goroutine SetOpusFrameProvider spawned; a quiescent sender
// (silent frames exhausted, speaking-stop sent) reads len==0 from its provider
// and returns without touching the network, so it can never observe the closed
// transport and never exits — one goroutine waking 50×/s per closed Session.
// Session.Close therefore closes the switchingProvider AFTER the transport,
// flipping it into its sender-reaping state: every poll returns a real silence
// frame, the sender writes it into the dead transport, hits net.ErrClosed (or
// ErrGatewayNotConnected on SetSpeaking), and its own handleErr shuts it down.
//
// The rig mirrors disgo's connImpl exactly where it matters: the voiceConn
// seam's SetOpusFrameProvider constructs a REAL voice.NewAudioSender over a
// fake transport that can be killed, so a disgo bump that changes the sender's
// teardown semantics fails HERE, not in production. Wall-clock waits are
// unavoidable — the real sender owns its own 20ms pacing — so the test bounds
// them generously and stays off t.Parallel (goleak needs a quiet goroutine
// baseline).

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	botgateway "github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave"
	"github.com/disgoorg/snowflake/v2"
	"go.uber.org/goleak"
)

// reapTransport is the shared "is the connection alive" state of the fake
// disgo conn and its UDP transport, plus the quiescence signal the test
// synchronizes on.
type reapTransport struct {
	dead atomic.Bool

	// quiet closes once the sender reaches its quiescent state on the LIVE
	// transport: silent frames exhausted and SetSpeaking(None) sent — the state
	// a closed Session's sender idles in forever without the provider poke.
	quiet     chan struct{}
	quietOnce sync.Once
}

// reapUDPConn implements disgo's voice.UDPConn over the fake transport: writes
// succeed while alive and fail with net.ErrClosed once killed, exactly like a
// closed *net.UDPConn.
type reapUDPConn struct{ tr *reapTransport }

var _ voice.UDPConn = (*reapUDPConn)(nil)

func (u *reapUDPConn) LocalAddr() net.Addr                             { return nil }
func (u *reapUDPConn) RemoteAddr() net.Addr                            { return nil }
func (u *reapUDPConn) SetSecretKey(voice.EncryptionMode, []byte) error { return nil }
func (u *reapUDPConn) SetDeadline(time.Time) error                     { return nil }
func (u *reapUDPConn) SetReadDeadline(time.Time) error                 { return nil }
func (u *reapUDPConn) SetWriteDeadline(time.Time) error                { return nil }
func (u *reapUDPConn) Open(context.Context, string, int, uint32) (string, int, error) {
	return "", 0, nil
}
func (u *reapUDPConn) Close() error                       { return nil }
func (u *reapUDPConn) Read([]byte) (int, error)           { return 0, net.ErrClosed }
func (u *reapUDPConn) ReadPacket() (*voice.Packet, error) { return nil, net.ErrClosed }

func (u *reapUDPConn) Write(p []byte) (int, error) {
	if u.tr.dead.Load() {
		return 0, net.ErrClosed
	}
	return len(p), nil
}

// reapDisgoConn implements the subset of disgo's voice.Conn the real
// defaultAudioSender touches (UDP, DAVE, SetSpeaking); everything else is
// inert. DAVE is godave's noop session, whose Ready() is always true — the
// same post-Close answer the real dave-go session gives (its Close cancels
// watchdogs but clears neither activeEpoch nor sendRatchet), so the sender's
// Ready() guard does not park it before the poke frame reaches the transport.
type reapDisgoConn struct {
	tr   *reapTransport
	udp  *reapUDPConn
	dave godave.Session
}

var _ voice.Conn = (*reapDisgoConn)(nil)

func newReapDisgoConn() *reapDisgoConn {
	tr := &reapTransport{quiet: make(chan struct{})}
	return &reapDisgoConn{
		tr:   tr,
		udp:  &reapUDPConn{tr: tr},
		dave: godave.NewNoopSession(discardLogger(), "", nil),
	}
}

func (c *reapDisgoConn) Gateway() voice.Gateway           { return nil }
func (c *reapDisgoConn) UDP() voice.UDPConn               { return c.udp }
func (c *reapDisgoConn) DAVE() godave.Session             { return c.dave }
func (c *reapDisgoConn) ChannelID() *snowflake.ID         { return nil }
func (c *reapDisgoConn) GuildID() snowflake.ID            { return testGuild }
func (c *reapDisgoConn) UserIDBySSRC(uint32) snowflake.ID { return 0 }

func (c *reapDisgoConn) SetSpeaking(_ context.Context, flags voice.SpeakingFlags) error {
	if c.tr.dead.Load() {
		return voice.ErrGatewayNotConnected
	}
	if flags == voice.SpeakingFlagNone {
		// The sender only sends SpeakingFlagNone after exhausting its silent
		// frames: it has gone quiescent and will never write again unprovoked.
		c.tr.quietOnce.Do(func() { close(c.tr.quiet) })
	}
	return nil
}

func (c *reapDisgoConn) SetOpusFrameProvider(voice.OpusFrameProvider)              {}
func (c *reapDisgoConn) SetOpusFrameReceiver(voice.OpusFrameReceiver)              {}
func (c *reapDisgoConn) Open(context.Context, snowflake.ID, bool, bool) error      { return nil }
func (c *reapDisgoConn) Close(context.Context)                                     { c.tr.dead.Store(true) }
func (c *reapDisgoConn) HandleVoiceStateUpdate(botgateway.EventVoiceStateUpdate)   {}
func (c *reapDisgoConn) HandleVoiceServerUpdate(botgateway.EventVoiceServerUpdate) {}

// reapConn adapts reapDisgoConn to the package's voiceConn seam, spawning a
// REAL disgo AudioSender exactly the way disgo's connImpl.SetOpusFrameProvider
// does — the goroutine whose leak this test pins.
type reapConn struct {
	inner  *reapDisgoConn
	sender voice.AudioSender
}

var _ voiceConn = (*reapConn)(nil)

func (c *reapConn) Open(context.Context, snowflake.ID, bool, bool) error { return nil }

func (c *reapConn) SetOpusFrameProvider(provider voice.OpusFrameProvider) {
	if c.sender != nil {
		c.sender.Close()
	}
	c.sender = voice.NewAudioSender(discardLogger(), provider, c.inner)
	c.sender.Open()
}

func (c *reapConn) SetOpusFrameReceiver(voice.OpusFrameReceiver) {}
func (c *reapConn) Close(context.Context)                        { c.inner.tr.dead.Store(true) }

// TestSessionCloseReapsDisgoAudioSender is issue #579: Session.Close must end
// the disgo AudioSender goroutine even though disgo's conn.Close does not. The
// sender is driven into the quiescent state a real idle Session sits in, the
// Session is closed, and goleak asserts the sender goroutine exited — pre-fix
// it survives forever, waking every 20ms.
func TestSessionCloseReapsDisgoAudioSender(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	conn := &reapConn{inner: newReapDisgoConn()}
	sess, err := newSession(context.Background(), testGuild, testChannel, conn, sessionConfig{
		logger:        discardLogger(),
		metrics:       discardMetrics{},
		inboundBuffer: 8,
	})
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}

	// Let the real sender go quiescent on the LIVE transport: ~5 silence
	// writes then SetSpeaking(None), at its own 20ms cadence. Pre-fix, a
	// Session closed from this state leaks the sender with certainty.
	select {
	case <-conn.inner.tr.quiet:
	case <-time.After(5 * time.Second):
		t.Fatal("disgo sender never reached its quiescent state")
	}

	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// goleak (deferred) fails if the sender goroutine outlives the Session: the
	// closed provider's silence poke must have driven it into net.ErrClosed /
	// ErrGatewayNotConnected and out of its loop.
}
