package memory

import "context"

// RecapStore persists and retrieves generated session recaps.
// Implementations must be safe for concurrent use.
type RecapStore interface {
	// SaveRecap persists a recap. If a recap for the same session already
	// exists it is replaced (upsert).
	SaveRecap(ctx context.Context, recap Recap) error

	// GetRecap retrieves the recap for the given session.
	// Returns (nil, nil) when no recap exists.
	GetRecap(ctx context.Context, sessionID string) (*Recap, error)
}
