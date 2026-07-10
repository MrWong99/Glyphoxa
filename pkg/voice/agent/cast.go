package agent

import (
	"context"
	"sync"

	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// Cast multiplexes one reply strategy across several [Replier]s, keyed by the
// Agent they answer for. It is the single reply strategy [orchestrator.Conversation]
// wires when more than one NPC shares the conversation: each [voiceevent.AddressRouted]
// is delegated to the one Replier whose [Persona.AgentID] matches the route's
// [voiceevent.AddressTarget.AgentID], so N independent Agents speak on one bus and
// one barge-in floor.
//
// Why one strategy and not N bound repliers: the reply reactor takes the barge-in
// floor on every AddressRouted (reactor.go), so binding several independent
// repliers on one shared floor would corrupt floor state — they would each take
// and release the floor for the same turn. Routing a single addressed turn to a
// single Replier keeps the floor's one-turn-at-a-time invariant intact, which is
// why single-target is the safe default and the speculative fan-out / multiple
// floors of an Ensemble Turn (ADR-0025) is deferred.
//
// A route whose AgentID names no current member dispatches nothing and yields nil
// — the right answer when the matcher selected an Agent the Cast does not hold, or
// one that was removed mid-conversation. Members may be added and removed at
// runtime ([Cast.Add], [Cast.Remove]); the RWMutex guards the roster against a
// concurrent dispatch and the race detector.
type Cast struct {
	mu       sync.RWMutex
	repliers map[string]*Replier // AgentID -> Replier
}

// NewCast builds a Cast from the given repliers, keyed by each one's
// [Persona.AgentID]. When two repliers share an AgentID the last one wins (a
// wiring error the caller is responsible for avoiding). A Cast with no members is
// valid — every route says nothing until one is added.
func NewCast(repliers ...*Replier) *Cast {
	c := &Cast{repliers: make(map[string]*Replier, len(repliers))}
	for _, r := range repliers {
		c.Add(r)
	}
	return c
}

// Add registers r under its [Persona.AgentID], so subsequent routes for that
// Agent are delegated to it. Re-adding an AgentID replaces the prior member. A nil
// Replier is ignored.
func (c *Cast) Add(r *Replier) {
	if r == nil {
		return
	}
	c.mu.Lock()
	c.repliers[r.cfg.Persona.AgentID] = r
	c.mu.Unlock()
}

// Remove drops the Agent with the given AgentID from the Cast; subsequent routes
// for it say nothing. Removing an absent AgentID is a no-op.
func (c *Cast) Remove(agentID string) {
	c.mu.Lock()
	delete(c.repliers, agentID)
	c.mu.Unlock()
}

// lookup returns the Replier registered for agentID, or nil. It takes the read
// lock so dispatch never reads the roster concurrently with [Cast.Add] /
// [Cast.Remove]. The returned *Replier is itself concurrency-safe for its own turn.
func (c *Cast) lookup(agentID string) *Replier {
	c.mu.RLock()
	r := c.repliers[agentID]
	c.mu.RUnlock()
	return r
}

// ReplyStream returns the [orchestrator.StreamReplyFunc] that multiplexes the
// streaming reply path across the Cast: it looks up the Replier for
// e.Target.AgentID and delegates to that Replier's streaming turn. A route for an
// unknown (or removed) Agent dispatches nothing and returns nil. Install it with
// [orchestrator.WithReplyStream] — it is the production strategy for a multi-NPC
// conversation.
func (c *Cast) ReplyStream() orchestrator.StreamReplyFunc {
	return func(ctx context.Context, e voiceevent.AddressRouted, dispatch func(orchestrator.Reply) error) error {
		r := c.lookup(e.Target.AgentID)
		if r == nil {
			return nil // no Agent answers for this route
		}
		return r.ReplyStream()(ctx, e, dispatch)
	}
}

// Reply returns the batch [orchestrator.ReplyFunc] twin of [Cast.ReplyStream]: it
// delegates the route to the addressed Replier's batch turn, or returns nil for an
// unknown Agent. Provided for symmetry with [Replier.Reply]; production wires the
// streaming path.
func (c *Cast) Reply() orchestrator.ReplyFunc {
	return func(ctx context.Context, e voiceevent.AddressRouted) []orchestrator.Reply {
		r := c.lookup(e.Target.AgentID)
		if r == nil {
			return nil // no Agent answers for this route
		}
		return r.Reply()(ctx, e)
	}
}

// Draft implements [orchestrator.EnsembleSpeaker]: it produces the addressed
// Agent's would-be reply text WITHOUT mutating anything (the speculative fan-out
// half of an Ensemble Turn, ADR-0025/#301), by delegating to that member's
// [Replier.Draft]. An unknown (or removed) Agent yields "", nil — the same "no one
// answers" signal the coordinator reads as an empty draft, never an error.
func (c *Cast) Draft(ctx context.Context, e voiceevent.AddressRouted) (string, error) {
	r := c.lookup(e.Target.AgentID)
	if r == nil {
		return "", nil // no Agent answers for this route
	}
	return r.Draft(ctx, e.Text)
}

// Speak implements [orchestrator.EnsembleSpeaker]: it speaks the winning Lead's
// pre-generated draft as the addressed Agent's turn (committing the delivered text
// to that member's history, ADR-0012), by delegating to [Replier.SpeakDraft]. An
// unknown Agent dispatches nothing and returns "", nil.
func (c *Cast) Speak(ctx context.Context, e voiceevent.AddressRouted, draft string, dispatch func(orchestrator.Reply) error) (string, error) {
	r := c.lookup(e.Target.AgentID)
	if r == nil {
		return "", nil // no Agent answers for this route
	}
	return r.SpeakDraft(ctx, e.Text, draft, dispatch)
}
