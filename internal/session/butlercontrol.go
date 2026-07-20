package session

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// ButlerControl is the voiced-recap Butler voicer for the -mode voice worker
// (#503): local-first — when THIS worker hosts the Tenant's session the recap is
// spoken through the local Manager exactly as before — and only a local
// ErrNoActiveSession (the session lives on ANOTHER worker in the pool, or
// nowhere) falls through to the claim-plane relay, which answers
// ErrNoActiveSession itself when no session is live anywhere. Any OTHER local
// answer (success, ErrButlerVoiceless, ErrAgentNotInCampaign, …) stands: the
// local Manager already held the session, so re-asking the pool could only
// double-speak or lie. Wired in the worker boot only; the -mode all path keeps
// the plain *Manager (single process — the local read is the whole truth).
type ButlerControl struct {
	local  *Manager
	remote *IntentControl
}

// NewButlerControl builds the local-first Butler voicer over the worker's own
// Manager and the pool-wide control relay.
func NewButlerControl(local *Manager, remote *IntentControl) *ButlerControl {
	return &ButlerControl{local: local, remote: remote}
}

// SpeakAsButler voices text as the Tenant's Butler: locally when this worker
// hosts the session, else relayed to the hosting worker (#503).
func (b *ButlerControl) SpeakAsButler(ctx context.Context, tenantID uuid.UUID, text string) error {
	err := b.local.SpeakAsButler(ctx, tenantID, text)
	if !errors.Is(err, ErrNoActiveSession) {
		return err
	}
	return b.remote.SpeakAsButler(ctx, tenantID, text)
}
