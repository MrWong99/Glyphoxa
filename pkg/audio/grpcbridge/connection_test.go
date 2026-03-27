package grpcbridge

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"
	"layeh.com/gopus"

	pb "github.com/MrWong99/glyphoxa/gen/glyphoxa/v1"
	"github.com/MrWong99/glyphoxa/pkg/audio"
)

// ---------------------------------------------------------------------------
// Mock gRPC bidi stream (client side)
// ---------------------------------------------------------------------------

// mockStream implements grpc.BidiStreamingClient[pb.AudioFrame, pb.AudioFrame].
type mockStream struct {
	ctx    context.Context
	cancel context.CancelFunc

	// recvCh: test → Connection (simulates frames arriving from the gateway).
	recvCh chan *pb.AudioFrame
	// sentMu protects sent.
	sentMu sync.Mutex
	// sent collects frames the Connection sends to the gateway.
	sent []*pb.AudioFrame
}

func newMockStream() *mockStream {
	ctx, cancel := context.WithCancel(context.Background())
	return &mockStream{
		ctx:    ctx,
		cancel: cancel,
		recvCh: make(chan *pb.AudioFrame, 128),
	}
}

func (m *mockStream) Send(frame *pb.AudioFrame) error {
	select {
	case <-m.ctx.Done():
		return io.EOF
	default:
	}
	m.sentMu.Lock()
	m.sent = append(m.sent, frame)
	m.sentMu.Unlock()
	return nil
}

func (m *mockStream) Recv() (*pb.AudioFrame, error) {
	select {
	case <-m.ctx.Done():
		return nil, io.EOF
	case f, ok := <-m.recvCh:
		if !ok {
			return nil, io.EOF
		}
		return f, nil
	}
}

func (m *mockStream) Sent() []*pb.AudioFrame {
	m.sentMu.Lock()
	defer m.sentMu.Unlock()
	cp := make([]*pb.AudioFrame, len(m.sent))
	copy(cp, m.sent)
	return cp
}

func (m *mockStream) Close() {
	m.cancel()
}

// grpc.ClientStream methods:

func (m *mockStream) Header() (metadata.MD, error) { return nil, nil }
func (m *mockStream) Trailer() metadata.MD         { return nil }
func (m *mockStream) CloseSend() error             { m.cancel(); return nil }
func (m *mockStream) Context() context.Context     { return m.ctx }
func (m *mockStream) SendMsg(any) error            { return nil }
func (m *mockStream) RecvMsg(any) error            { return nil }

// ---------------------------------------------------------------------------
// Helper: encode silence as a valid opus packet.
// ---------------------------------------------------------------------------

func encodeOpusSilence(t *testing.T) []byte {
	t.Helper()
	enc, err := gopus.NewEncoder(opusSampleRate, opusChannels, gopus.Audio)
	if err != nil {
		t.Fatalf("create opus encoder: %v", err)
	}
	pcm := make([]int16, opusFrameSize*opusChannels) // silence
	opus, err := enc.Encode(pcm, opusFrameSize, opusFrameBytes)
	if err != nil {
		t.Fatalf("encode opus silence: %v", err)
	}
	return opus
}

// ---------------------------------------------------------------------------
// Tests: byte-conversion helpers
// ---------------------------------------------------------------------------

func TestInt16sToBytes_RoundTrip(t *testing.T) {
	t.Parallel()

	samples := []int16{0, 1, -1, 32767, -32768, 256, -256}
	b := int16sToBytes(samples)
	got := bytesToInt16s(b)

	if len(got) != len(samples) {
		t.Fatalf("length: got %d, want %d", len(got), len(samples))
	}
	for i := range samples {
		if got[i] != samples[i] {
			t.Errorf("sample[%d]: got %d, want %d", i, got[i], samples[i])
		}
	}
}

func TestInt16sToBytes_Empty(t *testing.T) {
	t.Parallel()

	b := int16sToBytes(nil)
	if len(b) != 0 {
		t.Fatalf("expected empty, got %d bytes", len(b))
	}
	pcm := bytesToInt16s(nil)
	if len(pcm) != 0 {
		t.Fatalf("expected empty, got %d samples", len(pcm))
	}
}

