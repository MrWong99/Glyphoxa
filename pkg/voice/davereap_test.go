package voice

import (
	"context"
	"log/slog"
	"net"
	"sync/atomic"
	"testing"
	"time"

	botgateway "github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave"
	"github.com/disgoorg/snowflake/v2"
)

// fakeDaveSession is a controllable godave.Session for the reap-wrapper tests.
// Only the methods the wrapper and disgo's audio goroutines touch are
// implemented; the embedded nil interface panics loudly on anything else.
type fakeDaveSession struct {
	godave.Session
	ready  atomic.Bool
	closed atomic.Bool
}

func (f *fakeDaveSession) Ready() bool { return f.ready.Load() }
func (f *fakeDaveSession) Close() error {
	f.closed.Store(true)
	return nil
}

// holdingDaveSession adds dave-go's ShouldHoldFrames surface: hold=true models
// a live pending handshake (protocol ≥ 1, no epoch yet), hold=false with
// ready=false models a channel that will never have E2EE (protocol version 0).
type holdingDaveSession struct {
	fakeDaveSession
	hold atomic.Bool
}

func (f *holdingDaveSession) ShouldHoldFrames() bool { return f.hold.Load() }

func wrap(t *testing.T, inner godave.Session) godave.Session {
	t.Helper()
	create := WrapDaveSessionCreate(func(*slog.Logger, godave.UserID, godave.Callbacks) godave.Session {
		return inner
	})
	return create(slog.New(slog.DiscardHandler), godave.UserID("1"), nil)
}

// The wrapper's three-way Ready answer, against a dave-go-shaped inner session
// (has ShouldHoldFrames): a pending handshake holds, a never-E2EE channel and a
// closed session both report ready so disgo's audio goroutines reach their
// terminal I/O.
func TestReapableDaveSessionReady(t *testing.T) {
	t.Parallel()

	inner := &holdingDaveSession{}
	inner.hold.Store(true) // handshake pending: protocol ≥ 1, no epoch yet
	s := wrap(t, inner)

	if s.Ready() {
		t.Fatal("Ready() = true during a live pending handshake; frames must be held")
	}

	// The channel downgrades to protocol version 0: E2EE will never establish,
	// dave-go's own Ready stays false forever — the wrapper must report ready
	// so audio flows in passthrough instead of busy-waiting on a phantom epoch.
	inner.hold.Store(false)
	if !s.Ready() {
		t.Fatal("Ready() = false on a never-E2EE (protocol v0) channel; audio goroutines would spin forever")
	}

	// Epoch established: plain delegation.
	inner.hold.Store(true)
	inner.ready.Store(true)
	if !s.Ready() {
		t.Fatal("Ready() = false with an active inner epoch")
	}

	// Closed while mid-handshake (the <5s Voice Session shape, #586): Ready
	// must flip true so the receiver's next poll reaches ReadPacket and reaps
	// itself on net.ErrClosed.
	inner.ready.Store(false)
	if err := s.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if !s.Ready() {
		t.Fatal("Ready() = false after Close; the audio goroutines can never observe teardown")
	}
	if !inner.closed.Load() {
		t.Fatal("Close was not delegated to the inner session")
	}
}

// An inner session without ShouldHoldFrames (not dave-go) keeps conservative
// delegation while open, and still reaps after Close.
func TestReapableDaveSessionReadyWithoutFrameHolder(t *testing.T) {
	t.Parallel()

	inner := &fakeDaveSession{}
	s := wrap(t, inner)
	if s.Ready() {
		t.Fatal("Ready() = true while the inner session reports not ready")
	}
	_ = s.Close()
	if !s.Ready() {
		t.Fatal("Ready() = false after Close")
	}
}

// fakeDisgoConn implements disgo's voice.Conn for the quiescence regression:
// it hands disgo's real audio goroutines the wrapped DAVE session and a UDP
// conn that behaves closed after teardown. daveCalls counts every DAVE()
// accessor hit — one per audio-goroutine loop iteration — which is the test's
// tick-activity signal.
type fakeDisgoConn struct {
	dave      godave.Session
	udp       *fakeUDPConn
	torndown  atomic.Bool
	daveCalls atomic.Int64
}

func (c *fakeDisgoConn) Gateway() voice.Gateway { return nil }
func (c *fakeDisgoConn) UDP() voice.UDPConn     { return c.udp }
func (c *fakeDisgoConn) DAVE() godave.Session {
	c.daveCalls.Add(1)
	return c.dave
}
func (c *fakeDisgoConn) ChannelID() *snowflake.ID                     { return nil }
func (c *fakeDisgoConn) GuildID() snowflake.ID                        { return 0 }
func (c *fakeDisgoConn) UserIDBySSRC(uint32) snowflake.ID             { return 0 }
func (c *fakeDisgoConn) SetOpusFrameProvider(voice.OpusFrameProvider) {}
func (c *fakeDisgoConn) SetOpusFrameReceiver(voice.OpusFrameReceiver) {}
func (c *fakeDisgoConn) Open(context.Context, snowflake.ID, bool, bool) error {
	return nil
}
func (c *fakeDisgoConn) Close(context.Context)                                     {}
func (c *fakeDisgoConn) HandleVoiceStateUpdate(botgateway.EventVoiceStateUpdate)   {}
func (c *fakeDisgoConn) HandleVoiceServerUpdate(botgateway.EventVoiceServerUpdate) {}
func (c *fakeDisgoConn) SetSpeaking(context.Context, voice.SpeakingFlags) error {
	if c.torndown.Load() {
		return voice.ErrGatewayNotConnected
	}
	return nil
}

