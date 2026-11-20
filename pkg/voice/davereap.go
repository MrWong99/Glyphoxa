package voice

import (
	"log/slog"
	"sync/atomic"

	"github.com/disgoorg/godave"
)

// frameHolder is the optional surface a DAVE session can expose to distinguish
// "E2EE handshake in progress, hold frames" from "this channel will never have
// E2EE" (DAVE protocol version 0, transport-only). dave-go's *session.Session
// implements it; godave.Session does not carry it, so it is asserted dynamically.
type frameHolder interface {
	ShouldHoldFrames() bool
}

// reapableDaveSession wraps the real DAVE session so disgo's audio sender and
// receiver goroutines can always terminate (#586).
//
// disgo gates BOTH per-session audio goroutines on DAVE().Ready():
//
//	audio_receiver.go: if !s.conn.DAVE().Ready() { sleep 20ms; return }  // before ReadPacket
//	audio_sender.go:   if !s.conn.DAVE().Ready() { return }               // before ProvideOpusFrame
//
// and every teardown path we have relies on code BEHIND that gate: the receiver
// exits when ReadPacket returns net.ErrClosed off the closed UDP conn, and the
// sender exits when the switchingProvider's post-Close silence poke (#579/#581)
// makes it write to the dead transport. While Ready() is false neither call is
// ever reached, so neither goroutine can ever observe the teardown: both keep
// waking every 20 ms (the receiver's not-ready sleep, the sender's frame
// pacing) for the life of the process. Before disgo v0.20.0 the receiver had
// no sleep there and this was a busy-wait burning a full core per session
// (#586's 2-core spin); v0.20.0 turned it into a leak, but a leak of two
// goroutines per ended session is still unbounded on a long-lived Voice
// Instance, so the wrapper stays.
//
// dave-go's Ready() is false during the whole MLS handshake window and stays
// false FOREVER on a protocol-version-0 channel (its docs point senders at
// ShouldHoldFrames for that distinction), so a Voice Session that ends before
// its first E2EE epoch activates — a short session, or any session on a
// non-E2EE channel — strands both goroutines past session end. On a v0
// channel it is worse than a leak while the session is live: disgo never reads
// or sends a frame, so the NPC is deaf and mute.
//
// The wrapper restores the godave.Session contract disgo's gate is written
// against ("sessions that never establish E2EE always return true") and adds
// the teardown poke:
//
//   - after Close, Ready() reports true, so both goroutines fall through to
//     their terminal I/O (ReadPacket → net.ErrClosed; provider poke → dead
//     transport write) and reap themselves;
//   - while open, a channel that will never have E2EE (ShouldHoldFrames false
//     with Ready false ⇒ protocol version 0) also reports true, so audio flows
//     in transport-only passthrough instead of waiting on an epoch that will
//     never come.
//
// A transient handshake window (protocol version ≥ 1, epoch pending) still
// reports false — frames must be held there — so both goroutines idle at the
// gate for the duration of a live handshake. That wait is bounded by the
// handshake; the unbounded, session-outliving one is what this wrapper removes.
type reapableDaveSession struct {
	godave.Session
	closed atomic.Bool
}

// WrapDaveSessionCreate wraps a DAVE session factory so every session it
// creates is a [reapableDaveSession]. The dave-tagged [DaveOption] applies it
// to dave-go's CreateFunc; it lives untagged here so the reap semantics are
// unit-testable in the default build.
func WrapDaveSessionCreate(create godave.SessionCreateFunc) godave.SessionCreateFunc {
	return func(logger *slog.Logger, userID godave.UserID, callbacks godave.Callbacks) godave.Session {
		return &reapableDaveSession{Session: create(logger, userID, callbacks)}
	}
}

// Ready implements godave.Session. See the type comment for the three-way
// answer: closed sessions and never-E2EE channels report true so disgo's audio
// goroutines reach their terminal I/O; only a live pending handshake holds.
func (s *reapableDaveSession) Ready() bool {
	if s.closed.Load() {
		return true
	}
	if s.Session.Ready() {
		return true
	}
	if h, ok := s.Session.(frameHolder); ok {
		return !h.ShouldHoldFrames()
	}
	return false
}

// Close implements godave.Session: flip into the reaping state (Ready reports
// true from now on) before delegating, so the audio goroutines' very next poll
// falls through to the dead transport and exits.
func (s *reapableDaveSession) Close() error {
	s.closed.Store(true)
	return s.Session.Close()
}
