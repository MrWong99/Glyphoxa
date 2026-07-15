package wirenpc

import (
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// wireMutes subscribes roster.SetMuted to [voiceevent.MuteChanged] on bus (so a
// GM mute from either surface de-routes / restores the NPC live) and seeds the
// current mute state from mutes, so a mid-session Discord RECONNECT — which
// rebuilds the roster from scratch — re-applies the mutes that were in effect
// (#211). It returns the unsubscribe func; the caller defers it for the cycle's
// lifetime. A nil mutes view still subscribes (a mute can arrive after connect)
// but seeds nothing.
//
// On each event the roster is set from the AUTHORITATIVE view (mutes.Muted), NOT
// the event's payload: the Manager can publish two overlapping ops' events in an
// order that no longer reflects the final set (a mute-all straddling a per-Agent
// unmute), so trusting a stale payload could leave an NPC de-routed while the
// Manager says unmuted. Re-reading the view makes every event converge the roster
// to the current truth — and closes the subscribe-then-seed window too (a mute
// landing between subscribe and seed applies the same current value either way).
func wireMutes(bus *voiceevent.Bus, roster *Roster, mutes orchestrator.MuteView) func() {
	unsub := voiceevent.On(bus, func(e voiceevent.MuteChanged) {
		muted := e.Muted
		if mutes != nil {
			muted = mutes.Muted(e.AgentID) // authoritative re-read; ignore the (possibly stale) payload
		}
		roster.SetMuted(e.AgentID, muted)
	})
	if mutes != nil {
		// Seed via the locked reconcile, NOT a raw range over roster.specs: this runs
		// on connectAndServe's goroutine while the subscription above may fire
		// SetMuted on the publisher's goroutine (a concurrent GM mute), so the whole
		// seed must be under the Roster lock.
		roster.ApplyMutes(mutes.Muted)
	}
	return unsub
}
