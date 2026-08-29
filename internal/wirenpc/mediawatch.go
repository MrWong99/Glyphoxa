// Media-path liveness watchdog (#633). Discord's voice server can stop
// forwarding inbound RTP while every socket stays open and the voice websocket
// stays healthy — historically after ~13-15 minutes of an outbound-silent Bot
// (no UDP keepalive; fixed by pkg/voice's TransportOption). When that happens
// nothing in the stack can error: disgo's receiver is parked in a deadline-less
// Read, so the session sits deaf until Idle Close reaps it with a reason that
// blames the table. This watchdog is the receive-side detection and recovery:
// it reads transport-level liveness each tick and, when remote participants
// keep announcing speech while no RTP arrives for the stall window, it ends the
// cycle so runWithReconnect rebuilds the whole voice connection on its existing
// backoff. It also owns the once-a-minute liveness log line, so a media
// blackout can never again be a log blackout.

package wirenpc

import (
	"context"
	"errors"
	"log/slog"
	"time"

	gxvoice "github.com/MrWong99/Glyphoxa/pkg/voice"
)

// errMediaStall is the ONE sentinel for the watchdog's verdict, returned by
// connectAndServe when the watchdog cancelled its cycle. classifyFatal does not
// know it, so runWithReconnect treats it as transient and rebuilds — exactly
// the intent. (One sentinel across the whole seam: connectAndServe compares
// context.Cause against this very value, never a second errors.New.)
var errMediaStall = errors.New("wirenpc: inbound media path stalled: remote speaking observed but no RTP arrived; rebuilding the voice connection")

const (
	// defaultMediaStallWindow is how long inbound RTP may be absent WHILE remote
	// speaking announcements keep arriving before the path is declared dead.
	// The incident's asks bound it to 30-60s: long enough that a mid-utterance
	// jitter gap can never trip it, short enough to cut the deaf window from
	// ~15 minutes to under one. Config.MediaStallWindow overrides it.
	defaultMediaStallWindow = 45 * time.Second
	// mediaWatchTick is the check cadence. Liveness recency is derived at this
	// resolution (the packet counter is diffed per tick, the idleclose marks
	// discipline), so it must stay well under the stall window.
	mediaWatchTick = 10 * time.Second
	// mediaLivenessLogEvery is the cadence of the INFO liveness line. Info, not
	// Debug: the prod log floor is Info, and 13.5 silent minutes on an open
	// session was itself one of #633's findings.
	mediaLivenessLogEvery = 60 * time.Second
)

// mediaWatchdog watches one connect-and-serve cycle's inbound media path. Its
// goroutine is bound to cycleCtx like every other per-cycle worker; now/ticks
// are the injected test seams (the idleclose discipline).
type mediaWatchdog struct {
	// liveness snapshots the Session's transport counters; ok=false (no
	// transport monitor: fakes, or a client built without TransportOption)
	// keeps the watchdog inert — no signal is not the same as no traffic.
	liveness func() (gxvoice.MediaLiveness, bool)
	// cancel ends the cycle with errMediaStall as the cause.
	cancel context.CancelCauseFunc
	// suspect is Config.MediaSuspect: it flags the session's Idle Close handle
	// so a close that lands anyway reports media_path_dead, not idle_no_audio.
	// May be nil (feature off).
	suspect func()
	metrics gxvoice.MetricsRecorder
	log     *slog.Logger
	guild   string
	// window <= 0 disables the stall verdict; the liveness log still runs.
	window time.Duration

	now   func() time.Time
	ticks <-chan time.Time

	lastPackets uint64
	lastMove    time.Time // when the packet counter last moved (start until then)
	everMoved   bool

	lastLog       time.Time
	logPackets    uint64
	logKeepalives uint64
	fired         bool
}

