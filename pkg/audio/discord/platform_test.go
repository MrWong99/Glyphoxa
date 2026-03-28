package discord

import (
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/glyphoxa/pkg/audio"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
)

// ─── compile-time interface assertions ───────────────────────────────────────

var _ audio.Platform = (*Platform)(nil)
var _ audio.Connection = (*Connection)(nil)
var _ voice.OpusFrameProvider = (*Connection)(nil)
var _ voice.OpusFrameReceiver = (*Connection)(nil)

// ─── test helpers ─────────────────────────────────────────────────────────────

// newTestConnection creates a Connection suitable for unit testing without
// a real Discord voice connection. It does not register with a voice.Conn.
func newTestConnection(t *testing.T) *Connection {
	t.Helper()
	c := &Connection{
		guildID:        snowflake.ID(123456),
		inputs:         make(map[snowflake.ID]chan audio.AudioFrame),
		decoders:       make(map[snowflake.ID]*opusDecoder),
		output:         make(chan audio.AudioFrame, outputChannelBuffer),
		done:           make(chan struct{}),
		disconnectConn: func() {}, // no-op for tests
	}
	// Create an encoder like the real constructor does.
	enc, err := newOpusEncoder()
	if err != nil {
		t.Fatalf("newOpusEncoder: %v", err)
	}
	c.encoder = enc
	c.conv = audio.FormatConverter{Target: audio.Format{SampleRate: opusSampleRate, Channels: opusChannels}}
	t.Cleanup(func() { _ = c.Disconnect() })
	return c
}

// ─── Platform tests ──────────────────────────────────────────────────────────

// TestNewPlatform verifies that New creates a Platform with the expected fields.
func TestNewPlatform(t *testing.T) {
	t.Parallel()

	guildID := snowflake.ID(789)
	p := New(nil, guildID)
	if p == nil {
		t.Fatal("New returned nil")
		return // unreachable; silences staticcheck SA5011
	}
	if p.guildID != guildID {
		t.Errorf("guildID = %v, want %v", p.guildID, guildID)
	}
}

// ─── Connection tests ─────────────────────────────────────────────────────────

// TestConnection_DisconnectIdempotent verifies that Disconnect can be called
// multiple times without panicking and returns nil on subsequent calls.
func TestConnection_DisconnectIdempotent(t *testing.T) {
	t.Parallel()

	c := newTestConnection(t)
	for i := range 3 {
		err := c.Disconnect()
		if err != nil {
			t.Fatalf("Disconnect[%d]: unexpected error: %v", i, err)
		}
	}
}

// TestConnection_InputStreamsEmpty verifies that InputStreams returns an empty
// map when no participants have sent audio.
func TestConnection_InputStreamsEmpty(t *testing.T) {
	t.Parallel()

	c := newTestConnection(t)
	streams := c.InputStreams()
	if streams == nil {
		t.Fatal("InputStreams returned nil")
	}
	if len(streams) != 0 {
		t.Errorf("InputStreams: want 0 entries, got %d", len(streams))
	}
}

// TestConnection_OutputStreamNotNil verifies that OutputStream returns a
// non-nil channel.
func TestConnection_OutputStreamNotNil(t *testing.T) {
	t.Parallel()

	c := newTestConnection(t)
	ch := c.OutputStream()
	if ch == nil {
		t.Fatal("OutputStream returned nil")
	}
}

