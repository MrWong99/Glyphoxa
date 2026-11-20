package voice

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave"
	"github.com/disgoorg/snowflake/v2"
	"go.uber.org/goleak"
)

// stateRecorder is the voice-state update func a guarded conn is handed in
// tests: it counts JOINs and LEAVEs that actually reach the "main gateway".
type stateRecorder struct {
	joins, leaves atomic.Int32
}

func (r *stateRecorder) update(_ context.Context, _ snowflake.ID, channel *snowflake.ID, _, _ bool) error {
	if channel == nil {
		r.leaves.Add(1)
	} else {
		r.joins.Add(1)
	}
	return nil
}

func TestConnGuardFencesStateUpdatesAfterDiscard(t *testing.T) {
	t.Parallel()
	var rec stateRecorder
	var removed atomic.Int32
	g := &connGuard{}
	update := g.fence(rec.update)
	discard := g.discard(func() { removed.Add(1) })

	channel := snowflake.ID(7)
	if err := update(context.Background(), 1, &channel, false, false); err != nil {
		t.Fatalf("JOIN before discard: %v", err)
	}
	if err := update(context.Background(), 1, nil, false, false); err != nil {
		t.Fatalf("LEAVE before discard: %v", err)
	}

	discard()

	if err := update(context.Background(), 1, nil, false, false); !errors.Is(err, errConnDiscarded) {
		t.Fatalf("LEAVE after discard = %v, want errConnDiscarded", err)
	}
	if err := update(context.Background(), 1, &channel, false, false); !errors.Is(err, errConnDiscarded) {
		t.Fatalf("JOIN after discard = %v, want errConnDiscarded", err)
	}
	if rec.joins.Load() != 1 || rec.leaves.Load() != 1 {
		t.Fatalf("updates that reached the gateway = %d JOIN / %d LEAVE, want 1 / 1", rec.joins.Load(), rec.leaves.Load())
	}
	if removed.Load() != 1 {
		t.Fatalf("remove func ran %d times, want 1", removed.Load())
	}
}

// guardedTestConn builds a conn exactly as TransportOption does — disgo's real
// connImpl, audio sender and audio receiver behind guardedConnCreate — over a
// fake voice gateway and a keepalive transport dialing the given fake sockets.
func guardedTestConn(t *testing.T, rec *stateRecorder, removed *atomic.Int32, ticks chan time.Time, conns ...*fakePacketConn) (voice.Conn, *keepaliveUDPConn) {
	t.Helper()
	var ku *keepaliveUDPConn
	conn := guardedConnCreate(1, 2, rec.update, func() { removed.Add(1) },
		voice.WithConnGatewayCreateFunc(func(godave.Session, voice.EventHandlerFunc, voice.CloseHandlerFunc, ...voice.GatewayConfigOpt) voice.Gateway {
			return &fakeVoiceGateway{ssrc: 42}
		}),
		voice.WithUDPConnCreateFunc(func(godave.Session, voice.SsrcLookupFunc, ...voice.UDPConnConfigOpt) voice.UDPConn {
			ku = newTestUDPConn(t, nil, ticks, conns...)
			return ku
		}),
	)
	if ku == nil {
		t.Fatal("guardedConnCreate did not build the transport")
	}
	return conn, ku
}

