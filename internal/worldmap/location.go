package worldmap

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
)

// The party-location clause for the volatile Hot Context tail (#540, ADR-0059
// note).
//
// This is the ONE piece of map data that earns a place in an NPC's prompt. Every
// other spatial question goes through the read-only Tool (#539) instead, because
// the facts block is capped and already competes with the neighbour walk — so
// anything added there silently evicts world knowledge.

// LocationBudget is the hard read budget for the clause, inside the turn ctx. Like
// kgfacts, an indexed OLTP read is sub-millisecond, so this only fires on a wedged
// DB — and when it does, the turn proceeds without the clause rather than stalling.
const LocationBudget = 30 * time.Millisecond

// LocationStore is the narrow read the clause needs; *storage.Store satisfies it.
type LocationStore interface {
	GetPartyMarker(ctx context.Context, campaignID, sessionID uuid.UUID) (storage.PartyMarker, error)
}

// Locator is the production [agent.LocationRecaller]. It renders one short clause
// naming where the party is, or "" — which drops the block entirely and leaves the
// prompt byte-identical to the pre-marker path.
type Locator struct {
	store LocationStore
	log   *slog.Logger
}

// NewLocator builds the recaller over the marker read.
func NewLocator(store LocationStore, log *slog.Logger) *Locator {
	if log == nil {
		log = slog.Default()
	}
	return &Locator{store: store, log: log}
}

// Location implements [agent.LocationRecaller].
//
// It NEVER errors and never stalls the turn: no session, no marker, a hidden
// marker, or a slow read all yield "" and the block is dropped.
//
// The marker is read per turn from the SESSION on the run context — never cached
// and never campaign-scoped — so two concurrent sessions on the shared Voice
// Instance pool cannot see each other's position (ADR-0057), and a GM moving the
// party takes effect on the very next turn.
func (l *Locator) Location(ctx context.Context, _ string) string {
	id, ok := session.FromContext(ctx)
	if !ok {
		// No live session: there is no party to be anywhere.
		return ""
	}

	ctx, cancel := context.WithTimeout(ctx, LocationBudget)
	defer cancel()

	marker, err := l.store.GetPartyMarker(ctx, id.CampaignID, id.SessionID)
	if err != nil {
		// A barge cancels the turn ctx — silent. Anything else is worth one warning,
		// but never a failed turn: an NPC that does not know where it is still speaks.
		if ctx.Err() == nil {
			l.log.Warn("party location unavailable; omitting the clause", "err", err)
		}
		return ""
	}
	return Clause(marker)
}

// Clause renders a PartyMarker as the prompt clause, or "" when there is nothing
// the table may be told.
//
// A gm_private Map, or a marker sitting on a hidden Pin, yields "" — the party's
// position is spoken aloud by whoever knows it, so a secret location in a prompt
// is a secret leaked at the table, exactly like a gm_private fact.
func Clause(m storage.PartyMarker) string {
	if !m.Set() || m.MapGMPrivate {
		return ""
	}
	place := strings.TrimSpace(m.PinLabel)
	area := strings.TrimSpace(m.MapName)
	switch {
	case m.PinID.Valid && m.PinHidden:
		// At a place the table must not be told about. Naming the surrounding area
		// alone would still be true and safe, but it would also be a hint that
		// something is here — so say nothing.
		return ""
	case place != "" && area != "":
		return fmt.Sprintf("You are at %s, in %s.", place, area)
	case place != "":
		return fmt.Sprintf("You are at %s.", place)
	case area != "":
		return fmt.Sprintf("You are in %s.", area)
	default:
		return ""
	}
}

// Compile-time assertion that Locator satisfies the tail seam.
var _ agent.LocationRecaller = (*Locator)(nil)
