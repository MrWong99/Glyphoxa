package presence

import (
	"context"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// PoolSession is the CLAIM-PLANE read the live-control commands consult when the
// LOCAL Manager holds no session for the Tenant (#483): at replicas > 1 the
// interactions are dispatched by the elected presence owner, but the Tenant's
// session may be hosted by ANOTHER worker in the pool — the local Manager then
// reports inactive and the handler would falsely reply "No Voice Session is
// active." while the session is very much live. *session.IntentControl satisfies
// it (its Active is the pool-wide intent read). nil in -mode all, where the one
// process hosts every session and the local read is already the whole truth.
type PoolSession interface {
	Active(ctx context.Context, tenantID uuid.UUID) (storage.VoiceSession, bool, error)
}

// splitLiveControlMsg is the reply for a live control (mute/muteall/say) whose
// session is live on ANOTHER worker: honest about the limitation instead of the
// false "No Voice Session is active." The cross-pod control plane that would make
// these work from the presence owner is tracked in #503 — this PR ships only the
// truthful degrade.
const splitLiveControlMsg = "This session is hosted by another worker; live controls aren't available from here yet. Use the web panel instead."

// replyNoLocalSession answers a live-control command whose local Manager holds no
// session for the Tenant (#483): when the claim plane shows the Tenant's session
// live on another worker it replies the split-mode limitation (see #503), else
// the plain no-session guard. A nil pool (single-process -mode all) or a pool
// read error degrades to the plain guard — the pre-#483 behavior.
func replyNoLocalSession(ctx context.Context, ic *Interaction, pool PoolSession) error {
	if pool != nil {
		if _, live, err := pool.Active(ctx, ic.TenantID()); err == nil && live {
			return ic.ReplyEphemeral(splitLiveControlMsg)
		}
	}
	return ic.ReplyEphemeral("No Voice Session is active.")
}
