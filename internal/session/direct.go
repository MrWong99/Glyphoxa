package session

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// GM directive track (ADR-0059): /direct lets the GM whisper a private steering
// note to ONE Character NPC — "Bart lies about the key" — that the Agent's
// Replier folds into its volatile Hot Context tail, without the table ever
// hearing it. Steering instead of the /say puppet takeover: the NPC still
// generates its own words, the GM only tilts them.
//
// The state is session-local and volatile (like the mute set): set/replaced/
// cleared via [LiveSession.DirectAs], consumed per committed Agent turn through
// the [agent.DirectiveRecaller] seam the Manager satisfies, and gone at session
// end. Nothing is persisted and nothing reaches the Transcript — the directive
// is a stage note, not table speech.

// directiveState is one Agent's active directive. remaining counts the
// committed turns the directive still applies to; < 0 means sticky (until
// cleared, replaced, or session end). Guarded by Manager.mu.
type directiveState struct {
	text      string
	remaining int
}

// stickyTurns marks a directive with no turn bound ([LiveSession.DirectAs]
// turns <= 0): it stays active until cleared, replaced, or the session ends.
const stickyTurns = -1

// DirectAs sets (or clears) the GM directive for the voiced Character NPC with
// agentID in this session (ADR-0059). A non-empty text REPLACES any active
// directive; empty text clears it. turns bounds how many committed Agent turns
// the directive rides (each single-target reply the Agent generates consumes
// one); turns <= 0 keeps it active until cleared, replaced, or session end.
//
// It rejects an agentID that is not a voiced Character of the session's
// Campaign — a foreign agent, an unknown id, or the Butler (the GM's own
// assistant needs no secret stage notes) — with ErrAgentNotInCampaign, and
// fails ErrNoActiveSession once the handle is stale. Validation and the write
// are SESSION-ATOMIC (mirrors SetAgentMute): the Campaign is listed with no
// lock held, then the state is written only if this handle's session is still
// active (revalidate), so a session swap can never smuggle a foreign agent's
// directive into the new session.
//
// Deliberately NO bus publish and NO transcript projection: the directive is
// GM-private. The Replier pulls it per turn through [Manager.Directive], so
// there is no event for the relay to leak.
func (l *LiveSession) DirectAs(ctx context.Context, agentID, text string, turns int) error {
	// Stale-fast before the roster read (the SetAgentMute shape): no store
	// round-trip for a session that is already gone.
	if err := l.revalidate(nil); err != nil {
		return err
	}
	agents, err := l.m.store.ListAgents(ctx, l.as.campaignID)
	if err != nil {
		return fmt.Errorf("session: list agents for direct: %w", err)
	}
	if !agentInList(voicedAgents(agents), agentID) {
		return ErrAgentNotInCampaign
	}

	return l.revalidate(func() {
		if text == "" {
			delete(l.as.directives, agentID)
			return
		}
		remaining := turns
		if remaining <= 0 {
			remaining = stickyTurns
		}
		l.as.directives[agentID] = &directiveState{text: text, remaining: remaining}
	})
}

// DirectAs routes to [LiveSession.DirectAs] on tenantID's session; no session
// for that Tenant is ErrNoActiveSession (refused before any roster lookup).
func (m *Manager) DirectAs(ctx context.Context, tenantID uuid.UUID, agentID, text string, turns int) error {
	l := m.Live(tenantID)
	if l == nil {
		return ErrNoActiveSession
	}
	return l.DirectAs(ctx, agentID, text, turns)
}

// Directive satisfies [agent.DirectiveRecaller] structurally (the MuteView
// pattern, #211): the Manager owns the session-local directive state, so it IS
// the per-turn directive source every session's Repliers consult. agentID is
// scanned across the active sessions (Agent ids are UUIDs, unique across
// Campaigns). consume marks a committed reply path: a turn-bounded directive's
// remaining budget is decremented then — the turn that consumes the last
// remaining still receives the text (the directive rides N turns, then
// vanishes) — while the speculative Draft/React consults only peek. Never
// errors, never blocks (a map read under the Manager lock), so it honors the
// recaller contract's degrade posture by construction.
func (m *Manager) Directive(_ context.Context, agentID string, consume bool) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, as := range m.active {
		d, ok := as.directives[agentID]
		if !ok {
			continue
		}
		if as.ended {
			return "" // the session is tearing down; its directives are already dead
		}
		text := d.text
		if consume && d.remaining > 0 {
			d.remaining--
			if d.remaining == 0 {
				delete(as.directives, agentID)
			}
		}
		return text
	}
	return ""
}

// DirectedAgentIDs is a sorted snapshot of the Agent ids with an active
// directive in tenantID's session, or nil when that Tenant has no live session
// — diagnostics/tests parity with MutedAgentIDs.
func (m *Manager) DirectedAgentIDs(tenantID uuid.UUID) []string {
	l := m.Live(tenantID)
	if l == nil {
		return nil
	}
	var ids []string
	if err := l.revalidate(func() {
		set := make(map[string]struct{}, len(l.as.directives))
		for id := range l.as.directives {
			set[id] = struct{}{}
		}
		ids = mutedIDsLocked(set)
	}); err != nil {
		return nil
	}
	return ids
}

// compile-time proof the Manager satisfies the storage-free directive seam the
// voice wiring consumes (mirrors the MuteView proof in wirenpc).
var _ interface {
	Directive(ctx context.Context, agentID string, consume bool) string
} = (*Manager)(nil)
