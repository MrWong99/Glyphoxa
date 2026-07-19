package session

import (
	"context"

	"github.com/google/uuid"
)

// Identity is the Voice Session a run's goroutines are executing on behalf of
// (#487, "Session-scoped attribution"): the session, its Campaign, and its
// Tenant. [Manager.Start] installs it on the run context so the per-turn
// consumers that DON'T ride the bus — memory recall's Recall(), KG-facts —
// resolve their session context from the ambient run context instead of a single
// global snapshot. The bus path resolves the session from the event's stamped
// SessionID instead; this is its non-bus twin.
type Identity struct {
	SessionID  uuid.UUID
	CampaignID uuid.UUID
	TenantID   uuid.UUID
}

type identityKey struct{}

// NewContext returns a copy of ctx carrying id, so a downstream per-turn consumer
// recovers it with [FromContext]. [Manager.Start] installs it on the run context
// the loop and its Agent turns descend from.
func NewContext(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// FromContext returns the [Identity] installed by [NewContext] and true, or the
// zero Identity and false when none is present (the bench / voice-standalone
// path, or a caller that never descended from a manager run context). A false
// result is the "no session to scope" signal — the consumer yields an empty read
// rather than an error.
func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(Identity)
	return id, ok
}
