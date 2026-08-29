package voice

import (
	"context"
	"testing"
	"time"

	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
)

// fakeVoiceGateway is the minimal disgo voice.Gateway the speaking-event tests
// need: a stable identity for the speakingEvents map plus our own SSRC.
type fakeVoiceGateway struct {
	ssrc uint32
}

func (g *fakeVoiceGateway) SSRC() uint32                            { return g.ssrc }
func (g *fakeVoiceGateway) Open(context.Context, voice.State) error { return nil }
func (g *fakeVoiceGateway) Close()                                  {}
func (g *fakeVoiceGateway) CloseWithCode(int, string)               {}
func (g *fakeVoiceGateway) Status() voice.Status                    { return voice.StatusReady }
func (g *fakeVoiceGateway) Send(context.Context, voice.Opcode, voice.GatewayMessageData) error {
	return nil
}
func (g *fakeVoiceGateway) Latency() time.Duration { return 0 }

var _ voice.Gateway = (*fakeVoiceGateway)(nil)

func TestNoteSpeakingEventRecordsRemoteSpeakersOnly(t *testing.T) {
	gw := &fakeVoiceGateway{ssrc: 42}

	// A non-Speaking opcode records nothing.
	noteSpeakingEvent(gw, voice.OpcodeHeartbeatACK, 0, nil)
	if !speakingEvents.last(gw).IsZero() {
		t.Fatal("non-Speaking opcode was recorded")
	}

	// Our own speaking relayed back is not remote evidence: an NPC talking into
	// a quiet room must not arm the watchdog against that room.
	noteSpeakingEvent(gw, voice.OpcodeSpeaking, 0, voice.GatewayMessageDataSpeaking{SSRC: 42})
	if !speakingEvents.last(gw).IsZero() {
		t.Fatal("our own speaking echo was recorded as remote evidence")
	}

	noteSpeakingEvent(gw, voice.OpcodeSpeaking, 0, voice.GatewayMessageDataSpeaking{SSRC: 7})
	if speakingEvents.last(gw).IsZero() {
		t.Fatal("remote speaking was not recorded")
	}

	// Entries are per gateway: another connection's evidence is invisible here.
	other := &fakeVoiceGateway{ssrc: 42}
	if !speakingEvents.last(other).IsZero() {
		t.Fatal("speaking evidence leaked across gateways")
	}
}

func TestSpeakingLogPrunesStaleEntries(t *testing.T) {
	l := &speakingLog{entries: map[voice.Gateway]time.Time{}}
	base := time.Date(2026, 8, 29, 20, 0, 0, 0, time.UTC)

	// Fill past the prune threshold with entries already stale relative to the
	// write that triggers pruning.
	var gws []*fakeVoiceGateway
	for range 70 {
		gw := &fakeVoiceGateway{}
		gws = append(gws, gw)
		l.note(gw, base)
	}
	fresh := &fakeVoiceGateway{}
	l.note(fresh, base.Add(speakingStaleAfter+time.Minute))

	if got := l.last(fresh); got.IsZero() {
		t.Fatal("fresh entry was pruned")
	}
	pruned := 0
	for _, gw := range gws {
		if l.last(gw).IsZero() {
			pruned++
		}
	}
	if pruned == 0 {
		t.Fatal("no stale gateway entries were pruned; a long-lived shared client would leak one per session")
	}
}

// livenessConn satisfies the voiceConn seam AND the narrow transport assertion
// MediaLiveness makes — the shape of the real disgo conn.
type livenessConn struct {
	udp voice.UDPConn
	gw  voice.Gateway
}

func (c *livenessConn) Open(context.Context, snowflake.ID, bool, bool) error { return nil }
func (c *livenessConn) SetOpusFrameProvider(voice.OpusFrameProvider)         {}
func (c *livenessConn) SetOpusFrameReceiver(voice.OpusFrameReceiver)         {}
func (c *livenessConn) Close(context.Context)                                {}
func (c *livenessConn) UDP() voice.UDPConn                                   { return c.udp }
func (c *livenessConn) Gateway() voice.Gateway                               { return c.gw }

func TestSessionMediaLiveness(t *testing.T) {
	// No transport monitor (a stock or mock conn): explicitly NOT ok, so the
	// watchdog stays inert instead of reading zeros as a dead path.
	bare := &Session{conn: bareConn{}}
	if _, ok := bare.MediaLiveness(); ok {
		t.Fatal("MediaLiveness reported ok without a transport monitor")
	}

	gw := &fakeVoiceGateway{ssrc: 42}
	ku := newKeepaliveUDPConn(noopDave(), nil, discardLogger(), discardMetrics{})
	ku.packets.Store(5)
	ku.keepalives.Store(9)
	noteSpeakingEvent(gw, voice.OpcodeSpeaking, 0, voice.GatewayMessageDataSpeaking{SSRC: 7})

	s := &Session{conn: &livenessConn{udp: ku, gw: gw}}
	ml, ok := s.MediaLiveness()
	if !ok {
		t.Fatal("MediaLiveness not ok with the keepalive transport installed")
	}
	if ml.Packets != 5 || ml.Keepalives != 9 {
		t.Fatalf("counters = %d/%d, want 5/9", ml.Packets, ml.Keepalives)
	}
	if ml.LastSpeaking.IsZero() {
		t.Fatal("LastSpeaking not surfaced from the speaking log")
	}

	// The transport option installed but a foreign UDPConn behind it (a future
	// refactor swapping implementations): still not ok, never a false signal.
	s2 := &Session{conn: &livenessConn{udp: nil, gw: gw}}
	if _, ok := s2.MediaLiveness(); ok {
		t.Fatal("MediaLiveness reported ok over a non-keepalive UDPConn")
	}
}

// bareConn is a voiceConn without the transport assertion surface.
type bareConn struct{}

func (bareConn) Open(context.Context, snowflake.ID, bool, bool) error { return nil }
func (bareConn) SetOpusFrameProvider(voice.OpusFrameProvider)         {}
func (bareConn) SetOpusFrameReceiver(voice.OpusFrameReceiver)         {}
func (bareConn) Close(context.Context)                                {}
