package session

import (
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
)

// SetEndTimeoutForTest shrinks the per-step end budget (Finalize / end-write)
// so the #143 budget-separation tests run in milliseconds instead of the
// production 5s. Test-only; called before any session starts.
func (m *Manager) SetEndTimeoutForTest(d time.Duration) {
	m.endTimeout = d
}

// BaseFactsForTest returns the FactsRecaller threaded onto the base voice config,
// so a test can assert SetFacts wired it (#126). Test-only.
func (m *Manager) BaseFactsForTest() agent.FactsRecaller {
	return m.base.Facts
}