// fakeUDPConn implements disgo's voice.UDPConn: reads block until Close, then
// every read/write reports net.ErrClosed — the shape of a torn-down transport.
type fakeUDPConn struct {
	closed  atomic.Bool
	closeCh chan struct{}
}

func newFakeUDPConn() *fakeUDPConn { return &fakeUDPConn{closeCh: make(chan struct{})} }

func (u *fakeUDPConn) LocalAddr() net.Addr                             { return nil }
func (u *fakeUDPConn) RemoteAddr() net.Addr                            { return nil }
func (u *fakeUDPConn) SetSecretKey(voice.EncryptionMode, []byte) error { return nil }
func (u *fakeUDPConn) SetDeadline(time.Time) error                     { return nil }
func (u *fakeUDPConn) SetReadDeadline(time.Time) error                 { return nil }
func (u *fakeUDPConn) SetWriteDeadline(time.Time) error                { return nil }
func (u *fakeUDPConn) Open(context.Context, string, int, uint32) (string, int, error) {
	return "", 0, nil
}
func (u *fakeUDPConn) Close() error {
	if u.closed.CompareAndSwap(false, true) {
		close(u.closeCh)
	}
	return nil
}
func (u *fakeUDPConn) Read([]byte) (int, error) {
	<-u.closeCh
	return 0, net.ErrClosed
}
func (u *fakeUDPConn) ReadPacket() (*voice.Packet, error) {
	if u.closed.Load() {
		return nil, net.ErrClosed
	}
	<-u.closeCh
	return nil, net.ErrClosed
}
func (u *fakeUDPConn) Write([]byte) (int, error) {
	if u.closed.Load() {
		return 0, net.ErrClosed
	}
	return 0, nil
}

// closeSignal signals the receiver goroutine's own exit: disgo's audio
// receiver calls its OpusFrameReceiver's Close as it shuts down.
type closeSignal struct {
	closed chan struct{}
	once   atomic.Bool
}

func (r *closeSignal) ReceiveOpusFrame(snowflake.ID, *voice.Packet) error { return nil }
func (r *closeSignal) CleanupUser(snowflake.ID)                           {}
func (r *closeSignal) Close() {
	if r.once.CompareAndSwap(false, true) {
		close(r.closed)
	}
}

// The #586 regression: a Voice Session that ends while its DAVE session never
// became ready must not leave disgo's audio goroutines behind. The receiver's
// loop is a non-blocking for/select that polls conn.DAVE().Ready() and returns
// — a full-core busy-wait — and its ONLY exit (ReadPacket → net.ErrClosed) sits
// behind that gate, as does the sender's (the #581 silence poke via
// ProvideOpusFrame). This drives disgo's REAL audio receiver + sender against
// the wrapped DAVE session through the exact teardown order Session.Close uses,
// then asserts the tick activity (DAVE() polls) returns to zero — i.e. the
// goroutines exited — not merely that the session state looks closed.
func TestAudioGoroutinesQuiesceAfterUnreadyTeardown(t *testing.T) {
	t.Parallel()

	inner := &holdingDaveSession{}
	inner.hold.Store(true) // mid-handshake for the whole (short) session: never ready
	dave := wrap(t, inner)

	udp := newFakeUDPConn()
	conn := &fakeDisgoConn{dave: dave, udp: udp}
	logger := slog.New(slog.DiscardHandler)

	sink := &closeSignal{closed: make(chan struct{})}
	receiver := voice.NewAudioReceiver(logger, sink, conn)
	receiver.Open()

	provider := &switchingProvider{}
	sender := voice.NewAudioSender(logger, provider, conn)
	sender.Open()

	// Both goroutines are live and polling the DAVE gate (the receiver at spin
	// speed). Without this pre-check a broken Open would make the quiescence
	// assertion below pass vacuously.
	waitFor(t, "audio goroutines to start polling DAVE()", func() bool {
		return conn.daveCalls.Load() > 10
	})

	// Session end, in Session.Close's order: transport down first, then the
	// DAVE session (disgo's conn.Close does gateway → udp → dave), then the
	// provider flips into its #581 sender-poke state.
	conn.torndown.Store(true)
	_ = udp.Close()
	_ = dave.Close()
	provider.Close()

	// The receiver must reap itself: next poll sees Ready()=true → ReadPacket
	// → net.ErrClosed → Close, which closes our sink.
	select {
	case <-sink.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("audio receiver goroutine did not exit after teardown; it is busy-spinning behind the DAVE gate")
	}

	// And the whole loop activity must return to baseline: once both goroutines
	// are gone, DAVE() is never polled again. A surviving receiver polls it
	// thousands of times per second; a surviving sender ~50/s.
	waitFor(t, "audio goroutine tick activity to stop", func() bool {
		before := conn.daveCalls.Load()
		time.Sleep(150 * time.Millisecond)
		return conn.daveCalls.Load() == before
	})
}

// waitFor polls cond until true or a 5s deadline, failing the test with what it
// was waiting on. The generous deadline keeps slow CI green; the happy path is
// milliseconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
