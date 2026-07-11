package knowledge

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/recap"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// blockingEngine blocks until its ctx is cancelled, then returns ctx.Err() — the
// shape a slow/stuck recap LLM presents when the recapToolTimeout child fires.
type blockingEngine struct{}

func (blockingEngine) Recap(ctx context.Context, _ []uuid.UUID) (recap.Result, error) {
	<-ctx.Done()
	return recap.Result{}, ctx.Err()
}

// stubStore serves one ended, non-empty session so the picker selects it.
type stubStore struct{}

func (stubStore) ListVoiceSessions(_ context.Context, _ uuid.UUID, _ int) ([]storage.VoiceSession, error) {
	return []storage.VoiceSession{{ID: uuid.New(), Status: storage.VoiceSessionEnded, LineCount: 3}}, nil
}

// stubSessions reports a live session (resolves the Campaign).
type stubSessions struct{}

func (stubSessions) Snapshot() (storage.VoiceSession, bool) {
	return storage.VoiceSession{CampaignID: uuid.New()}, true
}

// TestRecapAdapterTimeoutMapsFriendlyAndTurnSurvives pins finding 1: when the recap
// engine blows the recapToolTimeout (a slow LLM), the child deadline fires BELOW the
// turn deadline, so RecapLastSessions returns the friendly took-too-long text as a
// tool RESULT (nil error) — the Butler can relay it — and the parent turn ctx is
// still alive (never killed by the recap's own budget).
func TestRecapAdapterTimeoutMapsFriendlyAndTurnSurvives(t *testing.T) {
	restore := recapToolTimeout
	recapToolTimeout = 20 * time.Millisecond
	defer func() { recapToolTimeout = restore }()

	// Parent turn ctx: alive far longer than the recap child budget.
	turnCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	a := NewRecap(blockingEngine{}, stubStore{}, stubSessions{})
	out, err := a.RecapLastSessions(turnCtx, 1)
	if err != nil {
		t.Fatalf("RecapLastSessions returned an error, want friendly result text: %v", err)
	}
	if out != recapTookTooLong {
		t.Errorf("out = %q, want the friendly took-too-long text", out)
	}
	if turnCtx.Err() != nil {
		t.Errorf("turn ctx died (%v); the recap budget must not kill the turn", turnCtx.Err())
	}
}
