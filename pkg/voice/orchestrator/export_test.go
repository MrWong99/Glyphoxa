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

// WaitStreamUp blocks until the manager has a live session or timeout elapses,
// reporting whether one came up. Test-only: pipeline tests feed the first
// utterance only after the eager dial completes, so utterance 1 streams
// deterministically instead of racing the maintainer.
func (m *StreamManager) WaitStreamUp(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		up := m.stream != nil
		m.mu.Unlock()
		if up {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}
