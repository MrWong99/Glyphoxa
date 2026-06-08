package voice

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/disgoorg/snowflake/v2"
)

// State is the lifecycle phase of a [Session].
type State uint8

const (
	// Connecting is the initial state while the voice connection opens.
	Connecting State = iota
	// Ready means the connection is open and audio can flow both ways.
	Ready
	// Reconnecting is reserved for v1.1 auto-reconnect; never set in v1.
	Reconnecting
	// Closed is terminal: the connection is gone and Inbound is closed.
	Closed
)

// String renders the State for logs and the [fmt.Stringer] contract.
func (s State) String() string {
	switch s {
	case Connecting:
		return "connecting"
	case Ready:
		return "ready"
	case Reconnecting:
		return "reconnecting"
	case Closed:
		return "closed"
	default:
		return fmt.Sprintf("State(%d)", uint8(s))
	}
}

// Session is the Bot's presence in one Guild's voice channel: an outbound
// playback slot and a buffered inbound stream of per-speaker [Frame]s. Obtain
// one from [Manager.Open]. All methods are safe for concurrent use.
type Session struct {
	guild   snowflake.ID
	conn    voiceConn
	logger  *slog.Logger
	metrics MetricsRecorder

	provider   *switchingProvider
	dispatcher *inboundDispatcher

	state atomic.Uint32 // State

	// playMu serializes Play callers so the "interrupt current, install next"
	// sequence is atomic with respect to other Play calls. The provider's slot
	// swap is itself atomic; playMu only orders the surrounding bookkeeping.
	playMu  sync.Mutex
	current atomic.Pointer[Playback]

	closeOnce sync.Once
}

// newSession wires conn with a fresh provider/dispatcher and opens the
// connection. It is the internal constructor [Manager.Open] calls; tests
// exercise it directly with a fake voiceConn.
func newSession(ctx context.Context, guild snowflake.ID, channel snowflake.ID, conn voiceConn, cfg sessionConfig) (*Session, error) {
	logger := cfg.logger.With(slog.String("guild", guild.String()))
	s := &Session{
		guild:      guild,
		conn:       conn,
		logger:     logger,
		metrics:    cfg.metrics,
		provider:   &switchingProvider{},
		dispatcher: newInboundDispatcher(guild.String(), cfg.inboundBuffer, cfg.metrics),
	}
	s.state.Store(uint32(Connecting))

	if err := conn.Open(ctx, channel, cfg.selfMute, cfg.selfDeaf); err != nil {
		return nil, fmt.Errorf("voice: open session for guild %s: %w", guild, err)
	}

	// Install audio handlers only after a successful Open: each spawns a disgo
	// goroutine, and wiring them on a failed connection would leak.
	conn.SetOpusFrameProvider(s.provider)
	conn.SetOpusFrameReceiver(s.dispatcher)
	s.state.Store(uint32(Ready))
	// The session is live: bump the sessions gauge (A2). Paired with the
	// SessionClosed in Close, which the closeOnce guards to fire exactly once.
	s.metrics.SessionOpened(guild.String())
	logger.Debug("voice session ready")
	return s, nil
}

// Play streams src to the channel, interrupting and replacing any current
// playback. It returns immediately with a [Playback] handle; the audio flows on
// disgo's sender goroutine. The returned Playback's Done closes when src is
// exhausted, it is Stopped, or a later Play interrupts it.
//
// ctx scopes this playback: cancelling it ends the stream as [ErrInterrupted].
// Play returns an error only if the Session is already Closed.
func (s *Session) Play(ctx context.Context, src Source) (*Playback, error) {
	if src == nil {
		return nil, fmt.Errorf("voice: Play requires a non-nil Source")
	}

	s.playMu.Lock()
	defer s.playMu.Unlock()

	if State(s.state.Load()) == Closed {
		return nil, fmt.Errorf("voice: Play on closed session for guild %s", s.guild)
	}

	playCtx, cancel := context.WithCancel(ctx)
	pb := newPlayback(cancel)
	slot := &playSlot{pb: pb, src: src, ctx: playCtx}

	// Swap atomically, then interrupt whatever we displaced. The provider may
	// have already retired the previous slot on EOF (clear sets it to nil); in
	// that case swap returns nil and there is nothing to interrupt. Stop closes
	// the displaced playback's Done, which its own accounting goroutine (below)
	// observes — so we must NOT also record PlaybackFinished here, or every
	// interrupted playback would be counted twice.
	if prev := s.provider.swap(slot); prev != nil {
		prev.pb.Stop()
	}
	s.current.Store(pb)
	s.metrics.PlaybackStarted(s.guild.String())

	// Single accounting point: every playback records exactly one Finished when
	// its Done closes, whether by clean EOF, Stop, or interruption.
	go func() {
		<-pb.Done()
		s.metrics.PlaybackFinished(s.guild.String(), pb.Err() != nil)
	}()

	return pb, nil
}

// Inbound returns the buffered channel of inbound [Frame]s. The channel uses a
// drop-oldest policy when full and is closed after [Session.Close] completes.
// v1 supports a single consumer.
func (s *Session) Inbound() <-chan Frame { return s.dispatcher.inbound() }

// State returns the Session's current lifecycle [State].
func (s *Session) State() State { return State(s.state.Load()) }

// Close leaves the voice channel and releases all resources: it interrupts any
// current playback, tears down the connection, and closes the [Session.Inbound]
// channel. It is idempotent and safe to call concurrently with [Session.Play].
func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		s.state.Store(uint32(Closed))

		// Interrupt the active playback before tearing the connection down so
		// its Done closes deterministically rather than waiting on disgo's
		// sender to notice the closed transport.
		if pb := s.current.Load(); pb != nil {
			pb.Stop()
		}

		// conn.Close only tears down the gateway/UDP; it does not call our
		// provider/receiver Close. Do that ourselves so Inbound closes and any
		// straggler playback is retired, regardless of disgo's goroutine timing.
		s.conn.Close(context.WithoutCancel(context.Background()))
		s.provider.Close()
		s.dispatcher.Close()
		// Sessions gauge -1 (A2), inside closeOnce so it pairs exactly once with
		// the SessionOpened in newSession.
		s.metrics.SessionClosed(s.guild.String())
		s.logger.Debug("voice session closed")
	})
	return nil
}
