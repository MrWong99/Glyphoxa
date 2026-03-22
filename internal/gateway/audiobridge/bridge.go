// Package audiobridge provides the per-session audio bridge that connects a
// gateway's Discord voice connection to a worker's audio pipeline over gRPC.
//
// The bridge maintains two channels per session:
//   - toWorker: opus frames flowing from Discord → worker
//   - fromWorker: opus frames flowing from worker → Discord
//
// The gRPC AudioBridgeService streams these channels as a bidirectional
// gRPC stream. The gateway side writes to/reads from the channels via the
// voice.OpusFrameReceiver/Provider interfaces. The worker side writes to/reads
// from the channels via the gRPC stream.
package audiobridge

import (
	"fmt"
	"log/slog"
	"sync"

	"google.golang.org/grpc"

	pb "github.com/MrWong99/glyphoxa/gen/glyphoxa/v1"
)

// Server implements the AudioBridgeService gRPC service on the gateway side.
// It manages per-session bridges and handles the bidirectional opus stream.
//
// All methods are safe for concurrent use.
type Server struct {
	pb.UnimplementedAudioBridgeServiceServer

	mu      sync.RWMutex
	bridges map[string]*SessionBridge // sessionID -> bridge
}

// SessionBridge bridges audio for a single session between the gateway's
// Discord voice.Conn and the worker's gRPC stream.
type SessionBridge struct {
	sessionID string

	// toWorker receives frames from the Discord voice connection to be sent
	// to the worker via the gRPC stream.
	toWorker chan *pb.AudioFrame

	// fromWorker receives frames from the worker via the gRPC stream to be
	// sent to the Discord voice connection.
	fromWorker chan *pb.AudioFrame

	// done is closed when the bridge is torn down.
	done      chan struct{}
	closeOnce sync.Once
}

// NewServer creates the gRPC audio bridge server.
func NewServer() *Server {
	return &Server{
		bridges: make(map[string]*SessionBridge),
	}
}

// Register adds the Server to a gRPC server.
func (s *Server) Register(gs *grpc.Server) {
	pb.RegisterAudioBridgeServiceServer(gs, s)
}

// NewSessionBridge creates and registers a bridge for a session.
// The caller must call RemoveBridge when the session ends.
func (s *Server) NewSessionBridge(sessionID string) *SessionBridge {
	bridge := &SessionBridge{
		sessionID:  sessionID,
		toWorker:   make(chan *pb.AudioFrame, 128),
		fromWorker: make(chan *pb.AudioFrame, 128),
		done:       make(chan struct{}),
	}

	s.mu.Lock()
	s.bridges[sessionID] = bridge
	s.mu.Unlock()

	slog.Info("audiobridge: session bridge created", "session_id", sessionID)
	return bridge
}

// RemoveBridge unregisters and closes a session's bridge.
func (s *Server) RemoveBridge(sessionID string) {
	s.mu.Lock()
	bridge, ok := s.bridges[sessionID]
	if ok {
		delete(s.bridges, sessionID)
	}
	s.mu.Unlock()

	if ok {
		bridge.Close()
		slog.Info("audiobridge: session bridge removed", "session_id", sessionID)
	}
}

// StreamAudio implements the gRPC AudioBridgeService.StreamAudio RPC.
// The worker connects to this stream after receiving a StartSession command.
// The first frame from the worker must include a session_id to identify which
// bridge to attach to.
func (s *Server) StreamAudio(stream grpc.BidiStreamingServer[pb.AudioFrame, pb.AudioFrame]) error {
	// Read the first frame to identify the session.
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	sessionID := first.GetSessionId()
	if sessionID == "" {
		return fmt.Errorf("audiobridge: first frame must include session_id")
	}

	s.mu.RLock()
	bridge, ok := s.bridges[sessionID]
	s.mu.RUnlock()
	if !ok {
		slog.Warn("audiobridge: stream for unknown session", "session_id", sessionID)
		return fmt.Errorf("audiobridge: session %q not found", sessionID)
	}

	slog.Info("audiobridge: worker stream attached", "session_id", sessionID)

	// Forward the first frame if it has data.
	if len(first.GetOpusData()) > 0 {
		select {
		case bridge.fromWorker <- first:
		default:
		}
	}

	errCh := make(chan error, 2)

	// Gateway → Worker: forward frames from the Discord voice connection.
	go func() {
		for {
			select {
			case <-bridge.done:
				errCh <- nil
				return
			case frame, ok := <-bridge.toWorker:
				if !ok {
					errCh <- nil
					return
				}
				if err := stream.Send(frame); err != nil {
					errCh <- err
					return
				}
			}
		}
	}()

	// Worker → Gateway: receive frames from the worker.
	go func() {
		for {
			frame, err := stream.Recv()
			if err != nil {
				errCh <- err
				return
			}
			select {
			case bridge.fromWorker <- frame:
			case <-bridge.done:
				errCh <- nil
				return
			default:
				// Drop frame if channel full.
			}
		}
	}()

	// Wait for either direction to finish.
	streamErr := <-errCh

	slog.Info("audiobridge: worker stream detached",
		"session_id", sessionID,
		"err", streamErr,
	)
	return streamErr
}

// SendToWorker enqueues a frame to be sent to the worker. Non-blocking; drops
// frames if the channel is full.
func (b *SessionBridge) SendToWorker(frame *pb.AudioFrame) {
	select {
	case b.toWorker <- frame:
	case <-b.done:
	default:
	}
}

// ReceiveFromWorker returns the channel that delivers frames from the worker.
func (b *SessionBridge) ReceiveFromWorker() <-chan *pb.AudioFrame {
	return b.fromWorker
}

// Done returns a channel that is closed when the bridge is torn down.
func (b *SessionBridge) Done() <-chan struct{} {
	return b.done
}

// Close tears down the bridge. Safe to call multiple times.
func (b *SessionBridge) Close() {
	b.closeOnce.Do(func() {
		close(b.done)
	})
}
