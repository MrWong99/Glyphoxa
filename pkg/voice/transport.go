package voice

import (
	"log/slog"
	"sync"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave"
)

// TransportOption returns the [bot.ConfigOpt] that hardens the Discord voice
// media transport: it swaps disgo's stock UDP connection for this package's
// [keepaliveUDPConn] (a keepalive datagram every ~5s for the whole connection
// lifetime — without it Discord's voice server silently stops routing inbound
// RTP to a non-speaking Bot after ~13–15 minutes) and registers the
// voice-gateway event observer that timestamps remote Speaking announcements
// (the media watchdog's "audio should be flowing" evidence).
//
// Pass it to disgo.New alongside [DaveOption] at EVERY client build site — the
// per-cycle wirenpc client and the standing shared presence client both open
// voice connections, and a site without this option runs the stock,
// keepalive-less transport. Unlike DaveOption it is build-tag-free and must be
// applied unconditionally.
//
// A nil logger discards transport logs; a nil rec discards the keepalive
// counters (glyphoxa_voice_udp_keepalives_total and friends).
func TransportOption(logger *slog.Logger, rec MetricsRecorder) bot.ConfigOpt {
	if logger == nil {
		logger = discardLogger()
	}
	if rec == nil {
		rec = discardMetrics{}
	}
	return bot.WithVoiceManagerConfigOpts(voice.WithConnConfigOpts(
		// The opts disgo forwards configure only the stock impl's logger/dialer
		// (an unexported config we cannot apply); nothing in this repo sets them.
		voice.WithUDPConnCreateFunc(func(daveSession godave.Session, ssrcLookup voice.SsrcLookupFunc, _ ...voice.UDPConnConfigOpt) voice.UDPConn {
			return newKeepaliveUDPConn(daveSession, ssrcLookup, logger, rec)
		}),
		voice.WithConnEventHandlerFunc(noteSpeakingEvent),
	))
}

// MediaLiveness is a point-in-time view of one Session's inbound media
// transport, for the wirenpc media watchdog and its liveness log. Packets is a
// COUNTER, not a timestamp: the reader derives recency by diffing it on its
// own tick (the idleclose marks discipline), so the packet hot path reads no
// clock.
type MediaLiveness struct {
	// Packets counts parsed inbound RTP voice packets on the current socket,
	// stamped before decryption — an undecryptable packet still proves the
	// media path is alive. Reset to zero when the connection re-opens.
	Packets uint64
	// Keepalives counts keepalive datagrams written since the connection was
	// first opened.
	Keepalives uint64
	// LastSpeaking is when a REMOTE participant last announced speaking on the
	// voice gateway (opcode 5), zero if never. The Bot's own speaking echo is
	// excluded — otherwise an NPC talking into a quiet room would look like
	// evidence that inbound audio should be flowing.
	LastSpeaking time.Time
}

// MediaLiveness reports transport-level liveness for this Session's voice
// connection. ok is false when the transport monitor is not installed — a
// client built without [TransportOption], or the test fakes — and callers must
// treat that as "no signal", not "no traffic".
func (s *Session) MediaLiveness() (MediaLiveness, bool) {
	// The voiceConn seam deliberately hides the transport; the real disgo conn
	// carries it. A narrow assertion here keeps the seam (and every fake) as-is.
	tc, ok := s.conn.(interface {
		UDP() voice.UDPConn
		Gateway() voice.Gateway
	})
	if !ok {
		return MediaLiveness{}, false
	}
	ku, ok := tc.UDP().(*keepaliveUDPConn)
	if !ok {
		return MediaLiveness{}, false
	}
	return MediaLiveness{
		Packets:      ku.packets.Load(),
		Keepalives:   ku.keepalives.Load(),
		LastSpeaking: speakingEvents.last(tc.Gateway()),
	}, true
}

// speakingEvents timestamps, per voice gateway, the last REMOTE Speaking
// announcement. It is package-level because disgo's event handler is
// registered per client (shared across every guild conn the client hosts)
// while the readers are per-Session; the gateway instance — one per voice
// connection — is the correlation key both sides can reach. Entries are pruned
// by age on write: a gateway is unreachable once its conn is discarded, and a
// long-lived shared client would otherwise accumulate one stale entry per
// session ever hosted.
var speakingEvents = &speakingLog{entries: map[voice.Gateway]time.Time{}}

// speakingStaleAfter is how long a gateway's entry survives without a new
// Speaking event before pruning may drop it. Comfortably above any watchdog
// window so pruning can never erase live evidence.
const speakingStaleAfter = time.Hour

type speakingLog struct {
	mu      sync.Mutex
	entries map[voice.Gateway]time.Time
}

func (l *speakingLog) note(gw voice.Gateway, at time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries[gw] = at
	if len(l.entries) > 64 {
		for k, v := range l.entries {
			if at.Sub(v) > speakingStaleAfter {
				delete(l.entries, k)
			}
		}
	}
}

func (l *speakingLog) last(gw voice.Gateway) time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.entries[gw]
}

// noteSpeakingEvent observes every voice-gateway message (disgo invokes it
// after its own handling, synchronously on the websocket read goroutine — it
// must stay non-blocking) and timestamps remote Speaking announcements. During
// the silent-media failure mode the websocket stays healthy, so these events
// keep arriving while RTP does not — the exact discriminator between a dead
// media path and a genuinely quiet table.
func noteSpeakingEvent(gw voice.Gateway, op voice.Opcode, _ int, data voice.GatewayMessageData) {
	if op != voice.OpcodeSpeaking {
		return
	}
	d, ok := data.(voice.GatewayMessageDataSpeaking)
	if !ok {
		return
	}
	if d.SSRC == gw.SSRC() {
		return // our own speaking relayed back is not remote evidence
	}
	speakingEvents.note(gw, time.Now())
}
