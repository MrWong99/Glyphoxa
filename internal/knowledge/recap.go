package knowledge

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/recap"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/tool"
)

// recapListLimit caps how many recent Voice Sessions the picker scans for a
// recappable (ended, non-empty) row. 50 matches the slash surface's server policy
// (presence.recapListLimit / rpc.listSessionsLimit) so the in-voice recap Tool sees
// the same window — enough that a pageful of running/failed/empty rows can't hide a
// real recappable one.
const recapListLimit = 50

// recapToolTimeout bounds the in-voice recap work: the recap engine fans out
// map-reduce windows and can run for many seconds. It is a child of the turn ctx
// (WithTimeout below), so a barge-in that cancels the turn also cancels the recap,
// while a slow/stuck LLM is cut off here rather than blocking the turn open-ended.
// A var (not a const) so a test can shrink it without a 120s wait; production 120s.
var recapToolTimeout = 120 * time.Second

// errNoRecappableSession is the friendly failure when no ended, non-empty Voice
// Session exists to recap (idle history, or only running/empty rows). The Tool
// handler surfaces it to the LLM as a plain reason, not a crash.
var errNoRecappableSession = errors.New("knowledge: no ended session with a transcript to recap")

// RecapEngine is the one-shot recap service the adapter drives: given Voice Session
// ids it renders their transcript and returns a Butler-flavoured recap (#272).
// *recap.Engine satisfies it; tests use a fake. Kept local (not the recap package's
// interface) so this package's contract is explicit and unit-fakeable.
type RecapEngine interface {
	Recap(ctx context.Context, sessionIDs []uuid.UUID) (recap.Result, error)
}

// RecapStore is the narrow storage read the recap adapter needs: the campaign's
// recent Voice Sessions, newest-first, to pick the recappable ones. *storage.Store
// satisfies it. Kept local so the adapter is unit-fakeable without a DB.
type RecapStore interface {
	ListVoiceSessions(ctx context.Context, campaignID uuid.UUID, limit int) ([]storage.VoiceSession, error)
}

// RecapAdapter implements tool.Recapper over a recap engine, a storage read, and
// the active-session source. It is the production wiring behind the recap Tool
// (#372): the Tool asks for the last N sessions' recap; this adapter resolves WHICH
// sessions entirely from server state — the active Campaign's newest ENDED non-empty
// rows — so the LLM never names a session id (ADR-0029) and can never recap another
// Campaign. Safe for concurrent use (its deps are). Built once at web boot.
type RecapAdapter struct {
	eng      RecapEngine
	store    RecapStore
	sessions Sessions
}

// NewRecap builds the adapter. All three deps must be non-nil — they are wiring
// requirements, so a nil is a boot bug, not a runtime condition (mirrors New).
func NewRecap(eng RecapEngine, store RecapStore, sessions Sessions) *RecapAdapter {
	if eng == nil || store == nil || sessions == nil {
		panic("knowledge: NewRecap: nil engine, store or sessions")
	}
	return &RecapAdapter{eng: eng, store: store, sessions: sessions}
}

// RecapLastSessions implements [tool.Recapper]. It resolves the active Campaign from
// the live session (no session ⇒ ErrNoActiveSession), lists its recent Voice
// Sessions and picks the newest `n` that are ENDED with a recorded transcript
// (isRecappable parity with presence.isRecappable — a running/failed/empty row is
// skipped), then recaps them under a turn-child timeout. No recappable session ⇒ a
// friendly error; recap.ErrNoTranscript is mapped to the same friendly error (a race
// let an empty row through). The recap prose is returned verbatim for the Tool to
// relay.
func (a *RecapAdapter) RecapLastSessions(ctx context.Context, n int) (string, error) {
	s, ok := a.sessions.Snapshot()
	if !ok {
		return "", ErrNoActiveSession
	}
	sessions, err := a.store.ListVoiceSessions(ctx, s.CampaignID, recapListLimit)
	if err != nil {
		return "", fmt.Errorf("knowledge: list voice sessions for recap: %w", err)
	}
	ids := make([]uuid.UUID, 0, n)
	for _, vs := range sessions {
		if isRecappable(vs) { // newest-first order preserved from the store
			ids = append(ids, vs.ID)
			if len(ids) == n {
				break
			}
		}
	}
	if len(ids) == 0 {
		return "", errNoRecappableSession
	}

	ctx, cancel := context.WithTimeout(ctx, recapToolTimeout)
	defer cancel()

	res, err := a.eng.Recap(ctx, ids)
	if errors.Is(err, recap.ErrNoTranscript) {
		return "", errNoRecappableSession
	}
	if err != nil {
		return "", fmt.Errorf("knowledge: recap: %w", err)
	}
	return res.Text, nil
}

// isRecappable reports whether a Voice Session can seed a default recap: it must
// have ENDED and have a recorded transcript (line_count > 0). Parity with
// presence.isRecappable — the two recap surfaces must not diverge on which sessions
// count. Duplicated as a one-liner rather than refactoring presence (whose copy
// stays authoritative for the slash surface).
func isRecappable(vs storage.VoiceSession) bool {
	return vs.Status == storage.VoiceSessionEnded && vs.LineCount > 0
}

// Compile-time assertion the adapter satisfies the Tool seam, and that the concrete
// engine/store satisfy the local read interfaces.
var (
	_ tool.Recapper = (*RecapAdapter)(nil)
	_ RecapEngine   = (*recap.Engine)(nil)
	_ RecapStore    = (*storage.Store)(nil)
)
