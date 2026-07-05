package orchestrator

import (
	"context"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// MuteView is the live authoritative read of which Agents are muted in the
// current Voice Session (#211). The session Manager satisfies it. The
// orchestrator keeps NO duplicated mute set of its own — the [Replier] gate and
// the reload/route paths always ask this view, so the set the Manager wrote
// before publishing [voiceevent.MuteChanged] is the single source of truth and
// the check is airtight against the publish/read race (the set is written first).
type MuteView interface {
	// Muted reports whether the Agent with agentID is currently muted.
	Muted(agentID string) bool
}

// WithMute wires the live mute view into the conversation (#211). Register stores
// it on the [Replier] (so a muted addressee's route is discarded before the floor
// is taken) and, when barge-in built the floor, binds a [MuteCut] reactor beside
// [BargeIn] (so muting the speaking Agent cuts its turn). A nil view is the
// feature-off default — voice standalone / the benchmark are byte-for-byte
// unchanged.
func WithMute(v MuteView) Option {
	return func(c *Conversation) { c.mutes = v }
}

// MuteCut is the [Reactor] that cuts a speaking Agent's turn the moment it is
// muted (#211, AC2). On a [voiceevent.MuteChanged] with Muted=true it asks the
// [Floor] to yield ONLY if that Agent holds it ([Floor.YieldAgent] — a held-but-
// silent pre-audio turn is killed too), and on an actual cut publishes
// [voiceevent.TurnEnded] with the distinct [voiceevent.TurnEndMute] reason. It
// NEVER publishes [voiceevent.BargeDetected]: a mute is a deliberate GM control
// action, not a human barge (CONTEXT.md reserves Barge-in for the
// human-interrupts-Agent case). An unmute does nothing here — re-enabling an
// Agent is the matcher-restore / route-gate path, not a floor cut.
//
// It is bound beside [BargeIn] (before the [Replier]) on the same barge-in floor;
// muting an Agent that is not the current holder is a no-op, so it never disturbs
// whoever holds the floor (AC3).
type MuteCut struct {
	floor *Floor
}

// NewMuteCut builds a mute-cut reactor over floor. floor must be non-nil.
func NewMuteCut(floor *Floor) *MuteCut {
	if floor == nil {
		panic("orchestrator.NewMuteCut: floor must not be nil")
	}
	return &MuteCut{floor: floor}
}

// Bind subscribes the reactor to [voiceevent.MuteChanged] on bus and returns the
// unsubscribe func. It implements [Reactor]; bus must be non-nil.
func (m *MuteCut) Bind(_ context.Context, bus *voiceevent.Bus) (cancel func()) {
	if bus == nil {
		panic("orchestrator.MuteCut.Bind: bus must not be nil")
	}
	return voiceevent.On(bus, func(e voiceevent.MuteChanged) {
		if !e.Muted {
			return // an unmute never cuts a turn
		}
		if turnID, ok := m.floor.YieldAgent(e.AgentID); ok {
			// The mute cut the Agent's turn — announce it with the distinct mute
			// reason so the transcript/history commit as a barge would (delivered
			// sentences only, ADR-0012) while the cause stays a mute, never a barge.
			bus.Publish(voiceevent.TurnEnded{At: time.Now(), TurnID: turnID, Reason: voiceevent.TurnEndMute})
		}
	})
}
