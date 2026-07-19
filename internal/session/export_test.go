package session

import (
	"context"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
)

// TickForTest runs one claim-loop tick synchronously (reap → claim-and-start
// while capacity), so a test can drive assignment deterministically without the
// poll timer (#491). Test-only.
func (l *ClaimLoop) TickForTest(ctx context.Context) { l.tick(ctx) }

// DrainForTest waits for every live per-session goroutine to finish — the same
// wait Run does on ctx cancellation (the graceful drain, #491). Test-only.
func (l *ClaimLoop) DrainForTest() { l.wg.Wait() }

// SetEndTimeoutForTest shrinks the per-step end budget (Finalize / end-write)
// so the #143 budget-separation tests run in milliseconds instead of the
// production 5s. Test-only; called before any session starts.
func (m *Manager) SetEndTimeoutForTest(d time.Duration) {
	m.endTimeout = d
}

// BaseFactsForTest returns the FactsRecaller threaded onto the base voice config,
// so a test can assert Deps.Facts wired it (#126, #448). Test-only.
func (m *Manager) BaseFactsForTest() agent.FactsRecaller {
	return m.base.Facts
}
