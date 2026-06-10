package orchestrator

import "time"

// SetClock overrides the floor's clock for deterministic coalesce-window tests
// (external test package). Production code always uses the real time.Now.
func (f *Floor) SetClock(now func() time.Time) {
	f.mu.Lock()
	f.now = now
	f.mu.Unlock()
}

// SetFloor installs floor on the replier for the barge-in/coalesce wiring tests
// (external test package). Production wiring sets r.floor inside
// Conversation.Register.
func (r *Replier) SetFloor(floor *Floor) { r.floor = floor }
