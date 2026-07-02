package session

import "time"

// SetEndTimeoutForTest shrinks the per-step end budget (Finalize / end-write)
// so the #143 budget-separation tests run in milliseconds instead of the
// production 5s. Test-only; called before any session starts.
func (m *Manager) SetEndTimeoutForTest(d time.Duration) {
	m.endTimeout = d
}