// ---------------------------------------------------------------------------
// Tests: Connection lifecycle
// ---------------------------------------------------------------------------

func TestNew_SendsHandshake(t *testing.T) {
	t.Parallel()

	ms := newMockStream()
	conn, err := New("sess-1", ms)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = conn.Disconnect() }()

	sent := ms.Sent()
	if len(sent) == 0 {
		t.Fatal("expected handshake frame to be sent")
	}
	if sent[0].GetSessionId() != "sess-1" {
		t.Errorf("handshake session_id: got %q, want %q", sent[0].GetSessionId(), "sess-1")
	}
	if len(sent[0].GetOpusData()) != 0 {
		t.Error("handshake frame should not contain opus data")
	}
}

func TestConnection_DisconnectIdempotent(t *testing.T) {
	t.Parallel()

	ms := newMockStream()
	conn, err := New("sess-1", ms)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := conn.Disconnect(); err != nil {
		t.Fatalf("first Disconnect: %v", err)
	}
	if err := conn.Disconnect(); err != nil {
		t.Fatalf("second Disconnect: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tests: recv loop — per-user demuxing
// ---------------------------------------------------------------------------

func TestConnection_RecvDemuxByUser(t *testing.T) {
	t.Parallel()

	ms := newMockStream()
	conn, err := New("sess-1", ms)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = conn.Disconnect() }()

	opus := encodeOpusSilence(t)

	// Send frames from two different users.
	ms.recvCh <- &pb.AudioFrame{SessionId: "sess-1", OpusData: opus, UserId: "user-A", Ssrc: 1}
	ms.recvCh <- &pb.AudioFrame{SessionId: "sess-1", OpusData: opus, UserId: "user-B", Ssrc: 2}
	// Second frame from user-A.
	ms.recvCh <- &pb.AudioFrame{SessionId: "sess-1", OpusData: opus, UserId: "user-A", Ssrc: 1}

	// Wait for channels to appear.
	deadline := time.After(2 * time.Second)
	for {
		streams := conn.InputStreams()
		if len(streams) >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for 2 input streams, got %d", len(conn.InputStreams()))
		case <-time.After(10 * time.Millisecond):
		}
	}

	streams := conn.InputStreams()

	chA, ok := streams["user-A"]
	if !ok {
		t.Fatal("missing input stream for user-A")
	}
	chB, ok := streams["user-B"]
	if !ok {
		t.Fatal("missing input stream for user-B")
	}

	// Drain user-A (should have 2 frames).
	var countA int
	for countA < 2 {
		select {
		case <-chA:
			countA++
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out reading from user-A, got %d frames", countA)
		}
	}

	// Drain user-B (should have 1 frame).
	select {
	case <-chB:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out reading from user-B")
	}
}

func TestConnection_RecvSkipsEmptyFrames(t *testing.T) {
	t.Parallel()

	ms := newMockStream()
	conn, err := New("sess-1", ms)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = conn.Disconnect() }()

	// Frame with no user_id should be silently skipped.
	ms.recvCh <- &pb.AudioFrame{SessionId: "sess-1", OpusData: []byte{1, 2, 3}}
	// Frame with no opus data should be silently skipped.
	ms.recvCh <- &pb.AudioFrame{SessionId: "sess-1", UserId: "user-A"}

	// Give the recv loop time to process.
	time.Sleep(50 * time.Millisecond)

	streams := conn.InputStreams()
	if len(streams) != 0 {
		t.Fatalf("expected 0 input streams, got %d", len(streams))
	}
}

// ---------------------------------------------------------------------------
// Tests: send loop — PCM → opus encoding
// ---------------------------------------------------------------------------

