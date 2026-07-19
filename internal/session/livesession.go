package session

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// LiveSession is a handle to ONE live Voice Session (#448), carrying the
// session-scoped operations — the mute set, GM puppeteering (SayAs /
// SpeakAsButler) and Highlight replay — that the lease/lifecycle [Manager] hands
// out via [Manager.Live]. The handle is pinned to the session that was active
// when it was obtained: every operation revalidates that its session is STILL
// the active one (the one revalidate implementation) and fails
// [ErrNoActiveSession] once stale — after the session ended, or after a new one
// started. A handle is cheap, safe for concurrent use, and never outlives its
// usefulness silently: staleness surfaces as ErrNoActiveSession (the idle nil
// for the MutedAgentIDs read), never as a misdirected write.
type LiveSession struct {
	m  *Manager
	as *activeSession
}

// Live returns a handle to tenantID's currently active Voice Session, or nil when
// that Tenant has none (the caller's ErrNoActiveSession moment). The handle stays
// valid only while that same session is the Tenant's active one; see [LiveSession].
func (m *Manager) Live(tenantID uuid.UUID) *LiveSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	as := m.active[tenantID]
	// A session in its end window (as.ended, the #487 tombstone) is already tearing
	// down — its bus bridge is cut — so hand out no handle: a mute/say/replay then
	// would publish onto a detached bus and falsely report success (#488 review item
	// 6). Idle (nil) and ending both map to the caller's ErrNoActiveSession.
	if as == nil || as.ended {
		return nil
	}
	return &LiveSession{m: m, as: as}
}

// revalidate is THE one implementation of the capture → drop-lock → revalidate
// dance the session-scoped ops share (#448): it re-locks Manager.mu, confirms
// this handle's session is still the active one, runs fn (which may be nil for
// pure liveness checks) under the lock, and unlocks. A stale handle — its
// session ended or another one started between the capture and now — fails
// ErrNoActiveSession without running fn, so an op can never mutate or publish
// into a session it was not obtained from.
func (l *LiveSession) revalidate(fn func()) error {
	l.m.mu.Lock()
	defer l.m.mu.Unlock()
	// Stale once this handle's session is no longer the Tenant's active one, OR once
	// it has entered its end window (as.ended, the #487 tombstone): an op arriving
	// during teardown must refuse rather than publish onto the already-detached bus
	// and report a phantom success (#488 review item 6).
	if l.m.active[l.as.tenantID] != l.as || l.as.ended {
		return ErrNoActiveSession
	}
	if fn != nil {
		fn()
	}
	return nil
}

// MutedAgentIDs is a sorted snapshot of the session's currently-muted Agent ids,
// or nil when the handle is stale — the same shape as the idle Manager read
// backing GetSession's reload truth (AC5).
func (l *LiveSession) MutedAgentIDs() []string {
	var ids []string
	if err := l.revalidate(func() { ids = mutedIDsLocked(l.as.muted) }); err != nil {
		return nil
	}
	return ids
}

// SetAgentMute mutes or unmutes one voiced Agent in this session, returning the
// resulting sorted muted-id set (#211). It rejects an agentID that is not a
// VOICED Agent of the session's Campaign — a foreign agent, an unknown id, or
// the Address-Only Butler (never a mute target, ADR-0009/ADR-0024) — with
// ErrAgentNotInCampaign, and fails ErrNoActiveSession once the handle is stale.
//
// Validation and the write are SESSION-ATOMIC (mirrors SetAllMute): the Campaign
// is listed with no lock held (the store read may block), then the set is
// written only if this handle's session is still active (revalidate) — so a
// session swap between the roster read and the write can never sneak a foreign
// agent (valid in the old Campaign, not the new) into the new session's mute
// set. The write-then-publish runs under pubMu so it is globally ordered against
// a concurrent SetAllMute (no reverse-order events).
func (l *LiveSession) SetAgentMute(ctx context.Context, agentID string, muted bool) ([]string, error) {
	// Fail a stale handle BEFORE the roster read (the shape the pre-handle ops
	// had: active-session check first), so no store round-trip runs — and no
	// store error masks the staleness — for a session that is already gone. The
	// authoritative check is the one under pubMu below; this one only fails fast.
	if err := l.revalidate(nil); err != nil {
		return nil, err
	}
	agents, err := l.m.store.ListAgents(ctx, l.as.campaignID)
	if err != nil {
		return nil, fmt.Errorf("session: list agents for mute: %w", err)
	}
	if !agentInList(voicedAgents(agents), agentID) {
		return nil, ErrAgentNotInCampaign
	}

	l.as.pubMu.Lock()
	defer l.as.pubMu.Unlock()

	var (
		changed bool
		ids     []string
	)
	if err := l.revalidate(func() {
		changed = applyMuteLocked(l.as.muted, agentID, muted)
		ids = mutedIDsLocked(l.as.muted)
	}); err != nil {
		return nil, err
	}
	// Publish onto THIS session's own bus (#487): the session-local mute reactor
	// subscribes there, and Forward stamps it onto the process bus for the relay.
	// The publish runs outside Manager.mu (subscribers re-read Muted) but inside
	// pubMu (the #211 ordering); l.as is still the active session (revalidate above).
	if changed {
		l.as.bus.Publish(voiceevent.MuteChanged{At: time.Now(), AgentID: agentID, Muted: muted})
	}
	return ids, nil
}

