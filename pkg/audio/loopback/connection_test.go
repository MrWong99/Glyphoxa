package loopback

import (
	"testing"
	"time"

	"github.com/MrWong99/glyphoxa/pkg/audio"
)

func TestConnection_InputStreams(t *testing.T) {
	t.Parallel()

	frames := []audio.AudioFrame{
		{Data: []byte{1, 2, 3, 4}, SampleRate: 16000, Channels: 1},
		{Data: []byte{5, 6, 7, 8}, SampleRate: 16000, Channels: 1},
	}

	conn := New([]Participant{
		{UserID: "player-1", Username: "Alice", Frames: frames},
	})
	defer func() { _ = conn.Disconnect() }()

	streams := conn.InputStreams()
	if len(streams) != 1 {
		t.Fatalf("expected 1 input stream, got %d", len(streams))
	}

	ch, ok := streams["player-1"]
	if !ok {
		t.Fatal("missing input stream for player-1")
	}

	// Read all frames.
	var got []audio.AudioFrame
	for f := range ch {
		got = append(got, f)
	}

	if len(got) != 2 {
		t.Fatalf("expected 2 frames, got %d", len(got))
	}
	if got[0].Data[0] != 1 || got[1].Data[0] != 5 {
		t.Error("frame data mismatch")
	}
}

func TestConnection_OutputCapture(t *testing.T) {
	t.Parallel()

	conn := New(nil) // no participants needed for output test

	out := conn.OutputStream()

	// Write some frames.
	for i := range 5 {
		out <- audio.AudioFrame{
			Data:       []byte{byte(i)},
			SampleRate: 48000,
			Channels:   2,
		}
	}

	// Wait for capture.
	if !conn.WaitForOutput(5, 2*time.Second) {
		t.Fatalf("expected 5 captured frames, got %d", conn.CapturedOutputCount())
	}

	captured := conn.CapturedOutput()
	if len(captured) != 5 {
		t.Fatalf("expected 5 captured frames, got %d", len(captured))
	}

	for i, f := range captured {
		if f.Data[0] != byte(i) {
			t.Errorf("frame %d: got data %d, want %d", i, f.Data[0], i)
		}
	}

	conn.Disconnect()
}

func TestConnection_Disconnect_Idempotent(t *testing.T) {
	t.Parallel()

	conn := New(nil)

	if err := conn.Disconnect(); err != nil {
		t.Fatalf("first Disconnect: %v", err)
	}
	if err := conn.Disconnect(); err != nil {
		t.Fatalf("second Disconnect: %v", err)
	}
}

func TestConnection_AddParticipant(t *testing.T) {
	t.Parallel()

	conn := New(nil) // start with no participants
	defer func() { _ = conn.Disconnect() }()

	var joined []audio.Event
	conn.OnParticipantChange(func(ev audio.Event) {
		joined = append(joined, ev)
	})

	conn.AddParticipant(Participant{
		UserID:   "player-2",
		Username: "Bob",
		Frames: []audio.AudioFrame{
			{Data: []byte{42}, SampleRate: 16000, Channels: 1},
		},
	})

	if len(joined) != 1 {
		t.Fatalf("expected 1 join event, got %d", len(joined))
	}
	if joined[0].UserID != "player-2" {
		t.Errorf("join event user: got %q, want %q", joined[0].UserID, "player-2")
	}

	// Verify the new stream is accessible.
	streams := conn.InputStreams()
	ch, ok := streams["player-2"]
	if !ok {
		t.Fatal("missing input stream for player-2")
	}

	f, ok := <-ch
	if !ok {
		t.Fatal("channel closed unexpectedly")
	}
	if f.Data[0] != 42 {
		t.Errorf("frame data: got %d, want 42", f.Data[0])
	}
}

func TestConnection_WaitForOutput_Timeout(t *testing.T) {
	t.Parallel()

	conn := New(nil)
	defer func() { _ = conn.Disconnect() }()

	// Wait for 10 frames but never write any — should timeout.
	if conn.WaitForOutput(10, 50*time.Millisecond) {
		t.Fatal("WaitForOutput should have returned false on timeout")
	}
}

func TestConnection_EmptyParticipants(t *testing.T) {
	t.Parallel()

	conn := New([]Participant{})
	defer func() { _ = conn.Disconnect() }()

	streams := conn.InputStreams()
	if len(streams) != 0 {
		t.Fatalf("expected 0 input streams, got %d", len(streams))
	}
}
