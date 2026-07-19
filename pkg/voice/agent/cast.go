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

	// linesMu guards the per-Ensemble-Turn resolved user lines (#473 review):
	// [Config.SpeakerName] is a live cache lookup, so the Cast pins each member's
	// speaker-attributed line at Draft time and replays the identical string for
	// the same turn's Speak/React/SpeakReaction — a single-slot store (ensemble
	// turns are serialized by the floor), retired when the next turn's Drafts
	// arrive. See [Cast.turnLine].
	linesMu     sync.Mutex
	linesTurnID string
	lines       map[string]string // AgentID -> the user line resolved at Draft time
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
// [orchestrator.ReplyStrategy.Stream] — it is the production strategy for a multi-NPC
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

// turnLine resolves the speaker-attributed user line for e ONCE per Ensemble
// Turn and Agent (#473 review): [Config.SpeakerName] is a live cache lookup, so
// its answer can change between a turn's Draft (the prompt) and its Speak/React/
// SpeakReaction (the history commit) — e.g. the speaker Warm fill landing
// mid-turn would commit "Artusas: ..." for a line the model reasoned over as
// "Player / DM: ...". The FIRST resolution for a (TurnID, AgentID) is pinned and
// every later same-turn call returns the identical string. start marks the Draft
// entry point: only it may retire the previous turn's pins (a single-slot store
// — ensemble turns are serialized by the floor), so a superseded turn's late
// Speak racing the superseding turn's Drafts degrades to a fresh resolution (the
// pre-pin behavior) instead of wiping the new turn's pins. A route without a
// TurnID resolves fresh (nothing to pin to).
func (c *Cast) turnLine(r *Replier, e voiceevent.AddressRouted, start bool) string {
	if e.TurnID == "" {
		return r.userLine(e.SpeakerID, e.Text)
	}
	c.linesMu.Lock()
	defer c.linesMu.Unlock()
	if c.linesTurnID != e.TurnID {
		if !start {
			return r.userLine(e.SpeakerID, e.Text)
		}
		c.linesTurnID = e.TurnID
		c.lines = make(map[string]string, 2)
	}
	id := r.cfg.Persona.AgentID
	if line, ok := c.lines[id]; ok {
		return line
	}
	line := r.userLine(e.SpeakerID, e.Text)
	c.lines[id] = line
	return line
}

// Draft implements [orchestrator.EnsembleSpeaker]: it produces the addressed
// Agent's would-be reply text WITHOUT mutating anything (the speculative fan-out
// half of an Ensemble Turn, ADR-0025/#301), by delegating to that member's
// [Replier.draftWithLine] with the user line pinned via [Cast.turnLine] — so the
// same turn's Speak commits the identical attribution the draft reasoned over.
// An unknown (or removed) Agent yields "", nil — the same "no one answers"
// signal the coordinator reads as an empty draft, never an error.
func (c *Cast) Draft(ctx context.Context, e voiceevent.AddressRouted) (string, error) {
	r := c.lookup(e.Target.AgentID)
	if r == nil {
		return "", nil // no Agent answers for this route
	}
	return r.draftWithLine(ctx, c.turnLine(r, e, true), e.Text)
}

// Speak implements [orchestrator.EnsembleSpeaker]: it speaks the winning Lead's
// pre-generated draft as the addressed Agent's turn (committing the delivered text
// to that member's history, ADR-0012), reusing the user line pinned at Draft time
// ([Cast.turnLine]) so the committed message is the one the draft reasoned over.
// An unknown Agent dispatches nothing and returns "", nil.
func (c *Cast) Speak(ctx context.Context, e voiceevent.AddressRouted, draft string, dispatch func(orchestrator.Reply) error) (string, error) {
	r := c.lookup(e.Target.AgentID)
	if r == nil {
		return "", nil // no Agent answers for this route
	}
	return r.speakDraftModality(ctx, c.turnLine(r, e, false), e.Text, draft, dispatch)
}

// SpeakFallback implements [orchestrator.FallbackSpeaker] (#473 review): when an
// Ensemble Turn's EVERY candidate Draft failed terminally, the coordinator speaks
// the top-scored candidate's canned fallback line through its member Replier —
// the routed path's [Config.FallbackLine] mechanism: dispatched in the member's
// Voice, never committed to history, refused under a cancelled ctx (a barged unit
// stays silent) and for a voiceless Persona (an empty VoiceID must never reach
// TTS). An unknown (or removed) Agent speaks nothing.
func (c *Cast) SpeakFallback(ctx context.Context, agentID string, dispatch func(orchestrator.Reply) error) bool {
	r := c.lookup(agentID)
	if r == nil {
		return false
	}
	return r.speakFallback(ctx, dispatch)
}

// React implements [orchestrator.CrossTalker]: it produces the addressed Agent's
// would-be Cross-talk Reaction to the Lead's delivered line WITHOUT mutating
// anything (the speculative half of the Reaction phase, ADR-0025/#302), by
// delegating to that member's [Replier.reactWithLine] with the user line pinned
// at the reactor's own Draft ([Cast.turnLine]) — so the composite prompt and the
// composite SpeakReaction later commits can never drift. An unknown (or removed)
// Agent yields "", nil — the "no one reacts" signal the coordinator reads as a
// decline.
func (c *Cast) React(ctx context.Context, e voiceevent.AddressRouted, leadName, leadText string) (string, error) {
	r := c.lookup(e.Target.AgentID)
	if r == nil {
		return "", nil // no Agent reacts for this route
	}
	return r.reactWithLine(ctx, c.turnLine(r, e, false), e.Text, leadName, leadText)
}

// ReactsAsText implements [orchestrator.ReactionModality]: it reports whether the
// addressed Agent would deliver its Cross-talk Reaction as channel TEXT (the same
// [AnswerAsText] decision SpeakReaction applies), the coordinator's pre-render seam
// (#389). An unknown (or removed) Agent — and every non-Butler — is never text.
func (c *Cast) ReactsAsText(agentID, utterance, reaction string) bool {
	r := c.lookup(agentID)
	if r == nil {
		return false
	}
	return r.ReactsAsText(utterance, reaction)
}

// SpeakReaction implements [orchestrator.CrossTalker]: it speaks the addressed
// Agent's pre-generated Reaction as its own sub-turn (committing the delivered text
// to that member's history, ADR-0012), rebuilding the composite from the user line
// pinned at Draft time ([Cast.turnLine]) so it commits the SAME composite React
// reasoned over. An unknown Agent dispatches nothing and returns "", nil.
func (c *Cast) SpeakReaction(ctx context.Context, e voiceevent.AddressRouted, leadName, leadText, reaction string, dispatch func(orchestrator.Reply) error) (string, error) {
	r := c.lookup(e.Target.AgentID)
	if r == nil {
		return "", nil // no Agent reacts for this route
	}
	return r.speakDraftModality(ctx, crossTalkUserText(c.turnLine(r, e, false), leadName, leadText), e.Text, reaction, dispatch)
}