// SetAllMute mutes or unmutes every mutable Agent of the session's Campaign (the
// Character NPCs from store.ListAgents, minus the Address-Only Butler — which is
// voiced now (ADR-0009 #299 amendment) but is not a mute target, mute being
// matcher-owned and Character-only), returning the resulting sorted muted-id set
// (#211). The roster is listed with no lock held (the store read may block),
// then the set is applied only if this handle's session is still active
// (revalidate) — a session that ended (or a new one that started) while listing
// fails ErrNoActiveSession. The apply + its per-change MuteChanged burst run
// under pubMu, so the whole mute-all is ordered atomically against a concurrent
// per-Agent toggle.
func (l *LiveSession) SetAllMute(ctx context.Context, muted bool) ([]string, error) {
	// Stale-fast before the roster read; the authoritative check runs under
	// pubMu below (mirrors SetAgentMute).
	if err := l.revalidate(nil); err != nil {
		return nil, err
	}
	agents, err := l.m.store.ListAgents(ctx, l.as.campaignID)
	if err != nil {
		return nil, fmt.Errorf("session: list agents for mute-all: %w", err)
	}

	l.as.pubMu.Lock()
	defer l.as.pubMu.Unlock()

	var (
		changes []voiceevent.MuteChanged
		ids     []string
	)
	if err := l.revalidate(func() {
		changes = make([]voiceevent.MuteChanged, 0, len(agents))
		for _, a := range voicedAgents(agents) {
			id := a.ID.String()
			if applyMuteLocked(l.as.muted, id, muted) {
				changes = append(changes, voiceevent.MuteChanged{AgentID: id, Muted: muted})
			}
		}
		ids = mutedIDsLocked(l.as.muted)
	}); err != nil {
		return nil, err
	}
	now := time.Now()
	for _, c := range changes {
		c.At = now
		l.as.bus.Publish(c) // #487: onto this session's bus (Forward stamps to process)
	}
	return ids, nil
}

// SayAs publishes a GM-puppeteered direct-speech request (#295, ADR-0010): the
// voiced Agent with agentID speaks text verbatim in this Voice Session. It
// rejects an agentID that is not an Agent of the session's Campaign — a foreign
// agent or an unknown id — with ErrAgentNotInCampaign. The now-voiced Butler
// (ADR-0009 #299 amendment) IS a valid target reached via
// [LiveSession.SpeakAsButler]; the Discord /say roster still excludes it
// (say.go's voiced filter), so a GM cannot puppet it by hand.
//
// Validation and the publish are SESSION-ATOMIC (mirrors SetAgentMute): the
// Campaign is listed with no lock held (the store read may block), then the
// event is published only if this handle's session is still active (revalidate)
// — so a session swap between the roster read and the publish can never voice a
// foreign agent into the new session. It publishes [voiceevent.SpeakRequested]
// carrying the agent's Target (id + the Agent's OWN role + display name — a
// butler-role Target projects a KindButler line, ADR-0040), a fresh TurnID, and
// the text — NOT [voiceevent.AddressRouted], which would trigger the LLM Replier
// (ADR-0024). The GM mute is deliberately NOT consulted here (puppeteering is a
// GM override, so a muted NPC still speaks a /say — the DirectSpeech reactor
// bypasses the mute gate).
func (l *LiveSession) SayAs(ctx context.Context, agentID, text string) error {
	// Stale-fast before the roster read (the pre-handle SayAs checked the active
	// session first, so an ended session never reached the store and a store
	// error could never mask ErrNoActiveSession); the publish-guarding check
	// below stays the authoritative one.
	if err := l.revalidate(nil); err != nil {
		return err
	}
	agents, err := l.m.store.ListAgents(ctx, l.as.campaignID)
	if err != nil {
		return fmt.Errorf("session: list agents for say: %w", err)
	}
	var target storage.Agent
	found := false
	for _, a := range agents {
		if a.ID.String() == agentID {
			target = a
			found = true
			break
		}
	}
	if !found {
		return ErrAgentNotInCampaign
	}

	if err := l.revalidate(nil); err != nil {
		return err
	}
	l.as.bus.Publish(voiceevent.SpeakRequested{ // #487: onto this session's bus (Forward stamps to process)
		At:     time.Now(),
		TurnID: voiceevent.NewTurnID(),
		Target: voiceevent.AddressTarget{
			AgentID:   agentID,
			AgentRole: sayRole(target.Role),
			Name:      target.Name,
		},
		Text: text,
	})
	return nil
}

