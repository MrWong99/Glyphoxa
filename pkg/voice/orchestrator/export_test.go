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

// SetErrorHandler installs onError on the segmenter for the off-loop STT error
// tests (external test package). Production wiring sets it inside
// Conversation.Register from [WithErrorHandler].
func (s *Segmenter) SetErrorHandler(fn ErrorFunc) { s.onError = fn }
