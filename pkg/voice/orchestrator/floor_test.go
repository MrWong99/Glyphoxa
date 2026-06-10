package orchestrator_test

import (
	"context"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
)

func TestFloor_TakeActiveReleaseInactive(t *testing.T) {
	f := orchestrator.NewFloor()
	if f.Active() {
		t.Fatal("a fresh floor must be inactive")
	}
	ctx, release, _ := f.Take(context.Background())
	if !f.Active() {
		t.Fatal("floor must be active after Take")
	}
	if ctx.Err() != nil {
		t.Fatalf("turn ctx must be live while held: %v", ctx.Err())
	}
	release()
	if f.Active() {
		t.Fatal("floor must be inactive after release")
	}
	if ctx.Err() == nil {
		t.Fatal("release must cancel the turn ctx")
	}
}

func TestFloor_YieldCancelsHeldTurnAndReportsTrue(t *testing.T) {
	f := orchestrator.NewFloor()
	ctx, release, _ := f.Take(context.Background())
	defer release()

	if !f.Yield() {
		t.Fatal("Yield must report true when a turn was held")
	}
	if ctx.Err() == nil {
		t.Fatal("Yield must cancel the held turn ctx")
	}
	if f.Active() {
		t.Fatal("floor must be free after Yield")
	}
}

func TestFloor_YieldOnFreeFloorReportsFalse(t *testing.T) {
	f := orchestrator.NewFloor()
	if f.Yield() {
		t.Fatal("Yield on a free floor must report false")
	}
}

func TestFloor_TakeSupersedesPreviousTurn(t *testing.T) {
	f := orchestrator.NewFloor()
	ctx1, release1, _ := f.Take(context.Background())
	defer release1()

	ctx2, release2, coalesced := f.Take(context.Background())
	defer release2()

	if coalesced {
		t.Fatal("a plain (no-coalesce) Take must never report coalesced")
	}
	if ctx1.Err() == nil {
		t.Fatal("a new Take must cancel the previous turn's ctx")
	}
	if ctx2.Err() != nil {
		t.Fatalf("the new turn's ctx must be live: %v", ctx2.Err())
	}
	if !f.Active() {
		t.Fatal("floor must remain held by the new turn")
	}
}

// TestFloor_CoalesceWindowKeepsInFlightTurn pins root cause #2's fix: a re-Take
// landing inside the coalesce window (one utterance VAD-split into two segments)
// must NOT cancel the in-flight turn. The late segment's context comes back
// already cancelled (its reply is suppressed) and the original turn keeps the
// floor.
func TestFloor_CoalesceWindowKeepsInFlightTurn(t *testing.T) {
	now := time.Unix(0, 0)
	f := orchestrator.NewFloorWithCoalesce(500 * time.Millisecond)
	f.SetClock(func() time.Time { return now })

	ctx1, release1, c1 := f.Take(context.Background())
	defer release1()
	if c1 {
		t.Fatal("the first Take must not coalesce (nothing is holding the floor)")
	}

	// Second segment arrives 100ms later — inside the 500ms window.
	now = now.Add(100 * time.Millisecond)
	ctx2, release2, c2 := f.Take(context.Background())
	defer release2()

	if !c2 {
		t.Fatal("a re-take inside the coalesce window must report coalesced=true so the caller can announce TurnYielded")
	}
	if ctx1.Err() != nil {
		t.Fatal("a re-take inside the coalesce window must NOT cancel the in-flight turn")
	}
	if ctx2.Err() == nil {
		t.Fatal("the coalesced (late-segment) turn's ctx must come back cancelled so its reply is suppressed")
	}
	if !f.Active() {
		t.Fatal("the original turn must still hold the floor after a coalesced re-take")
	}
	// The coalesced turn's release is a no-op on the floor: it must not free the
	// holder.
	release2()
	if !f.Active() {
		t.Fatal("a coalesced turn's release must not clear the in-flight turn's floor")
	}
	if ctx1.Err() != nil {
		t.Fatal("a coalesced turn's release must not cancel the in-flight turn")
	}
}