// SpeakAsButler voices text verbatim as the session Campaign's Butler (#365) —
// the recap decision-6a voiced on-ramp. It resolves the Butler from the roster
// and delegates to [LiveSession.SayAs] ON THE SAME HANDLE (so a session that
// rolled over between the two roster reads is refused rather than voiced into
// the successor), and the published SpeakRequested carries the Butler's
// butler-role Target — the transcript projects a KindButler line through the
// NORMAL relay projection (no hand-crafted row). A campaign with no Butler
// yields ErrAgentNotInCampaign, and a VOICELESS Butler (empty VoiceID — the
// default auto-Butler) yields ErrButlerVoiceless BEFORE any publish, so the
// recap surface can degrade to text rather than persist a phantom line for
// unsynthesizable speech (AC1, ADR-0012).
func (l *LiveSession) SpeakAsButler(ctx context.Context, text string) error {
	// Stale-fast before the roster read (mirrors SayAs, which re-checks before
	// the publish).
	if err := l.revalidate(nil); err != nil {
		return err
	}
	agents, err := l.m.store.ListAgents(ctx, l.as.campaignID)
	if err != nil {
		return fmt.Errorf("session: list agents for butler say: %w", err)
	}
	for _, a := range agents {
		if a.Role != storage.AgentRoleButler {
			continue
		}
		// The default auto-Butler is voiceless (empty VoiceID). Refuse BEFORE SayAs
		// publishes anything, so no phantom KindButler line is persisted for speech the
		// room can never hear (AC1: "when a live session has a VOICED Butler").
		voice, err := storage.VoiceFromJSON(a.Voice)
		if err != nil {
			return fmt.Errorf("session: decode butler voice: %w", err)
		}
		if voice.VoiceID == "" {
			return ErrButlerVoiceless
		}
		return l.SayAs(ctx, a.ID.String(), text)
	}
	return ErrAgentNotInCampaign
}

// ReplayHighlight publishes a [voiceevent.ReplayRequested] so the orchestrator's
// ClipReplay reactor plays a promoted Session Highlight's clip into this
// session's voice channel (#310, Epic 8, ADR-0051 GM-only sharing). The clipKey
// is the blob key the ShareHighlight RPC already resolved (and
// campaign-ownership-checked) — the handle only gates on its session still being
// live (revalidate, so a session that ended is refused rather than replayed
// into) and mints the turn (ADR-0005: the event carries the KEY, never audio).
func (l *LiveSession) ReplayHighlight(_ context.Context, clipKey string) error {
	if err := l.revalidate(nil); err != nil {
		return err
	}
	l.as.bus.Publish(voiceevent.ReplayRequested{ // #487: onto this session's bus (Forward stamps to process)
		At:      time.Now(),
		TurnID:  voiceevent.NewTurnID(),
		ClipKey: clipKey,
	})
	return nil
}

// The session-scoped ops below stay reachable on the Manager as one-line routes
// through the CURRENT LiveSession (#448): the existing consumer seams
// (rpc.SessionManager, rpc.HighlightReplayer, presence.SessionMuter,
// presence.SayControl, presence.ButlerVoicer) are satisfied by *Manager and
// resolve the live Voice Session per call — a handle captured at boot would be
// forever stale. Idle maps to the same ErrNoActiveSession a stale handle
// reports, so callers see one error contract either way.

