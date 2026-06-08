package orchestrator_test

import (
	"context"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
)

func TestFloor_TakeActiveReleaseInactive(t *testing.T) {
	f := orchestrator.NewFloor()
	if f.Active() {
		t.Fatal("a fresh floor must be inactive")
	}
	ctx, release := f.Take(context.Background())
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
	ctx, release := f.Take(context.Background())
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
	ctx1, release1 := f.Take(context.Background())
	defer release1()

	ctx2, release2 := f.Take(context.Background())
	defer release2()

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

func TestFloor_StaleReleaseDoesNotClearNewerTurn(t *testing.T) {
	f := orchestrator.NewFloor()
	_, release1 := f.Take(context.Background())
	_, release2 := f.Take(context.Background())
	defer release2()

	// Releasing the first (already-superseded) turn must not wipe the second
	// turn's hold on the floor.
	release1()
	if !f.Active() {
		t.Fatal("a stale release must not clear the newer turn's floor")
	}
}
