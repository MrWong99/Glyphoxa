package session_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestDirectAs_IdleReturnsNoActiveSession pins the active-session requirement
// (ADR-0059): a /direct with no live Voice Session is refused before any roster
// lookup, and no directive state appears anywhere.
func TestDirectAs_IdleReturnsNoActiveSession(t *testing.T) {
	mgr, _ := muteManager(t, newFakeStore())

	if err := mgr.DirectAs(context.Background(), uuid.New(), uuid.NewString(), "lie", 3); err != session.ErrNoActiveSession {
		t.Fatalf("DirectAs while idle = %v, want ErrNoActiveSession", err)
	}
	if got := mgr.Directive(context.Background(), uuid.NewString(), true); got != "" {
		t.Fatalf("idle Directive = %q, want empty", got)
	}
}

// TestDirectAs_ForeignAgentRejected pins the campaign-membership guard: an agent
// not in the active session's voiced roster is refused ErrAgentNotInCampaign.
func TestDirectAs_ForeignAgentRejected(t *testing.T) {
	store := newFakeStore()
	seedAgents(store, 1)
	mgr, _ := muteManager(t, store)
	tenantID, _ := startMuteSession(t, mgr)

	if err := mgr.DirectAs(context.Background(), tenantID, uuid.NewString(), "lie", 3); err != session.ErrAgentNotInCampaign {
		t.Fatalf("DirectAs with a foreign agent = %v, want ErrAgentNotInCampaign", err)
	}
}

// TestDirectAs_ButlerRejected pins the Butler exclusion (ADR-0059): the GM's own
// assistant is never a directive target — the same voiced-Characters-only
// chokepoint the mute set uses.
func TestDirectAs_ButlerRejected(t *testing.T) {
	store := newFakeStore()
	butler := storage.Agent{ID: uuid.New(), Role: storage.AgentRoleButler, Name: "Glyphoxa"}
	store.agents = []storage.Agent{butler}
	mgr, _ := muteManager(t, store)
	tenantID, _ := startMuteSession(t, mgr)

	if err := mgr.DirectAs(context.Background(), tenantID, butler.ID.String(), "lie", 3); err != session.ErrAgentNotInCampaign {
		t.Fatalf("DirectAs on the Butler = %v, want ErrAgentNotInCampaign", err)
	}
}

// TestDirective_TurnBudgetConsumedOnCommittedTurnsOnly pins the ADR-0059
// countdown semantics: a directive set for N turns is returned by N CONSUMING
// consults (each committed Agent turn) — the consult that spends the last turn
// still receives the text — and vanishes after; peeking consults (the
// speculative Draft/React paths) never burn budget.
func TestDirective_TurnBudgetConsumedOnCommittedTurnsOnly(t *testing.T) {
	store := newFakeStore()
	agents := seedAgents(store, 1)
	mgr, _ := muteManager(t, store)
	tenantID, _ := startMuteSession(t, mgr)
	ctx := context.Background()
	id := agents[0].ID.String()

	if err := mgr.DirectAs(ctx, tenantID, id, "Bart lies about the key.", 2); err != nil {
		t.Fatalf("DirectAs: %v", err)
	}
	// Peeks (speculative paths) return the text without consuming — any number.
	for i := 0; i < 3; i++ {
		if got := mgr.Directive(ctx, id, false); got != "Bart lies about the key." {
			t.Fatalf("peek %d = %q, want the directive", i, got)
		}
	}
	// Two committed turns ride the directive...
	for i := 0; i < 2; i++ {
		if got := mgr.Directive(ctx, id, true); got != "Bart lies about the key." {
			t.Fatalf("committed consult %d = %q, want the directive", i, got)
		}
	}
	// ...the third is clean.
	if got := mgr.Directive(ctx, id, true); got != "" {
		t.Fatalf("consult past the budget = %q, want empty", got)
	}
	if ids := mgr.DirectedAgentIDs(tenantID); len(ids) != 0 {
		t.Fatalf("DirectedAgentIDs after expiry = %v, want empty", ids)
	}
}

// TestDirective_StickyReplaceAndClear pins the sticky default (turns <= 0: until
// cleared/replaced/session end), replacement, the explicit clear, and the
// DirectedAgentIDs snapshot.
func TestDirective_StickyReplaceAndClear(t *testing.T) {
	store := newFakeStore()
	agents := seedAgents(store, 2)
	mgr, _ := muteManager(t, store)
	tenantID, _ := startMuteSession(t, mgr)
	ctx := context.Background()
	id := agents[0].ID.String()

	if err := mgr.DirectAs(ctx, tenantID, id, "Speak in riddles.", 0); err != nil {
		t.Fatalf("DirectAs sticky: %v", err)
	}
	for i := 0; i < 5; i++ {
		if got := mgr.Directive(ctx, id, true); got != "Speak in riddles." {
			t.Fatalf("sticky consult %d = %q, want the directive", i, got)
		}
	}
	if ids := mgr.DirectedAgentIDs(tenantID); len(ids) != 1 || ids[0] != id {
		t.Fatalf("DirectedAgentIDs = %v, want [%s]", ids, id)
	}

	// Replace: the new text wins immediately.
	if err := mgr.DirectAs(ctx, tenantID, id, "Drop the riddles.", 0); err != nil {
		t.Fatalf("DirectAs replace: %v", err)
	}
	if got := mgr.Directive(ctx, id, true); got != "Drop the riddles." {
		t.Fatalf("post-replace consult = %q, want the new directive", got)
	}

	// Clear: empty text removes it.
	if err := mgr.DirectAs(ctx, tenantID, id, "", 0); err != nil {
		t.Fatalf("DirectAs clear: %v", err)
	}
	if got := mgr.Directive(ctx, id, true); got != "" {
		t.Fatalf("post-clear consult = %q, want empty", got)
	}
	// The other agent was never directed.
	if got := mgr.Directive(ctx, agents[1].ID.String(), true); got != "" {
		t.Fatalf("undirected agent consult = %q, want empty", got)
	}
}

// TestDirective_DiesWithSession pins the volatility contract (ADR-0059, the mute
// precedent): a directive never survives its Voice Session.
func TestDirective_DiesWithSession(t *testing.T) {
	store := newFakeStore()
	agents := seedAgents(store, 1)
	mgr, _ := muteManager(t, store)
	tenantID, _ := startMuteSession(t, mgr)
	ctx := context.Background()
	id := agents[0].ID.String()

	if err := mgr.DirectAs(ctx, tenantID, id, "Sticky note.", 0); err != nil {
		t.Fatalf("DirectAs: %v", err)
	}
	if _, err := mgr.Stop(ctx, tenantID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := mgr.Directive(ctx, id, true); got != "" {
		t.Fatalf("post-stop Directive = %q, want empty (directives die with the session)", got)
	}
	if ids := mgr.DirectedAgentIDs(tenantID); ids != nil {
		t.Fatalf("post-stop DirectedAgentIDs = %v, want nil", ids)
	}
}