// TestGuardedConnRecoversInPlaceAndTearsDownOnce drives disgo's REAL audio
// sender and receiver through the sequence its v0.20.0 conn runs on a
// session-invalidating voice-gateway close — transport Close, then re-Open for
// a fresh session — and then through a final conn.Close. It is the end-to-end
// statement of the design: both audio directions survive the recovery (the
// Bot never has to leave the channel), and only the discard ends them.
func TestGuardedConnRecoversInPlaceAndTearsDownOnce(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	ticks := make(chan time.Time)
	fc1, fc2 := newFakePacketConn(), newFakePacketConn()
	var rec stateRecorder
	var removed atomic.Int32
	conn, ku := guardedTestConn(t, &rec, &removed, ticks, fc1, fc2)

	// Wire audio the way newSession does.
	provider := &switchingProvider{}
	dispatcher := newInboundDispatcher("1", 16, DiscardMetrics{})
	provider.onClose = dispatcher.Close
	open(t, ku, fc1, 42)
	if err := ku.SetSecretKey(voice.EncryptionModeAEADAES256GCMRTPSize, testSecretKey); err != nil {
		t.Fatalf("SetSecretKey: %v", err)
	}
	conn.SetOpusFrameProvider(provider)
	conn.SetOpusFrameReceiver(dispatcher)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider.swap(&playSlot{pb: newPlayback(cancel), src: endlessSource{}, ctx: ctx})

	// The payload's first byte carries the sequence number, so each check knows
	// it got the packet it just sealed.
	expectFrame := func(when string, fc *fakePacketConn, seq uint16) {
		t.Helper()
		payload := []byte{byte(seq), 0x02, 0x03, 0x04}
		fc.reads <- sealRTP(t, seq, 960, 9, payload)
		select {
		case f, ok := <-dispatcher.inbound():
			if !ok {
				t.Fatalf("%s: the inbound stream closed", when)
			}
			if !bytes.Equal(f.Opus, payload) {
				t.Fatalf("%s: inbound frame = %x, want %x", when, f.Opus, payload)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: no inbound frame; the receiver is gone", when)
		}
	}

	if !eventually(func() bool { return rtpWrites(fc1) > 0 }) {
		t.Fatal("before recovery: disgo's sender wrote no audio")
	}
	expectFrame("before recovery", fc1, 1)

	// disgo's recovery: udp.Close(), a new voice session, udp.Open().
	if err := ku.Close(); err != nil {
		t.Fatalf("transport Close: %v", err)
	}
	time.Sleep(60 * time.Millisecond) // several sender polls land in the gap
	select {
	case _, ok := <-dispatcher.inbound():
		if !ok {
			t.Fatal("the pause closed the inbound stream; the cycle would tear the session down")
		}
	default:
	}
	if provider.closed.Load() {
		t.Fatal("the pause made disgo's sender close its provider; outbound audio is gone")
	}
	open(t, ku, fc2, 43)
	if err := ku.SetSecretKey(voice.EncryptionModeAEADAES256GCMRTPSize, testSecretKey); err != nil {
		t.Fatalf("SetSecretKey after recovery: %v", err)
	}

	if !eventually(func() bool { return rtpWrites(fc2) > 0 }) {
		t.Fatal("after recovery: disgo's sender never wrote to the new socket")
	}
	// The last write may be a keepalive (8 bytes), which the transport sends on
	// the same socket concurrently: read the SSRC off the last AUDIO packet.
	sent := lastRTPWrite(fc2)
	if ssrc := binary.BigEndian.Uint32(sent[8:12]); ssrc != 43 {
		t.Fatalf("after recovery: outbound SSRC = %d, want 43", ssrc)
	}
	expectFrame("after recovery", fc2, 2)
	if rec.leaves.Load() != 0 {
		t.Fatalf("recovery sent %d LEAVE(s); the Bot must stay in the channel", rec.leaves.Load())
	}

	// Teardown, in Session.Close's order.
	conn.Close(context.Background())
	provider.Close()
	dispatcher.Close()

	if got := rec.leaves.Load(); got != 1 {
		t.Fatalf("teardown sent %d LEAVE(s), want exactly 1", got)
	}
	if got := removed.Load(); got != 1 {
		t.Fatalf("remove func ran %d times, want 1", got)
	}
	if !ku.isTerminated() {
		t.Fatal("conn.Close did not terminate the transport")
	}
	if !eventually(func() bool {
		select {
		case _, ok := <-dispatcher.inbound():
			return !ok
		default:
			return false
		}
	}) {
		t.Fatal("the inbound stream did not close on teardown")
	}

	// A second Close is what disgo's recovery goroutine does when its 30s
	// re-open times out on a conn the Session already discarded. Its LEAVE
	// must not reach the gateway: by then another conn may own the guild's
	// voice state.
	conn.Close(context.Background())
	if got := rec.leaves.Load(); got != 1 {
		t.Fatalf("a Close on a discarded conn sent a LEAVE (%d total); it would kick the live session", got)
	}
	// goleak (deferred) proves disgo's sender and receiver goroutines and both
	// keepalive loops are gone.
}

// TestGuardedConnDiscardedByDisgoEndsTheSession covers disgo closing the conn
// on its own — its recovery gave up — while the transport sits paused. Nothing
// else would ever wake the receiver; the discard has to.
func TestGuardedConnDiscardedByDisgoEndsTheSession(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	ticks := make(chan time.Time)
	fc := newFakePacketConn()
	var rec stateRecorder
	var removed atomic.Int32
	conn, ku := guardedTestConn(t, &rec, &removed, ticks, fc)

	provider := &switchingProvider{}
	dispatcher := newInboundDispatcher("1", 16, DiscardMetrics{})
	provider.onClose = dispatcher.Close
	open(t, ku, fc, 42)
	if err := ku.SetSecretKey(voice.EncryptionModeAEADAES256GCMRTPSize, testSecretKey); err != nil {
		t.Fatalf("SetSecretKey: %v", err)
	}
	conn.SetOpusFrameProvider(provider)
	conn.SetOpusFrameReceiver(dispatcher)

	if err := ku.Close(); err != nil { // recovery starts...
		t.Fatalf("transport Close: %v", err)
	}
	conn.Close(context.Background()) // ...and disgo gives up on it

	select {
	case _, ok := <-dispatcher.inbound():
		if ok {
			t.Fatal("unexpected inbound frame")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("disgo discarded the conn but the inbound stream stayed open; the session would hang deaf")
	}
	// The receiver's reap closed the dispatcher; what the wirenpc cycle does
	// next is Session.Close, whose provider.Close pokes the sender out.
	provider.Close()
}

func TestProviderCloseEndsTheInboundStream(t *testing.T) {
	t.Parallel()
	s, _ := openSession(t)
	defer s.Close()

	// disgo's audio sender closing its provider is the only notice that
	// outbound audio is gone; a Session that kept listening would be a mute
	// NPC nobody can detect.
	s.provider.Close()

	select {
	case _, ok := <-s.Inbound():
		if ok {
			t.Fatal("unexpected inbound frame")
		}
	case <-time.After(time.Second):
		t.Fatal("a provider closed by the sender left the inbound stream open")
	}
}
