// Package loopback provides a test implementation of [audio.Connection] that
// feeds pre-loaded audio frames as input and captures all output frames.
//
// Use this for integration testing the full voice pipeline (VAD→STT→LLM→TTS→Mixer)
// without a real voice platform (Discord, WebRTC, etc.).
//
// Typical usage:
//
//	conn := loopback.New([]loopback.Participant{
//	    {UserID: "player-1", Username: "Alice", Frames: testFrames},
//	})
//	defer conn.Disconnect()
//
//	// Wire pipeline, mixer, etc. using conn.
//	// ...
//	// Wait for output.
//	if !conn.WaitForOutput(1, 5*time.Second) {
//	    t.Fatal("no output received")
//	}
//	frames := conn.CapturedOutput()
package loopback

import (
	"sync"
	"time"

	"github.com/MrWong99/glyphoxa/pkg/audio"
)

// Compile-time interface assertion.
var _ audio.Connection = (*Connection)(nil)

// Participant defines a simulated participant and its pre-loaded audio frames.
type Participant struct {
	// UserID is the platform-specific identifier for this participant.
	UserID string

	// Username is the human-readable display name.
	Username string

	// Frames is the sequence of audio frames this participant will "speak".
	// All frames are loaded into a buffered channel; the channel is closed
	// after the last frame, signalling end of input.
	Frames []audio.AudioFrame
}

// Connection is a test implementation of [audio.Connection] for integration
// testing without a real voice platform. Pre-loaded audio frames are
// streamed as participant input, and all frames written to the output channel
// are captured for post-test assertions.
//
// All exported methods are safe for concurrent use.
type Connection struct {
	mu       sync.RWMutex
	inputs   map[string]<-chan audio.AudioFrame
	output   chan audio.AudioFrame
	callback func(audio.Event)

	capturedMu sync.Mutex
	captured   []audio.AudioFrame
	notify     chan struct{} // non-blocking signal when a frame is captured

	stopCapture chan struct{}
	captureDone chan struct{}
}

// New creates a loopback Connection. Each participant's frames are loaded into
// a buffered channel that is closed after the last frame. A background
// goroutine captures all frames written to the output channel.
func New(participants []Participant) *Connection {
	inputs := make(map[string]<-chan audio.AudioFrame, len(participants))
	for _, p := range participants {
		ch := make(chan audio.AudioFrame, len(p.Frames)+1)
		for _, f := range p.Frames {
			ch <- f
		}
		close(ch)
		inputs[p.UserID] = ch
	}

	c := &Connection{
		inputs:      inputs,
		output:      make(chan audio.AudioFrame, 256),
		notify:      make(chan struct{}, 1),
		stopCapture: make(chan struct{}),
		captureDone: make(chan struct{}),
	}
	go c.runCapture()
	return c
}

// runCapture drains the output channel and stores frames. Runs until
// stopCapture is closed, then drains any remaining buffered frames.
func (c *Connection) runCapture() {
	defer close(c.captureDone)
	for {
		select {
		case <-c.stopCapture:
			// Drain remaining buffered frames.
			for {
				select {
				case frame := <-c.output:
					c.recordFrame(frame)
				default:
					return
				}
			}
		case frame := <-c.output:
			c.recordFrame(frame)
		}
	}
}

// recordFrame appends a frame to the captured list and sends a notification.
func (c *Connection) recordFrame(frame audio.AudioFrame) {
	c.capturedMu.Lock()
	c.captured = append(c.captured, frame)
	c.capturedMu.Unlock()

	// Non-blocking notify.
	select {
	case c.notify <- struct{}{}:
	default:
	}
}

// InputStreams implements [audio.Connection]. Returns a snapshot of per-participant
// input channels.
func (c *Connection) InputStreams() map[string]<-chan audio.AudioFrame {
	c.mu.RLock()
	defer c.mu.RUnlock()
	cp := make(map[string]<-chan audio.AudioFrame, len(c.inputs))
	for k, v := range c.inputs {
		cp[k] = v
	}
	return cp
}

// OutputStream implements [audio.Connection]. Returns the buffered output channel.
func (c *Connection) OutputStream() chan<- audio.AudioFrame {
	return c.output
}

// OnParticipantChange implements [audio.Connection]. Registers the callback.
func (c *Connection) OnParticipantChange(cb func(audio.Event)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.callback = cb
}

// Disconnect implements [audio.Connection]. Stops the capture goroutine and
// waits for it to drain remaining frames. Idempotent.
func (c *Connection) Disconnect() error {
	select {
	case <-c.stopCapture:
		return nil // already disconnected
	default:
	}
	close(c.stopCapture)
	<-c.captureDone
	return nil
}

// EmitEvent fires a participant change event to the registered callback.
// Use this in tests to simulate participants joining or leaving mid-session.
func (c *Connection) EmitEvent(ev audio.Event) {
	c.mu.RLock()
	cb := c.callback
	c.mu.RUnlock()
	if cb != nil {
		cb(ev)
	}
}

// AddParticipant registers a new participant with pre-loaded frames and emits
// an EventJoin. This simulates a participant joining mid-session.
func (c *Connection) AddParticipant(p Participant) {
	ch := make(chan audio.AudioFrame, len(p.Frames)+1)
	for _, f := range p.Frames {
		ch <- f
	}
	close(ch)

	c.mu.Lock()
	c.inputs[p.UserID] = ch
	c.mu.Unlock()

	c.EmitEvent(audio.Event{
		Type:     audio.EventJoin,
		UserID:   p.UserID,
		Username: p.Username,
	})
}

// CapturedOutput returns a copy of all frames captured from the output channel.
// Safe to call while the connection is still active.
func (c *Connection) CapturedOutput() []audio.AudioFrame {
	c.capturedMu.Lock()
	defer c.capturedMu.Unlock()
	cp := make([]audio.AudioFrame, len(c.captured))
	copy(cp, c.captured)
	return cp
}

// CapturedOutputCount returns the number of frames captured so far.
func (c *Connection) CapturedOutputCount() int {
	c.capturedMu.Lock()
	defer c.capturedMu.Unlock()
	return len(c.captured)
}

// WaitForOutput blocks until at least n output frames have been captured,
// or timeout expires. Returns true if the threshold was reached.
func (c *Connection) WaitForOutput(n int, timeout time.Duration) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	for {
		if c.CapturedOutputCount() >= n {
			return true
		}
		select {
		case <-deadline.C:
			return c.CapturedOutputCount() >= n
		case <-c.notify:
		}
	}
}
