package orchestrator_test

import (
	"context"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// TestFloor_ZeroValueUsable proves a bare Floor{} (no constructor) does not panic
// on its nil clock in Take — the clock defaults to time.Now. Guards the sharp
// edge code-quality flagged: the constructors set now, but the type must be
// usable zero-valued too.
func TestFloor_ZeroValueUsable(t *testing.T) {
	var f orchestrator.Floor // zero value, now == nil
	ctx, release, coalesced := f.Take(context.Background(), "")
	defer release()
	if coalesced {
		t.Fatal("a zero-value floor has no coalesce window; Take must not coalesce")
	}
	if ctx.Err() != nil {
		t.Fatalf("turn ctx must be live: %v", ctx.Err())
	}
	if !f.Active() {
		t.Fatal("floor must be active after Take on a zero-value floor")
	}
}

func TestFloor_TakeActiveReleaseInactive(t *testing.T) {
	f := orchestrator.NewFloor()
	if f.Active() {
		t.Fatal("a fresh floor must be inactive")
	}
	ctx, release, _ := f.Take(context.Background(), "")
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
	// The turn carries its TurnID in the parent ctx (as the production reply
	// reactor does) so Yield can attribute the barge to the cut turn.
	parent := voiceevent.WithTurnID(context.Background(), "T7")
	ctx, release, _ := f.Take(parent, "")
	defer release()

	turnID, yielded := f.Yield()
	if !yielded {
		t.Fatal("Yield must report true when a turn was held")
	}
	if turnID != "T7" {
		t.Fatalf("Yield returned turnID %q, want the held turn's T7 (barge attribution)", turnID)
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
	if turnID, yielded := f.Yield(); yielded || turnID != "" {
		t.Fatalf("Yield on a free floor must report (\"\", false), got (%q, %v)", turnID, yielded)
	}
}

func TestFloor_TakeSupersedesPreviousTurn(t *testing.T) {
	f := orchestrator.NewFloor()
	ctx1, release1, _ := f.Take(context.Background(), "")
	defer release1()

	ctx2, release2, coalesced := f.Take(context.Background(), "")
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

	ctx1, release1, c1 := f.Take(context.Background(), "bart")
	defer release1()
	if c1 {
		t.Fatal("the first Take must not coalesce (nothing is holding the floor)")
	}

	// Second segment arrives 100ms later — inside the 500ms window.
	now = now.Add(100 * time.Millisecond)
	ctx2, release2, c2 := f.Take(context.Background(), "bart")
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

// TestFloor_CoalesceWindowCrossTargetSupersedes pins #146: the coalesce window
// folds takes into the in-flight turn only when they address the SAME target
// agent. A take routed to a DIFFERENT agent inside the window is not "the same
// utterance continuing" — the matcher routed it elsewhere ("Bart, hold the
// door. Greta, run!") — so it must supersede the holder as a normal take, not
// be silently coalesced away.
func TestFloor_CoalesceWindowCrossTargetSupersedes(t *testing.T) {
	now := time.Unix(0, 0)
	f := orchestrator.NewFloorWithCoalesce(600 * time.Millisecond)
	f.SetClock(func() time.Time { return now })

	ctx1, release1, _ := f.Take(context.Background(), "bart")
	defer release1()

	// Greta's take lands 100ms later — inside the window, but for another agent.
	now = now.Add(100 * time.Millisecond)
	ctx2, release2, coalesced := f.Take(context.Background(), "greta")
	defer release2()

	if coalesced {
		t.Fatal("a cross-target take inside the coalesce window must supersede, not coalesce")
	}
	if ctx1.Err() == nil {
		t.Fatal("a cross-target take must cancel the prior holder's ctx (supersede)")
	}
	if ctx2.Err() != nil {
		t.Fatalf("the cross-target turn's ctx must be live so its reply is spoken: %v", ctx2.Err())
	}
	if !f.Active() {
		t.Fatal("the cross-target turn must hold the floor")
	}
}

// TestFloor_CoalesceWindowExpiresToSupersession proves the window is bounded: a
// re-Take after the window elapses is a genuine new utterance and supersedes the
// prior turn as normal.
func TestFloor_CoalesceWindowExpiresToSupersession(t *testing.T) {
	now := time.Unix(0, 0)
	f := orchestrator.NewFloorWithCoalesce(500 * time.Millisecond)
	f.SetClock(func() time.Time { return now })

	ctx1, release1, _ := f.Take(context.Background(), "bart")
	defer release1()

	// Real conversational gap: past the window.
	now = now.Add(800 * time.Millisecond)
	ctx2, release2, coalesced := f.Take(context.Background(), "bart")
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

	ctx1, release1, _ := f.Take(context.Background(), "bart")
	defer release1()

	// Segment 2 at +200ms (inside window of seg1), segment 3 at +400ms (outside
	// window of seg1 but inside window of seg2 — anchor refreshed).
	now = now.Add(200 * time.Millisecond)
	_, r2, c2 := f.Take(context.Background(), "bart")
	defer r2()
	now = now.Add(200 * time.Millisecond)
	_, r3, c3 := f.Take(context.Background(), "bart")
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
	ctx1, release1, _ := f.Take(context.Background(), "")
	defer release1()
	ctx2, release2, coalesced := f.Take(context.Background(), "")
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
	_, release1, _ := f.Take(context.Background(), "")
	_, release2, _ := f.Take(context.Background(), "")
	defer release2()

	// Releasing the first (already-superseded) turn must not wipe the second
	// turn's hold on the floor.
	release1()
	if !f.Active() {
		t.Fatal("a stale release must not clear the newer turn's floor")
	}
}

// TestFloor_YieldAgentCutsMatchingHolder pins the per-Agent mute cut (#211): a
// YieldAgent whose agentID matches the current holder's target cancels that turn
// and reports its TurnID — the same hard cut Yield does, but keyed to one Agent.
func TestFloor_YieldAgentCutsMatchingHolder(t *testing.T) {
	f := orchestrator.NewFloor()
	parent := voiceevent.WithTurnID(context.Background(), "Tm")
	ctx, release, _ := f.Take(parent, "bart")
	defer release()

	turnID, yielded := f.YieldAgent("bart")
	if !yielded {
		t.Fatal("YieldAgent must report true when the matching Agent holds the floor")
	}
	if turnID != "Tm" {
		t.Fatalf("YieldAgent returned turnID %q, want the held turn's Tm", turnID)
	}
	if ctx.Err() == nil {
		t.Fatal("YieldAgent must cancel the matching holder's turn ctx")
	}
	if f.Active() {
		t.Fatal("floor must be free after a matching YieldAgent")
	}
}

// TestFloor_YieldAgentIgnoresNonHolder proves a mute of an Agent that is NOT the
// current holder is a no-op: the speaking Agent keeps the floor (a muted
// addressee must never disturb whoever holds the floor, AC3).
func TestFloor_YieldAgentIgnoresNonHolder(t *testing.T) {
	f := orchestrator.NewFloor()
	parent := voiceevent.WithTurnID(context.Background(), "Tb")
	ctx, release, _ := f.Take(parent, "bart")
	defer release()

	turnID, yielded := f.YieldAgent("greta")
	if yielded || turnID != "" {
		t.Fatalf("YieldAgent for a non-holder must report (\"\", false), got (%q, %v)", turnID, yielded)
	}
	if ctx.Err() != nil {
		t.Fatal("YieldAgent for a non-holder must NOT cancel the current holder's turn")
	}
	if !f.Active() {
		t.Fatal("the current holder must keep the floor after a non-matching YieldAgent")
	}
}

// TestFloor_YieldAgentCutsHeldButSilentTurn pins AC2: muting the holder kills its
// turn even in the pre-audio (held-but-silent LLM "thinking") phase — YieldAgent
// deliberately ignores f.speaking, unlike the barge gate, so a just-muted Agent
// never starts speaking after the fact.
func TestFloor_YieldAgentCutsHeldButSilentTurn(t *testing.T) {
	f := orchestrator.NewFloor()
	parent := voiceevent.WithTurnID(context.Background(), "Ts")
	ctx, release, _ := f.Take(parent, "bart") // never marked speaking (no FirstOpus)
	defer release()

	turnID, yielded := f.YieldAgent("bart")
	if !yielded || turnID != "Ts" {
		t.Fatalf("YieldAgent must cut a held-but-silent turn: got (%q, %v)", turnID, yielded)
	}
	if ctx.Err() == nil {
		t.Fatal("YieldAgent must cancel a held-but-silent (pre-audio) holder — a mute kills the thinking turn too")
	}
}

// TestFloor_YieldAgentOnFreeFloorReportsFalse proves a mute with nothing speaking
// is a clean no-op.
func TestFloor_YieldAgentOnFreeFloorReportsFalse(t *testing.T) {
	f := orchestrator.NewFloor()
	if turnID, yielded := f.YieldAgent("bart"); yielded || turnID != "" {
		t.Fatalf("YieldAgent on a free floor must report (\"\", false), got (%q, %v)", turnID, yielded)
	}
}

// TestFloor_SetHolderAgentRetargetsMuteCut pins the Ensemble Lead election
// (#301): once the Lead is chosen, the floor is retargeted from the coalesce
// anchor (Targets[0]) onto the elected Lead, so a per-Agent mute cut
// ([Floor.YieldAgent], #211) — and the coalesce window — name the agent actually
// speaking. A stale turnID is a no-op (a late election for a superseded turn must
// not retarget the current holder).
func TestFloor_SetHolderAgentRetargetsMuteCut(t *testing.T) {
	f := orchestrator.NewFloor()
	parent := voiceevent.WithTurnID(context.Background(), "T-ens")
	ctx, release, _ := f.Take(parent, "bart") // taken under the coalesce anchor
	defer release()

	// A mute cut for the elected Lead does nothing until the floor is retargeted.
	if _, yielded := f.YieldAgent("mira"); yielded {
		t.Fatal("YieldAgent(mira) must be a no-op before the floor is retargeted onto mira")
	}

	// Stale turnID: must not retarget the live holder.
	f.SetHolderAgent("stale", "mira")
	if _, yielded := f.YieldAgent("mira"); yielded {
		t.Fatal("a stale-turnID SetHolderAgent must not retarget the current holder")
	}

	// Elect mira as Lead under the live turnID: now a mute cut for mira cuts it.
	f.SetHolderAgent("T-ens", "mira")
	turnID, yielded := f.YieldAgent("mira")
	if !yielded {
		t.Fatal("YieldAgent(mira) must cut the turn once the floor is retargeted onto mira")
	}
	if turnID != "T-ens" {
		t.Fatalf("YieldAgent returned turnID %q, want T-ens", turnID)
	}
	if ctx.Err() == nil {
		t.Fatal("the retargeted mute cut must cancel the turn ctx")
	}
}
