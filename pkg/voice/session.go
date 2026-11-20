package voice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/disgoorg/snowflake/v2"
)

// ErrSenderClosed is returned by [Session.Play] once disgo's audio sender has
// shut down and closed this Session's provider: nothing polls it any more, so a
// playback installed there would never finish. The Session is on its way out
// (its inbound stream closes too) and the caller should rebuild it.
var ErrSenderClosed = errors.New("voice: audio sender closed; the session can no longer play")

// sessionLeaveTimeout bounds the LEAVE voice-state update [Session.Close]
// sends through conn.Close. Close holds playMu meanwhile, so a main gateway
// that never accepts the update must not wedge every later Play or Close.
const sessionLeaveTimeout = 5 * time.Second

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

	// closed is set (under playMu) when Close begins; Play refuses afterwards.
	closed atomic.Bool

	// playMu serializes Play callers so the "interrupt current, install next"
	// sequence is atomic with respect to other Play calls, and serializes Play
	// against Close so a Play that passed the closed check can never install a
	// slot into an already-torn-down provider. The provider's slot swap is
	// itself atomic; playMu orders the surrounding bookkeeping.
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
	// A provider closed by disgo's sender means the connection can no longer
	// speak: end the inbound stream so the caller rebuilds (see onClose).
	s.provider.onClose = s.dispatcher.Close

	if err := conn.Open(ctx, channel, cfg.selfMute, cfg.selfDeaf); err != nil {
		return nil, fmt.Errorf("voice: open session for guild %s: %w", guild, err)
	}

	// Install audio handlers only after a successful Open: each spawns a disgo
	// goroutine, and wiring them on a failed connection would leak.
	conn.SetOpusFrameProvider(s.provider)
	conn.SetOpusFrameReceiver(s.dispatcher)
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
// Play returns an error only if the Session is already closed, or disgo's
// audio sender has shut down ([ErrSenderClosed]).
func (s *Session) Play(ctx context.Context, src Source) (*Playback, error) {
	if src == nil {
		return nil, fmt.Errorf("voice: Play requires a non-nil Source")
	}

	s.playMu.Lock()
	defer s.playMu.Unlock()

	if s.closed.Load() {
		return nil, fmt.Errorf("voice: Play on closed session for guild %s", s.guild)
	}

	playCtx, cancel := context.WithCancel(ctx)
	pb := newPlayback(cancel)
	slot := &playSlot{pb: pb, src: src, ctx: playCtx}
	// ctx cancellation is authoritative, not advisory: without this watcher the
	// only path from a cancelled playCtx to a closed Done ran on disgo's sender
	// goroutine (the provider's per-frame ctx.Err check), so a barge-in or
	// conv-stop landing while the sender was DAVE-gated or already reaped never
	// finished the playback — and a PlaybackPump waiting on Done wedged the whole
	// cycle teardown. finish is first-wins, so a clean EOF that already landed
	// keeps its nil error.
	stopWatch := context.AfterFunc(playCtx, func() { pb.finish(ErrInterrupted) })

	// Swap atomically, then interrupt whatever we displaced. The provider may
	// have already retired the previous slot on EOF (clear sets it to nil); in
	// that case swap returns nil and there is nothing to interrupt. Stop closes
	// the displaced playback's Done, which its own accounting goroutine (below)
	// observes — so we must NOT also record PlaybackFinished here, or every
	// interrupted playback would be counted twice.
	if prev := s.provider.swap(slot); prev != nil {
		prev.pb.Stop()
	}
	// disgo's sender closes the provider when it shuts down, and nothing polls
	// a closed provider: a slot installed there would never finish. Checked
	// AFTER the swap because the provider's Close flips closed before it
	// retires the active slot — so either Close's retire sees this slot, or
	// this load sees closed. Never both missed.
	if s.provider.closed.Load() {
		if s.provider.clear(slot) {
			pb.finish(ErrInterrupted)
		}
		stopWatch()
		cancel()
		return nil, fmt.Errorf("%w (guild %s)", ErrSenderClosed, s.guild)
	}
	s.current.Store(pb)
	s.metrics.PlaybackStarted(s.guild.String())

	// Single accounting point: every playback records exactly one Finished when
	// its Done closes, whether by clean EOF, Stop, or interruption.
	go func() {
		<-pb.Done()
		// Release the watcher and the child ctx on every completion path — a
		// clean EOF never cancelled playCtx before, leaking the AfterFunc.
		stopWatch()
		cancel()
		s.metrics.PlaybackFinished(s.guild.String(), pb.Err() != nil)
	}()

	return pb, nil
}

// Inbound returns the buffered channel of inbound [Frame]s. The channel uses a
// drop-oldest policy when full and is closed after [Session.Close] completes.
// v1 supports a single consumer.
func (s *Session) Inbound() <-chan Frame { return s.dispatcher.inbound() }

// Close leaves the voice channel and releases all resources: it interrupts any
// current playback, tears down the connection, and closes the [Session.Inbound]
// channel. It is idempotent and safe to call concurrently with [Session.Play].
func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		// Hold playMu for the whole teardown so Close serializes with Play.
		// Without it, Play could pass its closed check, lose the CPU while
		// Close runs to completion, then install a fresh slot into a provider
		// whose sender goroutine is gone — a playback whose Done never closes,
		// permanently wedging anyone waiting on it (e.g. the wire.PlaybackPump
		// worker, and through it PlaybackPump.Close).
		s.playMu.Lock()
		defer s.playMu.Unlock()

		s.closed.Store(true)

		// Interrupt the active playback before tearing the connection down so
		// its Done closes deterministically rather than waiting on disgo's
		// sender to notice the closed transport.
		if pb := s.current.Load(); pb != nil {
			pb.Stop()
		}

		// conn.Close only tears down the gateway/UDP; it does not call our
		// provider/receiver Close — nor its own AudioSender's (issue #579). Do
		// ours ourselves so Inbound closes and any straggler playback is
		// retired, regardless of disgo's goroutine timing. The ORDER matters:
		// conn.Close's last step discards the conn, which TERMINATES the
		// transport (connguard.go) — a plain udp.Close is only a pause.
		// Closing the provider after that flips it into its sender-reaping
		// state (see switchingProvider.Close): the next 20ms poll hands disgo's
		// sender a silence frame, the write on the terminated transport fails
		// with net.ErrClosed, and the sender goroutine exits instead of polling
		// forever. The receiver needs no such poke: terminate unblocks its
		// ReadPacket with net.ErrClosed and it closes itself.
		// Bounded: conn.Close sends the LEAVE under this ctx while playMu is held.
		leaveCtx, cancelLeave := context.WithTimeout(context.Background(), sessionLeaveTimeout)
		s.conn.Close(leaveCtx)
		cancelLeave()
		s.provider.Close()
		s.dispatcher.Close()
		// Sessions gauge -1 (A2), inside closeOnce so it pairs exactly once with
		// the SessionOpened in newSession.
		s.metrics.SessionClosed(s.guild.String())
		s.logger.Debug("voice session closed")
	})
	return nil
}