func TestConnection_SendEncodesOpus(t *testing.T) {
	t.Parallel()

	ms := newMockStream()
	conn, err := New("sess-1", ms)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = conn.Disconnect() }()

	// Write a full opus frame worth of PCM (48kHz stereo = 3840 bytes per 20ms).
	pcm := make([]byte, opusFrameBytes)
	conn.OutputStream() <- audio.AudioFrame{
		Data:       pcm,
		SampleRate: opusSampleRate,
		Channels:   opusChannels,
	}

	// Wait for the encoded frame to appear in the mock stream's sent list.
	deadline := time.After(2 * time.Second)
	for {
		sent := ms.Sent()
		// The first sent frame is the handshake; look for subsequent ones.
		if len(sent) > 1 {
			frame := sent[1]
			if len(frame.GetOpusData()) > 0 {
				if frame.GetSessionId() != "sess-1" {
					t.Errorf("sent frame session_id: got %q, want %q", frame.GetSessionId(), "sess-1")
				}
				return // success
			}
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for encoded opus frame to be sent")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// ---------------------------------------------------------------------------
// Tests: participant join events
// ---------------------------------------------------------------------------

func TestConnection_ParticipantJoinEvent(t *testing.T) {
	t.Parallel()

	ms := newMockStream()
	conn, err := New("sess-1", ms)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = conn.Disconnect() }()

	var (
		eventsMu sync.Mutex
		events   []audio.Event
	)
	conn.OnParticipantChange(func(ev audio.Event) {
		eventsMu.Lock()
		events = append(events, ev)
		eventsMu.Unlock()
	})

	opus := encodeOpusSilence(t)
	ms.recvCh <- &pb.AudioFrame{SessionId: "sess-1", OpusData: opus, UserId: "user-X", Ssrc: 1}

	deadline := time.After(2 * time.Second)
	for {
		eventsMu.Lock()
		n := len(events)
		eventsMu.Unlock()
		if n >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for join event")
		case <-time.After(10 * time.Millisecond):
		}
	}

	eventsMu.Lock()
	defer eventsMu.Unlock()

	if events[0].Type != audio.EventJoin {
		t.Errorf("event type: got %v, want EventJoin", events[0].Type)
	}
	if events[0].UserID != "user-X" {
		t.Errorf("event user_id: got %q, want %q", events[0].UserID, "user-X")
	}
}

func TestConnection_NoDuplicateJoinEvents(t *testing.T) {
	t.Parallel()

	ms := newMockStream()
	conn, err := New("sess-1", ms)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = conn.Disconnect() }()

	var (
		eventsMu sync.Mutex
		events   []audio.Event
	)
	conn.OnParticipantChange(func(ev audio.Event) {
		eventsMu.Lock()
		events = append(events, ev)
		eventsMu.Unlock()
	})

	opus := encodeOpusSilence(t)
	// Send multiple frames from the same user.
	for range 5 {
		ms.recvCh <- &pb.AudioFrame{SessionId: "sess-1", OpusData: opus, UserId: "user-Y", Ssrc: 1}
	}

	// Wait for processing.
	time.Sleep(200 * time.Millisecond)

	eventsMu.Lock()
	defer eventsMu.Unlock()
	if len(events) != 1 {
		t.Errorf("expected exactly 1 join event, got %d", len(events))
	}
}

// ---------------------------------------------------------------------------
// Tests: InputStreams snapshot
// ---------------------------------------------------------------------------

func TestConnection_InputStreamsSnapshot(t *testing.T) {
	t.Parallel()

	ms := newMockStream()
	conn, err := New("sess-1", ms)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = conn.Disconnect() }()

	// Initially empty.
	streams := conn.InputStreams()
	if len(streams) != 0 {
		t.Fatalf("expected 0 streams initially, got %d", len(streams))
	}

	// Add a user.
	opus := encodeOpusSilence(t)
	ms.recvCh <- &pb.AudioFrame{SessionId: "sess-1", OpusData: opus, UserId: "user-Z", Ssrc: 1}

	deadline := time.After(2 * time.Second)
	for {
		streams = conn.InputStreams()
		if len(streams) == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for input stream")
		case <-time.After(10 * time.Millisecond):
		}
	}

	if _, ok := streams["user-Z"]; !ok {
		t.Error("missing stream for user-Z")
	}
}

// ---------------------------------------------------------------------------
// Tests: OutputStream returns a writable channel
// ---------------------------------------------------------------------------