// SetAgentMute routes to [LiveSession.SetAgentMute] on tenantID's session; no
// session for that Tenant is ErrNoActiveSession (AC4). Tenant-scoped end-to-end
// (#488): the op resolves and acts on m.active[tenantID] under the lock, so the
// old snapshot-check-act TOCTOU gap is gone.
func (m *Manager) SetAgentMute(ctx context.Context, tenantID uuid.UUID, agentID string, muted bool) ([]string, error) {
	l := m.Live(tenantID)
	if l == nil {
		return nil, ErrNoActiveSession
	}
	return l.SetAgentMute(ctx, agentID, muted)
}

// SetAllMute routes to [LiveSession.SetAllMute] on tenantID's session; no session
// for that Tenant is ErrNoActiveSession.
func (m *Manager) SetAllMute(ctx context.Context, tenantID uuid.UUID, muted bool) ([]string, error) {
	l := m.Live(tenantID)
	if l == nil {
		return nil, ErrNoActiveSession
	}
	return l.SetAllMute(ctx, muted)
}

// SayAs routes to [LiveSession.SayAs] on tenantID's session; no session for that
// Tenant is ErrNoActiveSession (refused before any roster lookup).
func (m *Manager) SayAs(ctx context.Context, tenantID uuid.UUID, agentID, text string) error {
	l := m.Live(tenantID)
	if l == nil {
		return ErrNoActiveSession
	}
	return l.SayAs(ctx, agentID, text)
}

// SpeakAsButler routes to [LiveSession.SpeakAsButler] on tenantID's session
// (satisfies presence.ButlerVoicer structurally); no session for that Tenant is
// ErrNoActiveSession.
func (m *Manager) SpeakAsButler(ctx context.Context, tenantID uuid.UUID, text string) error {
	l := m.Live(tenantID)
	if l == nil {
		return ErrNoActiveSession
	}
	return l.SpeakAsButler(ctx, text)
}

// ReplayHighlight routes to [LiveSession.ReplayHighlight] on tenantID's session;
// no session for that Tenant is ErrNoActiveSession and publishes nothing.
func (m *Manager) ReplayHighlight(ctx context.Context, tenantID uuid.UUID, clipKey string) error {
	l := m.Live(tenantID)
	if l == nil {
		return ErrNoActiveSession
	}
	return l.ReplayHighlight(ctx, clipKey)
}

// sayRole maps an Agent's storage Role to the [voiceevent.AddressTarget] role
// string the transcript relay keys its Line Kind off (ADR-0040): the Butler yields
// the butler role (→ KindButler pill), every other Agent the character role. The two
// vocabularies share their underlying strings, but mapping explicitly keeps SayAs
// decoupled from that coincidence.
func sayRole(r storage.AgentRole) string {
	if r == storage.AgentRoleButler {
		return voiceevent.AgentRoleButler
	}
	return voiceevent.AgentRoleCharacter
}

// agentInList reports whether agentID (a UUID string) names an Agent in agents.
func agentInList(agents []storage.Agent, agentID string) bool {
	for _, a := range agents {
		if a.ID.String() == agentID {
			return true
		}
	}
	return false
}

// voicedAgents returns only the Agents the mute subsystem can act on — the
// Character NPCs. The auto-created Butler (agent_role='butler') now enters the
// voiced wirenpc Roster/Matcher/Cast (ADR-0009 #299 amendment), but it stays
// Address-Only and mute is matcher-owned and Character-only: muting the Butler is
// refused, so filtering it here could only ever record a phantom id that silences
// nothing. Filtering it here is the single chokepoint both SetAgentMute (which
// then rejects the Butler with ErrAgentNotInCampaign) and SetAllMute (which then
// skips it) share, so the live mute set is exactly the set of Character Agents —
// and GetSession's reload truth (muted_agent_ids) never lists the Butler.
func voicedAgents(agents []storage.Agent) []storage.Agent {
	out := make([]storage.Agent, 0, len(agents))
	for _, a := range agents {
		if a.Role == storage.AgentRoleButler {
			continue
		}
		out = append(out, a)
	}
	return out
}

// applyMuteLocked sets or clears agentID in the mute set, reporting whether the
// set actually changed (so an idempotent re-mute publishes nothing). Caller holds
// Manager.mu.
func applyMuteLocked(set map[string]struct{}, agentID string, muted bool) bool {
	_, was := set[agentID]
	if muted == was {
		return false
	}
	if muted {
		set[agentID] = struct{}{}
	} else {
		delete(set, agentID)
	}
	return true
}

// mutedIDsLocked returns the muted ids as a sorted slice. Caller holds Manager.mu.
func mutedIDsLocked(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