// TestConnection_OnParticipantChangeRegisters verifies that a callback can
// be registered and replaced.
func TestConnection_OnParticipantChangeRegisters(t *testing.T) {
	t.Parallel()

	c := newTestConnection(t)

	called := make(chan audio.Event, 4)
	c.OnParticipantChange(func(ev audio.Event) {
		called <- ev
	})

	// Emit an event manually and verify callback is invoked.
	c.emitEvent(audio.Event{Type: audio.EventJoin, UserID: "test-user", Username: "Alice"})

	select {
	case ev := <-called:
		if ev.Type != audio.EventJoin {
			t.Errorf("event type = %v, want EventJoin", ev.Type)
		}
		if ev.UserID != "test-user" {
			t.Errorf("event UserID = %q, want %q", ev.UserID, "test-user")
		}
		if ev.Username != "Alice" {
			t.Errorf("event Username = %q, want %q", ev.Username, "Alice")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for participant change event")
	}

	// Replace the callback.
	called2 := make(chan audio.Event, 4)
	c.OnParticipantChange(func(ev audio.Event) {
		called2 <- ev
	})
	c.emitEvent(audio.Event{Type: audio.EventLeave, UserID: "test-user"})

	select {
	case ev := <-called2:
		if ev.Type != audio.EventLeave {
			t.Errorf("replaced callback: event type = %v, want EventLeave", ev.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event on replaced callback")
	}

	// Original callback should NOT receive the second event.
	select {
	case ev := <-called:
		t.Errorf("original callback should not receive events after replacement, got %v", ev)
	case <-time.After(50 * time.Millisecond):
		// expected
	}
}

// TestConnection_RecvDemux verifies that incoming Opus packets are demuxed
// by user ID and appear on separate input streams.
func TestConnection_RecvDemux(t *testing.T) {
	t.Parallel()

	c := newTestConnection(t)

	// Opus silence frame: 0xF8 0xFF 0xFE (3 bytes).
	silenceOpus := []byte{0xF8, 0xFF, 0xFE}

	user1 := snowflake.ID(100)
	user2 := snowflake.ID(200)

	// Simulate receiving packets from two different users.
	if err := c.ReceiveOpusFrame(user1, &voice.Packet{SSRC: 1, Opus: silenceOpus}); err != nil {
		t.Fatalf("ReceiveOpusFrame user1: %v", err)
	}
	if err := c.ReceiveOpusFrame(user2, &voice.Packet{SSRC: 2, Opus: silenceOpus}); err != nil {
		t.Fatalf("ReceiveOpusFrame user2: %v", err)
	}

	streams := c.InputStreams()
	if len(streams) != 2 {
		t.Fatalf("InputStreams: want 2 entries, got %d", len(streams))
	}
	if _, ok := streams[user1.String()]; !ok {
		t.Error("InputStreams: missing user1")
	}
	if _, ok := streams[user2.String()]; !ok {
		t.Error("InputStreams: missing user2")
	}

	// Drain a frame from each stream.
	for uid, ch := range streams {
		select {
		case frame := <-ch:
			if frame.SampleRate != opusSampleRate {
				t.Errorf("user %s: SampleRate = %d, want %d", uid, frame.SampleRate, opusSampleRate)
			}
			if frame.Channels != opusChannels {
				t.Errorf("user %s: Channels = %d, want %d", uid, frame.Channels, opusChannels)
			}
			if len(frame.Data) == 0 {
				t.Errorf("user %s: frame data is empty", uid)
			}
		case <-time.After(time.Second):
			t.Fatalf("user %s: timed out waiting for frame", uid)
		}
	}
}

// TestConnection_RecvJoinEvent verifies that receiving a frame from a new user
// emits an EventJoin.
func TestConnection_RecvJoinEvent(t *testing.T) {
	t.Parallel()

	c := newTestConnection(t)

	events := make(chan audio.Event, 4)
	c.OnParticipantChange(func(ev audio.Event) {
		events <- ev
	})

	userID := snowflake.ID(42)
	silenceOpus := []byte{0xF8, 0xFF, 0xFE}

	if err := c.ReceiveOpusFrame(userID, &voice.Packet{SSRC: 1, Opus: silenceOpus}); err != nil {
		t.Fatalf("ReceiveOpusFrame: %v", err)
	}

	select {
	case ev := <-events:
		if ev.Type != audio.EventJoin {
			t.Errorf("event type = %v, want EventJoin", ev.Type)
		}
		if ev.UserID != userID.String() {
			t.Errorf("event UserID = %q, want %q", ev.UserID, userID.String())
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for join event")
	}
}

// TestConnection_CleanupUserEvent verifies that CleanupUser closes the user's
// channel and emits an EventLeave.
func TestConnection_CleanupUserEvent(t *testing.T) {
	t.Parallel()

	c := newTestConnection(t)

	events := make(chan audio.Event, 4)
	c.OnParticipantChange(func(ev audio.Event) {
		events <- ev
	})

	userID := snowflake.ID(42)
	silenceOpus := []byte{0xF8, 0xFF, 0xFE}

	// First, create the user by receiving a frame.
	if err := c.ReceiveOpusFrame(userID, &voice.Packet{SSRC: 1, Opus: silenceOpus}); err != nil {
		t.Fatalf("ReceiveOpusFrame: %v", err)
	}

	// Drain the join event.
	select {
	case <-events:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for join event")
	}

	// Now clean up the user.
	c.CleanupUser(userID)

	select {
	case ev := <-events:
		if ev.Type != audio.EventLeave {
			t.Errorf("event type = %v, want EventLeave", ev.Type)
		}
		if ev.UserID != userID.String() {
			t.Errorf("event UserID = %q, want %q", ev.UserID, userID.String())
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for leave event")
	}

	// The user's input stream should be gone.
	streams := c.InputStreams()
	if len(streams) != 0 {
		t.Errorf("InputStreams: want 0 entries after cleanup, got %d", len(streams))
	}
}

// TestConnection_SendEncodes verifies that frames written to OutputStream
// are available via ProvideOpusFrame.
func TestConnection_SendEncodes(t *testing.T) {
	t.Parallel()

	c := newTestConnection(t)

	// Create a PCM frame of the right size for 20ms stereo 48kHz.
	// 960 samples * 2 channels * 2 bytes/sample = 3840 bytes.
	pcmSize := opusFrameSize * opusChannels * 2
	pcm := make([]byte, pcmSize)
	frame := audio.AudioFrame{
		Data:       pcm,
		SampleRate: opusSampleRate,
		Channels:   opusChannels,
	}

	c.OutputStream() <- frame

	// ProvideOpusFrame should return the encoded opus data.
	done := make(chan []byte, 1)
	go func() {
		opus, err := c.ProvideOpusFrame()
		if err != nil {
			t.Errorf("ProvideOpusFrame: %v", err)
			done <- nil
			return
		}
		done <- opus
	}()

	select {
	case opus := <-done:
		if len(opus) == 0 {
			t.Error("ProvideOpusFrame: received empty Opus packet")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Opus packet from ProvideOpusFrame")
	}
}

// TestConnection_ConcurrentDisconnect exercises Disconnect from multiple
// goroutines to verify thread safety (run with -race).
func TestConnection_ConcurrentDisconnect(t *testing.T) {
	t.Parallel()

	c := newTestConnection(t)
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			_ = c.Disconnect()
		})
	}
	wg.Wait()
}

// TestConnection_ReceiveNilPacket verifies that a nil packet is silently ignored.
func TestConnection_ReceiveNilPacket(t *testing.T) {
	t.Parallel()

	c := newTestConnection(t)
	if err := c.ReceiveOpusFrame(snowflake.ID(1), nil); err != nil {
		t.Fatalf("ReceiveOpusFrame(nil): %v", err)
	}
	streams := c.InputStreams()
	if len(streams) != 0 {
		t.Errorf("InputStreams: want 0 entries, got %d", len(streams))
	}
}

// TestConnection_Close verifies that the Close method delegates to Disconnect.
func TestConnection_Close(t *testing.T) {
	t.Parallel()

	c := newTestConnection(t)
	// Close should not panic and should be equivalent to Disconnect.
	c.Close()

	// After Close, the done channel should be closed.
	select {
	case <-c.done:
		// expected
	default:
		t.Error("done channel not closed after Close()")
	}
}

// TestInt16sToBytes verifies the int16-to-byte conversion.
func TestInt16sToBytes(t *testing.T) {
	t.Parallel()

	pcm := []int16{0x0102, -1, 0}
	b := int16sToBytes(pcm)

	if len(b) != 6 {
		t.Fatalf("len = %d, want 6", len(b))
	}
	// 0x0102 in little-endian: 0x02, 0x01
	if b[0] != 0x02 || b[1] != 0x01 {
		t.Errorf("bytes[0:2] = [%02x %02x], want [02 01]", b[0], b[1])
	}
	// -1 = 0xFFFF in little-endian: 0xFF, 0xFF
	if b[2] != 0xFF || b[3] != 0xFF {
		t.Errorf("bytes[2:4] = [%02x %02x], want [ff ff]", b[2], b[3])
	}
	// 0 = 0x0000
	if b[4] != 0x00 || b[5] != 0x00 {
		t.Errorf("bytes[4:6] = [%02x %02x], want [00 00]", b[4], b[5])
	}
}

// TestBytesToInt16s verifies the byte-to-int16 conversion.
func TestBytesToInt16s(t *testing.T) {
	t.Parallel()

	b := []byte{0x02, 0x01, 0xFF, 0xFF, 0x00, 0x00}
	pcm := bytesToInt16s(b)

	if len(pcm) != 3 {
		t.Fatalf("len = %d, want 3", len(pcm))
	}
	if pcm[0] != 0x0102 {
		t.Errorf("pcm[0] = %d, want %d", pcm[0], 0x0102)
	}
	if pcm[1] != -1 {
		t.Errorf("pcm[1] = %d, want -1", pcm[1])
	}
	if pcm[2] != 0 {
		t.Errorf("pcm[2] = %d, want 0", pcm[2])
	}
}

// TestInt16sToBytes_RoundTrip verifies that converting to bytes and back
// produces the original values.
func TestInt16sToBytes_RoundTrip(t *testing.T) {
	t.Parallel()

	original := []int16{100, -200, 300, -400, 0, 32767, -32768}
	b := int16sToBytes(original)
	result := bytesToInt16s(b)

	if len(result) != len(original) {
		t.Fatalf("round-trip len = %d, want %d", len(result), len(original))
	}
	for i := range original {
		if result[i] != original[i] {
			t.Errorf("round-trip[%d] = %d, want %d", i, result[i], original[i])
		}
	}
}

// TestProvideOpusFrame_AfterDisconnect verifies that ProvideOpusFrame returns
// io.EOF after the connection is disconnected.
func TestProvideOpusFrame_AfterDisconnect(t *testing.T) {
	t.Parallel()

	c := newTestConnection(t)
	_ = c.Disconnect()

	_, err := c.ProvideOpusFrame()
	if err == nil {
		t.Fatal("expected error after disconnect")
	}
}

// TestConnection_EmitEvent_NilCallback verifies that emitting an event with
// no callback registered does not panic.
func TestConnection_EmitEvent_NilCallback(t *testing.T) {
	t.Parallel()

	c := newTestConnection(t)
	// No callback registered — should not panic.
	c.emitEvent(audio.Event{Type: audio.EventJoin, UserID: "test"})
}

// TestCleanupUser_Nonexistent verifies that cleaning up a nonexistent user
// does not emit a leave event.
func TestCleanupUser_Nonexistent(t *testing.T) {
	t.Parallel()

	c := newTestConnection(t)

	events := make(chan audio.Event, 4)
	c.OnParticipantChange(func(ev audio.Event) {
		events <- ev
	})

	// Clean up a user that never joined.
	c.CleanupUser(snowflake.ID(999))

	// No event should be emitted.
	select {
	case ev := <-events:
		t.Errorf("unexpected event for nonexistent user: %+v", ev)
	case <-time.After(50 * time.Millisecond):
		// expected
	}
}

// ─── Self-hearing guard tests ──────────────────────────────────────────────

// TestConnection_SelfHearingGuard verifies that frames from the bot's own
// user ID are silently dropped by ReceiveOpusFrame.
func TestConnection_SelfHearingGuard(t *testing.T) {
	t.Parallel()

	c := newTestConnection(t)

	botID := snowflake.ID(999)
	c.SetBotUserID(botID.String())

	silenceOpus := []byte{0xF8, 0xFF, 0xFE}

	// Frame from the bot's own user ID should be dropped.
	if err := c.ReceiveOpusFrame(botID, &voice.Packet{SSRC: 1, Opus: silenceOpus}); err != nil {
		t.Fatalf("ReceiveOpusFrame(botID): %v", err)
	}

	// No input stream should be created for the bot.
	streams := c.InputStreams()
	if len(streams) != 0 {
		t.Errorf("InputStreams: want 0 entries after bot frame, got %d", len(streams))
	}
}

// TestConnection_SelfHearingGuardAllowsOthers verifies that the self-hearing
// guard does not affect frames from other users.
func TestConnection_SelfHearingGuardAllowsOthers(t *testing.T) {
	t.Parallel()

	c := newTestConnection(t)

	botID := snowflake.ID(999)
	otherID := snowflake.ID(123)
	c.SetBotUserID(botID.String())

	silenceOpus := []byte{0xF8, 0xFF, 0xFE}

	// Frame from another user should go through.
	if err := c.ReceiveOpusFrame(otherID, &voice.Packet{SSRC: 2, Opus: silenceOpus}); err != nil {
		t.Fatalf("ReceiveOpusFrame(otherID): %v", err)
	}

	streams := c.InputStreams()
	if len(streams) != 1 {
		t.Fatalf("InputStreams: want 1 entry, got %d", len(streams))
	}
	if _, ok := streams[otherID.String()]; !ok {
		t.Error("InputStreams: missing stream for otherID")
	}
}

// TestConnection_SelfHearingGuardNoID verifies that when no bot user ID is set,
// all frames pass through (backwards compatible).
func TestConnection_SelfHearingGuardNoID(t *testing.T) {
	t.Parallel()

	c := newTestConnection(t)
	// No SetBotUserID call — guard should be inactive.

	silenceOpus := []byte{0xF8, 0xFF, 0xFE}
	userID := snowflake.ID(42)

	if err := c.ReceiveOpusFrame(userID, &voice.Packet{SSRC: 1, Opus: silenceOpus}); err != nil {
		t.Fatalf("ReceiveOpusFrame: %v", err)
	}

	streams := c.InputStreams()
	if len(streams) != 1 {
		t.Fatalf("InputStreams: want 1 entry, got %d", len(streams))
	}
}