func TestConnection_OutputStream(t *testing.T) {
	t.Parallel()

	ms := newMockStream()
	conn, err := New("sess-1", ms)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = conn.Disconnect() }()

	out := conn.OutputStream()
	if out == nil {
		t.Fatal("OutputStream returned nil")
	}

	// Should be able to write without blocking.
	select {
	case out <- audio.AudioFrame{Data: []byte{0, 0}, SampleRate: opusSampleRate, Channels: opusChannels}:
	case <-time.After(time.Second):
		t.Fatal("OutputStream write blocked")
	}
}

// ---------------------------------------------------------------------------
// Tests: opus encode/decode roundtrip
// ---------------------------------------------------------------------------

func TestOpusRoundTrip(t *testing.T) {
	t.Parallel()

	// Encode a known PCM frame to opus.
	enc, err := gopus.NewEncoder(opusSampleRate, opusChannels, gopus.Audio)
	if err != nil {
		t.Fatalf("create encoder: %v", err)
	}

	pcmSamples := make([]int16, opusFrameSize*opusChannels)
	// Fill with a simple tone.
	for i := range pcmSamples {
		pcmSamples[i] = int16((i % 100) * 100)
	}

	opusData, err := enc.Encode(pcmSamples, opusFrameSize, opusFrameBytes)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	// Decode it back.
	dec, err := gopus.NewDecoder(opusSampleRate, opusChannels)
	if err != nil {
		t.Fatalf("create decoder: %v", err)
	}

	decoded, err := dec.Decode(opusData, opusFrameSize, false)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Opus is lossy, so exact match isn't expected. But the length should match.
	if len(decoded) != opusFrameSize*opusChannels {
		t.Errorf("decoded sample count: got %d, want %d", len(decoded), opusFrameSize*opusChannels)
	}
}

// ---------------------------------------------------------------------------
// Tests: self-hearing guard
// ---------------------------------------------------------------------------

func TestConnection_SelfHearingGuard(t *testing.T) {
	t.Parallel()

	ms := newMockStream()
	conn, err := New("sess-guard", ms)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = conn.Disconnect() }()

	conn.SetBotUserID("bot-user-1")

	opus := encodeOpusSilence(t)

	// Frame from the bot should be dropped.
	ms.recvCh <- &pb.AudioFrame{SessionId: "sess-guard", OpusData: opus, UserId: "bot-user-1", Ssrc: 1}
	// Frame from another user should go through.
	ms.recvCh <- &pb.AudioFrame{SessionId: "sess-guard", OpusData: opus, UserId: "real-player", Ssrc: 2}

	// Wait for the real player's stream to appear.
	deadline := time.After(2 * time.Second)
	for {
		streams := conn.InputStreams()
		if len(streams) >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for player input stream")
		case <-time.After(10 * time.Millisecond):
		}
	}

	streams := conn.InputStreams()
	if _, ok := streams["bot-user-1"]; ok {
		t.Error("bot user should NOT have an input stream")
	}
	if _, ok := streams["real-player"]; !ok {
		t.Error("real player should have an input stream")
	}
}

// ---------------------------------------------------------------------------
// Tests: Flush race (3.4) — concurrent Flush and sendLoop
// ---------------------------------------------------------------------------

func TestConnection_FlushNoRace(t *testing.T) {
	t.Parallel()

	ms := newMockStream()
	conn, err := New("sess-flush", ms)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = conn.Disconnect() }()

	// Fill the output channel with frames.
	for range 10 {
		conn.OutputStream() <- audio.AudioFrame{
			Data:       make([]byte, opusFrameBytes),
			SampleRate: opusSampleRate,
			Channels:   opusChannels,
		}
	}

	// Flush should not race with sendLoop (run with -race to verify).
	conn.Flush()

	// Give sendLoop time to process the flush.
	time.Sleep(50 * time.Millisecond)

	// Verify the output channel was drained by checking that sendLoop processed
	// the flush (no crash, no race). The test primarily validates via -race.
	// Writing another frame after flush should succeed without blocking.
	select {
	case conn.OutputStream() <- audio.AudioFrame{
		Data:       make([]byte, opusFrameBytes),
		SampleRate: opusSampleRate,
		Channels:   opusChannels,
	}:
		// success — channel has space, confirming it was drained
	case <-time.After(time.Second):
		t.Error("output channel write blocked after flush — channel was not drained")
	}
}
