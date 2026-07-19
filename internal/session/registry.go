package session

import (
	"sync"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// Registry is the process-wide index of the live [Manager]s (#487, replacing the
// single-bind View): it lets a process-wide bus consumer resolve the full Voice
// Session behind an event's stamped SessionID, and lets a control surface publish
// an event INTO a specific live session's own bus by Campaign. Unlike the old
// View — which bound EXACTLY ONE Manager and panicked on a second — any number of
// Managers register here, so concurrent Voice Sessions coexist. Safe for
// concurrent use.
//
// A Manager registers itself at construction ([Deps.Registry]) and stays
// registered for its lifetime; registration is additive and never panics.
type Registry struct {
	mu   sync.Mutex
	mgrs []*Manager
}

// NewRegistry returns an empty Registry: Resolve reports no session and
// PublishToCampaign routes nowhere until Managers register (via [Deps.Registry]).
func NewRegistry() *Registry {
	return &Registry{}
}

// register adds m to the index, called by [NewManager] via [Deps.Registry].
// Additive and idempotent-safe — no bind-once guard, so multiple Managers
// (multiple concurrent Voice Sessions) coexist without the old double-bind panic.
func (r *Registry) register(m *Manager) {
	r.mu.Lock()
	r.mgrs = append(r.mgrs, m)
	r.mu.Unlock()
}

// Resolve returns the live Voice Session with id sessionID and true, or the zero
// value and false when no registered Manager currently runs it (idle, ended, or a
// pre-registry / cross-session straggler). It is the [Sessions] read every
// process-wide bus consumer uses to attribute a stamped event to its origin
// session's Campaign FKs.
func (r *Registry) Resolve(sessionID uuid.UUID) (storage.VoiceSession, bool) {
	r.mu.Lock()
	mgrs := append([]*Manager(nil), r.mgrs...)
	r.mu.Unlock()
	for _, m := range mgrs {
		if vs, ok := m.Lookup(sessionID); ok {
			return vs, true
		}
	}
	return storage.VoiceSession{}, false
}

// PublishToCampaign publishes e onto the live session bus of the Voice Session
// running campaignID, returning true when a live session took it and false when
// no registered Manager runs that Campaign. It is the control-surface seam (the
// presence tape-consent buttons, #306) that must reach a specific session's
// session-local reactors — and, via [voiceevent.Forward], the process bus stamped
// with that session's id — rather than blindly broadcasting on the process bus.
func (r *Registry) PublishToCampaign(campaignID uuid.UUID, e voiceevent.Event) bool {
	r.mu.Lock()
	mgrs := append([]*Manager(nil), r.mgrs...)
	r.mu.Unlock()
	for _, m := range mgrs {
		if m.PublishToCampaign(campaignID, e) {
			return true
		}
	}
	return false
}

// Sessions is the narrow read a process-wide bus consumer needs: resolve a
// stamped event's SessionID to its full Voice Session (hence its Campaign). It
// replaces the old Snapshot() seam (#487): consumers no longer read a single
// global active session, they resolve the specific session each event names.
// *Registry satisfies it; tests fake it.
type Sessions interface {
	Resolve(sessionID uuid.UUID) (storage.VoiceSession, bool)
}
