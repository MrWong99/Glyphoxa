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
//
// Both of those tiers read counters that only move while disgo's receiver
// reads, and it does not read while the DAVE session is not ready (#688).
// While that stands the two tiers are skipped rather than left to blame the
// socket for a gate. Not ready has two meanings, though. With another member
// in the channel it is an MLS handshake, and one that outlasts the window is
// its own verdict, tier 0 "dave_not_ready". With the Bot ALONE in an E2EE
// channel it is no handshake at all: Discord establishes the DAVE group only
// once a second member is present, so a Voice Session started in an empty
// channel is legitimately not ready until someone joins. That is a quiet
// table — the watchdog stays silent and Idle Close remains its only policy.

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
var errMediaStall = errors.New("wirenpc: inbound media path stalled; rebuilding the voice connection")

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
	// echoArmCount is how many keepalive echoes the current socket must have
	// carried before their cessation counts as a verdict. The echo tier is
	// self-calibrating: a server generation that never echoes never reaches
	// this count, so echo absence alone is never read as death.
	echoArmCount = 3
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
	// lastAny/lastAnyMove track packets+echoes combined: on an echo-armed
	// socket, NOTHING arriving — not even our keepalives reflected back — for
	// the stall window is a dead socket regardless of anyone speaking.
	lastAny     uint64
	lastAnyMove time.Time
	// notReadySince is when the current DAVE-not-ready spell was first seen
	// (zero while ready); readySince is when the last such spell ended. The
	// receiver reads nothing during a spell, so tiers 1 and 2 measure their
	// stall from readySince when it is later than their own last move.
	notReadySince time.Time
	readySince    time.Time
	everReady     bool
	// handshakeSince is since when the current spell has had evidence of
	// another member in the channel — i.e. since when it is a handshake that
	// ought to complete rather than the Bot waiting alone. Zero while ready,
	// and while not ready with nobody there. It is tier 0's clock.
	handshakeSince time.Time

	lastLog       time.Time
	logPackets    uint64
	logEchoes     uint64
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
	w.lastAnyMove = start
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
	if any := ml.Packets + ml.Echoes; any != w.lastAny {
		w.lastAny = any
		w.lastAnyMove = now
	}
	if ml.DaveReady {
		w.everReady = true
		if !w.notReadySince.IsZero() {
			w.readySince = now
			w.notReadySince = time.Time{}
		}
		w.handshakeSince = time.Time{}
	} else {
		if w.notReadySince.IsZero() {
			w.notReadySince = now
		}
		// Someone else is there when the voice gateway says so, or — belt and
		// braces, should a clients_connect ever go missing — when a remote
		// participant announced speech during this spell (Speaking travels on
		// the websocket, not through the gated receiver).
		switch peer := ml.DavePeers > 0 || ml.LastSpeaking.After(w.notReadySince); {
		case !peer:
			w.handshakeSince = time.Time{}
		case w.handshakeSince.IsZero():
			w.handshakeSince = now
		}
	}

	if now.Sub(w.lastLog) >= mediaLivenessLogEvery {
		w.log.Info("voice media liveness",
			"guild", w.guild,
			"packets_delta", counterDelta(ml.Packets, w.logPackets),
			"echoes_delta", counterDelta(ml.Echoes, w.logEchoes),
			"keepalives_delta", counterDelta(ml.Keepalives, w.logKeepalives),
			"last_packet_ago", agoOrNever(now, w.lastMove, w.everMoved),
			"speaking_ago", agoOrNever(now, ml.LastSpeaking, !ml.LastSpeaking.IsZero()),
			"dave_ready", ml.DaveReady,
			"dave_peers", ml.DavePeers,
		)
		w.lastLog = now
		w.logPackets = ml.Packets
		w.logEchoes = ml.Echoes
		w.logKeepalives = ml.Keepalives
	}

	if w.window <= 0 {
		return
	}

	// Verdict tier 0 — DAVE not ready. disgo's receiver reads nothing off the
	// socket while the DAVE session is not ready, so packets and echoes are
	// frozen by the gate, not by the path: the tiers below cannot judge it and
	// are skipped. Whether the gate itself is a fault depends on company:
	//
	//   - Alone (no peer announced, no remote Speaking this spell) there is no
	//     group to establish and nothing to wait for. A Bot that joined an
	//     empty channel, or was left alone across a reconnect, sits here until
	//     someone arrives; rebuilding would only rejoin the same empty channel.
	//     No verdict, however long it lasts.
	//   - With someone there it is a handshake, and one still pending a full
	//     window after that member was first seen is the verdict, named for
	//     what it is. A connection that has never been ready gets double, the
	//     never-moved rule below applied to a first handshake. The clock starts
	//     at the peer, not at the spell: a player joining a Bot that waited
	//     alone for ten minutes gets a whole window for the group to form.
	//
	// Two accepted limits. A handshake that flaps (not ready, ready for a tick,
	// not ready again) restarts this clock each time and moves readySince too,
	// so no tier convicts it — but each ready tick is a receiver that read, and
	// a path that then stays silent is tiers 1 and 2's to judge. And the peer
	// count can run stale after an in-place gateway reconnect, which costs at
	// most one needless rebuild: the rebuilt connection counts afresh.
	if !ml.DaveReady {
		if w.handshakeSince.IsZero() {
			return
		}
		window := w.window
		if !w.everReady {
			window *= 2
		}
		if pending := now.Sub(w.handshakeSince); pending >= window {
			w.fire(now, ml, "dave_not_ready", pending, window)
		}
		return
	}

	// Verdict tier 1 — echo cessation. Once the socket has proven the voice
	// server echoes our keepalives (echoArmCount on THIS socket), a full stall
	// window with NOTHING inbound — no RTP, no echoes, while keepalives go out
	// every 5s — is a dead socket, whether or not anyone is speaking. This is
	// the tier that does not depend on Discord relaying per-utterance Speaking
	// events, and a quiet table cannot trip it: quiet only stops RTP, never the
	// echoes. Time spent gated behind a not-ready DAVE session is not stall:
	// the clock starts no earlier than the moment the receiver was reading
	// again.
	if anyBase := laterOf(w.lastAnyMove, w.readySince); ml.Echoes >= echoArmCount && now.Sub(anyBase) >= w.window {
		w.fire(now, ml, "echo_cessation", now.Sub(anyBase), w.window)
		return
	}

	// Verdict tier 2 — speaking evidence. No RTP for the (possibly doubled)
	// window while a remote participant announced ACTIVE speech on the still
	// healthy voice websocket, comfortably after the last time audio moved
	// (one full tick of slack: lastMove is stamped at tick resolution, so a
	// stop-start announcement racing a tick boundary must not convict). A
	// silent room produces no Speaking events and can never trip this — Idle
	// Close remains its only policy.
	window := w.window
	if !w.everMoved {
		// A young connection legitimately shows zero packets (nobody has
		// spoken yet, or the receiver only just came out of the DAVE gate). A
		// path that never carried a single packet gets double the window
		// before a verdict.
		window *= 2
	}
	base := laterOf(w.lastMove, w.readySince)
	stall := now.Sub(base)
	if stall < window {
		return
	}
	if ml.LastSpeaking.IsZero() || !ml.LastSpeaking.After(base.Add(mediaWatchTick)) {
		return
	}
	w.fire(now, ml, "speaking_without_audio", stall, window)
}

// fire records the verdict exactly once and ends the cycle with errMediaStall.
func (w *mediaWatchdog) fire(now time.Time, ml gxvoice.MediaLiveness, evidence string, stall, window time.Duration) {
	w.fired = true
	w.log.Warn("inbound media path stalled; ending cycle to rebuild the voice connection",
		"guild", w.guild, "evidence", evidence, "stall", stall, "window", window,
		"speaking_ago", agoOrNever(now, ml.LastSpeaking, !ml.LastSpeaking.IsZero()),
		"packets", ml.Packets, "echoes", ml.Echoes, "keepalives", ml.Keepalives,
		"dave_ready", ml.DaveReady, "dave_peers", ml.DavePeers)
	if w.metrics != nil {
		w.metrics.MediaStallRebuild(w.guild)
	}
	if w.suspect != nil {
		w.suspect()
	}
	w.cancel(errMediaStall)
}

// laterOf returns the later of two instants.
func laterOf(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
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
