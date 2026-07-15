package session

import (
	"sync/atomic"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// View is the pre-constructible active-session read (#448): the narrow
// Sessions{Snapshot} seam the transcript projectors (relay, chunker), the
// recallers (memory, KG facts) and the knowledge Tools adapter consume. Those
// collaborators are the Manager's construction-time [Deps], so they must exist
// BEFORE the Manager and cannot take the Manager itself — the old cycle the
// post-construction setters papered over. They take a View instead; NewManager
// binds it (Deps.View), which is what makes the wiring order un-breakable:
// until a Manager is bound — and no session can run before its Manager exists —
// and while the bound Manager is idle, Snapshot truthfully reports no active
// session. A View binds to exactly one Manager; a second bind is a boot bug and
// panics.
type View struct {
	mgr atomic.Pointer[Manager]
}

// NewView returns an unbound View: Snapshot reports no active session until
// NewManager binds a Manager to it via [Deps].
func NewView() *View {
	return &View{}
}

// Snapshot reports the bound Manager's active Voice Session and true, or the
// zero value and false when unbound or idle — the same contract as
// [Manager.Snapshot], which every consumer's Sessions seam was written against.
func (v *View) Snapshot() (storage.VoiceSession, bool) {
	m := v.mgr.Load()
	if m == nil {
		return storage.VoiceSession{}, false
	}
	return m.Snapshot()
}

// bind attaches the one Manager this View reads, called by NewManager
// (Deps.View). The CompareAndSwap makes double-wiring loud: two Managers
// sharing a View would silently split the active-session truth.
func (v *View) bind(m *Manager) {
	if !v.mgr.CompareAndSwap(nil, m) {
		panic("session: View is already bound to a Manager")
	}
}
