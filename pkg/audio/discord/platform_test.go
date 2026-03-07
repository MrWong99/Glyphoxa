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
