package audiobridge

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"

	pb "github.com/MrWong99/glyphoxa/gen/glyphoxa/v1"
)

// ---------------------------------------------------------------------------
// Mock gRPC bidi stream (server side)
// ---------------------------------------------------------------------------

// mockServerStream implements grpc.BidiStreamingServer[pb.AudioFrame, pb.AudioFrame].
type mockServerStream struct {
	ctx    context.Context
	cancel context.CancelFunc

	// recvCh: test feeds frames that the server reads via Recv().
	recvCh chan *pb.AudioFrame
	// sentMu protects sent.
	sentMu sync.Mutex
	// sent collects frames the server sends to the worker via Send().
	sent []*pb.AudioFrame
	// sendCh optionally delivers sent frames for streaming assertions.
	sendCh chan *pb.AudioFrame
}

func newMockServerStream() *mockServerStream {
	ctx, cancel := context.WithCancel(context.Background())
	return &mockServerStream{
		ctx:    ctx,
		cancel: cancel,
		recvCh: make(chan *pb.AudioFrame, 64),
		sendCh: make(chan *pb.AudioFrame, 64),
	}
}

func (m *mockServerStream) Recv() (*pb.AudioFrame, error) {
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

func (m *mockServerStream) Send(frame *pb.AudioFrame) error {
	select {
	case <-m.ctx.Done():
		return io.EOF
	default:
	}
	m.sentMu.Lock()
	m.sent = append(m.sent, frame)
	m.sentMu.Unlock()
	select {
	case m.sendCh <- frame:
	default:
	}
	return nil
}

func (m *mockServerStream) Sent() []*pb.AudioFrame {
	m.sentMu.Lock()
	defer m.sentMu.Unlock()
	cp := make([]*pb.AudioFrame, len(m.sent))
	copy(cp, m.sent)
	return cp
}

// grpc.ServerStream methods:

func (m *mockServerStream) SetHeader(metadata.MD) error  { return nil }
func (m *mockServerStream) SendHeader(metadata.MD) error { return nil }
func (m *mockServerStream) SetTrailer(metadata.MD)       {}
func (m *mockServerStream) Context() context.Context     { return m.ctx }
func (m *mockServerStream) SendMsg(any) error            { return nil }
func (m *mockServerStream) RecvMsg(any) error            { return nil }

// ---------------------------------------------------------------------------
// Tests: session bridge create / remove
// ---------------------------------------------------------------------------

func TestServer_NewSessionBridge(t *testing.T) {
	t.Parallel()

	srv := NewServer()

	bridge := srv.NewSessionBridge("sess-1")
	if bridge == nil {
		t.Fatal("NewSessionBridge returned nil")
	}
	if bridge.sessionID != "sess-1" {
		t.Errorf("session_id: got %q, want %q", bridge.sessionID, "sess-1")
	}

	// Verify the bridge is registered.
	srv.mu.RLock()
	_, ok := srv.bridges["sess-1"]
	srv.mu.RUnlock()
	if !ok {
		t.Fatal("bridge not registered in server map")
	}
}

func TestServer_RemoveBridge(t *testing.T) {
	t.Parallel()

	srv := NewServer()
	bridge := srv.NewSessionBridge("sess-1")

	srv.RemoveBridge("sess-1")

	// Bridge should be closed.
	select {
	case <-bridge.Done():
	default:
		t.Fatal("bridge.Done() should be closed after RemoveBridge")
	}

	// Should not be in the map.
	srv.mu.RLock()
	_, ok := srv.bridges["sess-1"]
	srv.mu.RUnlock()
	if ok {
		t.Fatal("bridge still in server map after RemoveBridge")
	}
}

func TestServer_RemoveBridge_UnknownSession(t *testing.T) {
	t.Parallel()

	srv := NewServer()
	// Should not panic.
	srv.RemoveBridge("does-not-exist")
}

// ---------------------------------------------------------------------------
// Tests: SessionBridge
// ---------------------------------------------------------------------------

func TestSessionBridge_SendToWorker(t *testing.T) {
	t.Parallel()

	srv := NewServer()
	bridge := srv.NewSessionBridge("sess-1")
	defer bridge.Close()

	frame := &pb.AudioFrame{SessionId: "sess-1", OpusData: []byte{1, 2, 3}}
	bridge.SendToWorker(frame)

	select {
	case got := <-bridge.toWorker:
		if got.GetSessionId() != "sess-1" {
			t.Errorf("session_id: got %q, want %q", got.GetSessionId(), "sess-1")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out reading from toWorker")
	}
}

func TestSessionBridge_SendToWorker_AfterClose(t *testing.T) {
	t.Parallel()

	srv := NewServer()
	bridge := srv.NewSessionBridge("sess-1")
	bridge.Close()

	// Should not block or panic.
	bridge.SendToWorker(&pb.AudioFrame{OpusData: []byte{1}})
}

func TestSessionBridge_ReceiveFromWorker(t *testing.T) {
	t.Parallel()

	srv := NewServer()
	bridge := srv.NewSessionBridge("sess-1")
	defer bridge.Close()

	frame := &pb.AudioFrame{SessionId: "sess-1", OpusData: []byte{4, 5, 6}}
	bridge.fromWorker <- frame

	select {
	case got := <-bridge.ReceiveFromWorker():
		if string(got.GetOpusData()) != string(frame.GetOpusData()) {
			t.Error("frame data mismatch")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out reading from ReceiveFromWorker")
	}
}

func TestSessionBridge_CloseIdempotent(t *testing.T) {
	t.Parallel()

	srv := NewServer()
	bridge := srv.NewSessionBridge("sess-1")

	bridge.Close()
	bridge.Close() // should not panic
}

// ---------------------------------------------------------------------------
// Tests: StreamAudio — handshake
// ---------------------------------------------------------------------------

func TestStreamAudio_HandshakeTimeout(t *testing.T) {
	t.Parallel()

	// Temporarily override the timeout for fast tests. We can't do that
	// because it's a const. Instead we just test with a short stream that
	// never sends anything — the stream closes before the timeout.
	// For a real timeout test we'd need to make the constant configurable.
	// Instead, test that a closed stream returns an error promptly.
	srv := NewServer()
	ms := newMockServerStream()

	// Close the recv channel immediately — simulates worker disconnect.
	close(ms.recvCh)

	err := srv.StreamAudio(ms)
	if err == nil {
		t.Fatal("expected error from StreamAudio when stream is closed")
	}
}

func TestStreamAudio_MissingSessionID(t *testing.T) {
	t.Parallel()

	srv := NewServer()
	ms := newMockServerStream()

	// Send a frame without session_id.
	ms.recvCh <- &pb.AudioFrame{OpusData: []byte{1, 2, 3}}

	err := srv.StreamAudio(ms)
	if err == nil {
		t.Fatal("expected error for missing session_id")
	}
}

func TestStreamAudio_UnknownSession(t *testing.T) {
	t.Parallel()

	srv := NewServer()
	ms := newMockServerStream()

	ms.recvCh <- &pb.AudioFrame{SessionId: "unknown-sess"}

	err := srv.StreamAudio(ms)
	if err == nil {
		t.Fatal("expected error for unknown session")
	}
}

// ---------------------------------------------------------------------------
// Tests: StreamAudio — bidirectional forwarding
// ---------------------------------------------------------------------------

func TestStreamAudio_GatewayToWorker(t *testing.T) {
	t.Parallel()

	srv := NewServer()
	bridge := srv.NewSessionBridge("sess-fw")
	defer srv.RemoveBridge("sess-fw")

	ms := newMockServerStream()

	// Send handshake from worker.
	ms.recvCh <- &pb.AudioFrame{SessionId: "sess-fw"}

	// Run StreamAudio in background.
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.StreamAudio(ms)
	}()

	// Give the stream time to start.
	time.Sleep(50 * time.Millisecond)

	// Send a frame from gateway → worker via the bridge.
	bridge.SendToWorker(&pb.AudioFrame{
		SessionId: "sess-fw",
		OpusData:  []byte{10, 20, 30},
	})

	// The mock stream should receive it via Send().
	select {
	case frame := <-ms.sendCh:
		if string(frame.GetOpusData()) != string([]byte{10, 20, 30}) {
			t.Error("frame data mismatch in gateway → worker direction")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for frame in gateway → worker direction")
	}

	// Close the stream to clean up.
	ms.cancel()
	<-errCh
}

func TestStreamAudio_WorkerToGateway(t *testing.T) {
	t.Parallel()

	srv := NewServer()
	bridge := srv.NewSessionBridge("sess-fw2")
	defer srv.RemoveBridge("sess-fw2")

	ms := newMockServerStream()

	// Handshake.
	ms.recvCh <- &pb.AudioFrame{SessionId: "sess-fw2"}

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.StreamAudio(ms)
	}()

	time.Sleep(50 * time.Millisecond)

	// Send a frame from worker → gateway.
	ms.recvCh <- &pb.AudioFrame{
		SessionId: "sess-fw2",
		OpusData:  []byte{40, 50, 60},
		UserId:    "user-1",
	}

	// Read it from the bridge's fromWorker channel.
	select {
	case frame := <-bridge.ReceiveFromWorker():
		if string(frame.GetOpusData()) != string([]byte{40, 50, 60}) {
			t.Error("frame data mismatch in worker → gateway direction")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for frame in worker → gateway direction")
	}

	ms.cancel()
	<-errCh
}

func TestStreamAudio_FirstFrameWithData(t *testing.T) {
	t.Parallel()

	srv := NewServer()
	bridge := srv.NewSessionBridge("sess-data")
	defer srv.RemoveBridge("sess-data")

	ms := newMockServerStream()

	// Handshake frame that also carries opus data.
	ms.recvCh <- &pb.AudioFrame{
		SessionId: "sess-data",
		OpusData:  []byte{99},
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.StreamAudio(ms)
	}()

	// The first frame's opus data should arrive on fromWorker.
	select {
	case frame := <-bridge.ReceiveFromWorker():
		if len(frame.GetOpusData()) == 0 || frame.GetOpusData()[0] != 99 {
			t.Error("first frame opus data not forwarded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first frame data")
	}

	ms.cancel()
	<-errCh
}

// ---------------------------------------------------------------------------
// Tests: goroutine cleanup
// ---------------------------------------------------------------------------

func TestStreamAudio_SendGoroutineExitsOnStreamEnd(t *testing.T) {
	t.Parallel()

	srv := NewServer()
	bridge := srv.NewSessionBridge("sess-leak")
	// Intentionally do NOT call RemoveBridge — this simulates the scenario
	// where the worker disconnects before the gateway cleans up the bridge.

	ms := newMockServerStream()
	ms.recvCh <- &pb.AudioFrame{SessionId: "sess-leak"}

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.StreamAudio(ms)
	}()

	time.Sleep(50 * time.Millisecond)

	// Close the recv side — simulates worker disconnect.
	close(ms.recvCh)

	// StreamAudio should return promptly (not hang due to leaked goroutine).
	select {
	case <-errCh:
		// Success: StreamAudio returned.
	case <-time.After(5 * time.Second):
		t.Fatal("StreamAudio did not return — possible goroutine leak")
	}

	// Now clean up the bridge.
	bridge.Close()
}

func TestStreamAudio_BridgeCloseEndsStream(t *testing.T) {
	t.Parallel()

	srv := NewServer()
	srv.NewSessionBridge("sess-close")

	ms := newMockServerStream()
	ms.recvCh <- &pb.AudioFrame{SessionId: "sess-close"}

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.StreamAudio(ms)
	}()

	time.Sleep(50 * time.Millisecond)

	// Close bridge while stream is active.
	srv.RemoveBridge("sess-close")

	select {
	case <-errCh:
		// Success: StreamAudio returned after bridge close.
	case <-time.After(5 * time.Second):
		t.Fatal("StreamAudio did not return after bridge close")
	}
}
