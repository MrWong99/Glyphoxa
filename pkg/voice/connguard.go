package voice

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
)

// errConnDiscarded is what a discarded conn's voice-state updates are refused
// with (see [guardedConnCreate]).
var errConnDiscarded = errors.New("voice: conn was discarded; voice-state update refused")

// guardedConnCreate is the [voice.ConnCreateFunc] [TransportOption] installs.
// It builds disgo's stock conn but stands between that conn and the two
// functions the voice manager hands it, because disgo gives no other signal
// for "this conn is finished":
//
//   - removeConnFunc is called by exactly one thing, the last step of the
//     conn's Close — whether [Session.Close] asked for it or disgo gave up on
//     its own recovery and closed the conn itself. The guard uses it as the
//     discard signal: it terminates the transport ([keepaliveUDPConn.terminate]),
//     so the audio goroutines see net.ErrClosed and reap, the Session's
//     inbound stream closes, and the wirenpc cycle rebuilds. Without this a
//     conn disgo closed behind our back would leave a paused transport and a
//     Session waiting forever for a re-Open that is never coming.
//
//   - voiceStateUpdateFunc is how a conn joins and leaves on the main gateway.
//     Once discarded, the conn is fenced off from it. disgo's recovery runs on
//     its own goroutine with a 30s timeout and ENDS in a Close — a LEAVE for
//     the whole guild. If the Session was closed meanwhile (the operator
//     stopped it, or the cycle was rebuilt) that late LEAVE would land on
//     whichever conn serves the guild by then and kick the Bot out of a
//     session that is working. A guild has one voice state; a conn that no
//     longer owns it must not be able to change it.
//
// The Close that discards the conn sends its own LEAVE first — the fence only
// drops into place at that Close's final step — so a normal teardown is
// unchanged.
func guardedConnCreate(guildID, userID snowflake.ID, voiceStateUpdateFunc voice.StateUpdateFunc, removeConnFunc func(), opts ...voice.ConnConfigOpt) voice.Conn {
	g := &connGuard{}
	conn := voice.NewConn(guildID, userID, g.fence(voiceStateUpdateFunc), g.discard(removeConnFunc), opts...)
	// The transport only exists once NewConn has run its UDPConnCreateFunc;
	// discard cannot fire before NewConn returns the conn to anyone.
	if ku, ok := conn.UDP().(*keepaliveUDPConn); ok {
		g.transport.Store(ku)
	}
	// The gateway is fixed for the conn's life; remember it so discard can drop
	// its entry from the process-global speakingEvents.
	g.gateway.Store(&gatewayRef{conn.Gateway()})
	return conn
}

// connGuard is the per-conn state behind [guardedConnCreate].
type connGuard struct {
	discarded atomic.Bool
	transport atomic.Pointer[keepaliveUDPConn]
	gateway   atomic.Pointer[gatewayRef]
}

// gatewayRef boxes the conn's voice gateway (an interface) for atomic storage.
type gatewayRef struct{ gw voice.Gateway }

// fence wraps the conn's voice-state update func: it passes through until the
// conn is discarded and refuses afterwards.
func (g *connGuard) fence(update voice.StateUpdateFunc) voice.StateUpdateFunc {
	return func(ctx context.Context, guildID snowflake.ID, channelID *snowflake.ID, selfMute, selfDeaf bool) error {
		if g.discarded.Load() {
			return errConnDiscarded
		}
		return update(ctx, guildID, channelID, selfMute, selfDeaf)
	}
}

// discard wraps the conn's remove func: the conn is being closed for good.
func (g *connGuard) discard(remove func()) func() {
	return func() {
		g.discarded.Store(true)
		if ku := g.transport.Load(); ku != nil {
			ku.terminate()
		}
		// The gateway is unreachable from here on: forget its Speaking
		// timestamp now instead of leaving it to the age-based prune.
		if ref := g.gateway.Load(); ref != nil {
			speakingEvents.forget(ref.gw)
		}
		remove()
	}
}