func newMediaWatchdog(liveness func() (gxvoice.MediaLiveness, bool), cancel context.CancelCauseFunc, suspect func(), metrics gxvoice.MetricsRecorder, log *slog.Logger, guild string, window time.Duration) *mediaWatchdog {
	return &mediaWatchdog{
		liveness: liveness,
		cancel:   cancel,
		suspect:  suspect,
		metrics:  metrics,
		log:      log,
		guild:    guild,
		window:   window,
		now:      time.Now,
	}
}

// run ticks until the cycle ends or the watchdog fires. It fires at most once:
// after cancelling the cycle there is nothing left to watch, and the next
// cycle starts a fresh watchdog.
func (w *mediaWatchdog) run(ctx context.Context) {
	start := w.now()
	w.lastMove = start
	w.lastLog = start
	ticks := w.ticks
	if ticks == nil {
		t := time.NewTicker(mediaWatchTick)
		defer t.Stop()
		ticks = t.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			w.check(w.now())
			if w.fired {
				return
			}
		}
	}
}

// check is one watchdog pass. Split from run so tests drive it with a
// hand-advanced clock, the idleclose sweep pattern.
func (w *mediaWatchdog) check(now time.Time) {
	ml, ok := w.liveness()
	if !ok {
		return
	}

	// A moved packet counter is the only evidence of inbound media. A RESET
	// counter (the transport re-opened onto a fresh socket) also lands here and
	// restarts the grace window — a rebuilt path earns a fresh verdict.
	if ml.Packets != w.lastPackets {
		w.lastPackets = ml.Packets
		w.lastMove = now
		w.everMoved = ml.Packets > 0
	}

	if now.Sub(w.lastLog) >= mediaLivenessLogEvery {
		w.log.Info("voice media liveness",
			"guild", w.guild,
			"packets_delta", counterDelta(ml.Packets, w.logPackets),
			"keepalives_delta", counterDelta(ml.Keepalives, w.logKeepalives),
			"last_packet_ago", agoOrNever(now, w.lastMove, w.everMoved),
			"speaking_ago", agoOrNever(now, ml.LastSpeaking, !ml.LastSpeaking.IsZero()),
		)
		w.lastLog = now
		w.logPackets = ml.Packets
		w.logKeepalives = ml.Keepalives
	}

	if w.window <= 0 {
		return
	}
	window := w.window
	if !w.everMoved {
		// The receiver reads nothing while the DAVE/MLS handshake runs, so a
		// young connection legitimately shows zero packets. A path that never
		// carried a single packet gets double the window before a verdict.
		window *= 2
	}
	stall := now.Sub(w.lastMove)
	if stall < window {
		return
	}
	// The discriminator against a genuinely quiet table: someone announced
	// speech on the (still healthy) voice websocket AFTER the last time audio
	// moved. A silent room produces no Speaking events, so it can never trip
	// this — Idle Close remains its only policy.
	if ml.LastSpeaking.IsZero() || !ml.LastSpeaking.After(w.lastMove) {
		return
	}

	w.fired = true
	w.log.Warn("inbound media path stalled; ending cycle to rebuild the voice connection",
		"guild", w.guild, "stall", stall, "speaking_ago", now.Sub(ml.LastSpeaking),
		"packets", ml.Packets, "keepalives", ml.Keepalives, "window", window)
	if w.metrics != nil {
		w.metrics.MediaStallRebuild(w.guild)
	}
	if w.suspect != nil {
		w.suspect()
	}
	w.cancel(errMediaStall)
}

// counterDelta is cur-prev, tolerating a counter reset (transport re-open) by
// reporting the post-reset count instead of an underflowed value.
func counterDelta(cur, prev uint64) uint64 {
	if cur < prev {
		return cur
	}
	return cur - prev
}

// agoOrNever renders "time since t" for a log attr, or "never" when the event
// has not happened (slog renders either fine; the string keeps the line honest
// instead of showing a since-boot duration).
func agoOrNever(now, t time.Time, happened bool) any {
	if !happened {
		return "never"
	}
	return now.Sub(t)
}
