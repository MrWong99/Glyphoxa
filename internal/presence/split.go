package presence

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// PoolControl is the CLAIM-PLANE surface the live-control commands consult when
// the LOCAL Manager holds no session for the Tenant (#483/#503): at replicas > 1
// the interactions are dispatched by the elected presence owner, but the
// Tenant's session may be hosted by ANOTHER worker in the pool. Active is the
// pool-wide intent read; the control verbs RELAY through the
// voice_session_controls queue the hosting worker drains on its heartbeat tick
// (write-then-poll, ADR-0057 (b)/ADR-0051 — the dispatch never moves the
// session, ADR-0006/0057 (e)). *session.IntentControl satisfies it. nil in
// -mode all, where the one process hosts every session and the local read is
// already the whole truth (AC3: single-worker behavior unchanged).
type PoolControl interface {
	Active(ctx context.Context, tenantID uuid.UUID) (storage.VoiceSession, bool, error)
	SetAgentMute(ctx context.Context, tenantID uuid.UUID, agentID string, muted bool) ([]string, error)
	SetAllMute(ctx context.Context, tenantID uuid.UUID, muted bool) ([]string, error)
	SayAs(ctx context.Context, tenantID uuid.UUID, agentID, text string) error
	DirectAs(ctx context.Context, tenantID uuid.UUID, agentID, text string, turns int) error
}

// compile-time proof the claim-plane control satisfies the seam.
var _ PoolControl = (*session.IntentControl)(nil)

// poolActive resolves the Tenant's session from the pool when the LOCAL Manager
// has none: the cross-pod branch's entry read. A nil pool (-mode all) or a pool
// read error reports not-live — the caller then gives the plain no-session
// guard, the pre-#483 behavior.
func poolActive(ctx context.Context, pool PoolControl, tenantID uuid.UUID) (storage.VoiceSession, bool) {
	if pool == nil {
		return storage.VoiceSession{}, false
	}
	vs, live, err := pool.Active(ctx, tenantID)
	if err != nil || !live {
		return storage.VoiceSession{}, false
	}
	return vs, true
}

// replyControlOutcome answers a relayed cross-pod control (#503). Success posts
// the confirming text ONLY on the worker's confirmed terminal row (the relay
// polled it 'done' — ADR-0012: never claim what was not confirmed). An
// unconfirmed relay (ErrControlPending) posts the honest not-confirmed wording;
// a session that ended mid-relay posts the plain guard; anything else surfaces
// the REAL error text — a failure is never a silent no-op.
func replyControlOutcome(ic *Interaction, err error, success string) error {
	switch {
	case err == nil:
		return ic.ReplyEphemeral(success)
	case errors.Is(err, session.ErrControlPending):
		return ic.ReplyEphemeral("The worker hosting the session has not confirmed the control yet — it may still land; check the session and retry if needed.")
	case errors.Is(err, session.ErrNoActiveSession):
		return ic.ReplyEphemeral("No Voice Session is active.")
	default:
		return ic.ReplyEphemeral(fmt.Sprintf("The worker hosting the session could not apply that: %v", err))
	}
}