// TestFloor_CoalesceWindowExpiresToSupersession proves the window is bounded: a
// re-Take after the window elapses is a genuine new utterance and supersedes the
// prior turn as normal.
func TestFloor_CoalesceWindowExpiresToSupersession(t *testing.T) {
	now := time.Unix(0, 0)
	f := orchestrator.NewFloorWithCoalesce(500 * time.Millisecond)
	f.SetClock(func() time.Time { return now })

	ctx1, release1, _ := f.Take(context.Background())
	defer release1()

	// Real conversational gap: past the window.
	now = now.Add(800 * time.Millisecond)
	ctx2, release2, coalesced := f.Take(context.Background())
	defer release2()

	if coalesced {
		t.Fatal("a re-take past the coalesce window is a genuine new turn, not coalesced")
	}
	if ctx1.Err() == nil {
		t.Fatal("a re-take past the coalesce window must supersede the prior turn")
	}
	if ctx2.Err() != nil {
		t.Fatalf("the new turn's ctx must be live: %v", ctx2.Err())
	}
	if !f.Active() {
		t.Fatal("the new turn must hold the floor")
	}
}

// TestFloor_CoalesceChainKeepsCoalescing proves a run of closely-spaced splits
// all coalesce: each segment is within the window of the previous one (the anchor
// is refreshed), not just the first, so a 3-segment over-split still maps to one
// turn.
func TestFloor_CoalesceChainKeepsCoalescing(t *testing.T) {
	now := time.Unix(0, 0)
	f := orchestrator.NewFloorWithCoalesce(300 * time.Millisecond)
	f.SetClock(func() time.Time { return now })

	ctx1, release1, _ := f.Take(context.Background())
	defer release1()

	// Segment 2 at +200ms (inside window of seg1), segment 3 at +400ms (outside
	// window of seg1 but inside window of seg2 — anchor refreshed).
	now = now.Add(200 * time.Millisecond)
	_, r2, c2 := f.Take(context.Background())
	defer r2()
	now = now.Add(200 * time.Millisecond)
	_, r3, c3 := f.Take(context.Background())
	defer r3()

	if !c2 || !c3 {
		t.Fatalf("every segment in the rolling window must coalesce: seg2=%v seg3=%v", c2, c3)
	}
	if ctx1.Err() != nil {
		t.Fatal("a chain of splits inside the rolling window must all coalesce; the first turn must survive")
	}
	if !f.Active() {
		t.Fatal("the original turn must still hold the floor after a coalesced chain")
	}
}

// TestFloor_ZeroCoalesceIsPlainSupersession guards the default: NewFloor (and a
// zero coalesce window) keeps the always-supersede behaviour the barge path and
// the tracer-bullet tests depend on, even on a back-to-back re-take.
func TestFloor_ZeroCoalesceIsPlainSupersession(t *testing.T) {
	f := orchestrator.NewFloorWithCoalesce(0)
	ctx1, release1, _ := f.Take(context.Background())
	defer release1()
	ctx2, release2, coalesced := f.Take(context.Background())
	defer release2()

	if coalesced {
		t.Fatal("a zero coalesce window must never coalesce")
	}
	if ctx1.Err() == nil {
		t.Fatal("a zero coalesce window must supersede the prior turn (no debounce)")
	}
	if ctx2.Err() != nil {
		t.Fatalf("the new turn's ctx must be live: %v", ctx2.Err())
	}
}

func TestFloor_StaleReleaseDoesNotClearNewerTurn(t *testing.T) {
	f := orchestrator.NewFloor()
	_, release1, _ := f.Take(context.Background())
	_, release2, _ := f.Take(context.Background())
	defer release2()

	// Releasing the first (already-superseded) turn must not wipe the second
	// turn's hold on the floor.
	release1()
	if !f.Active() {
		t.Fatal("a stale release must not clear the newer turn's floor")
	}
}
