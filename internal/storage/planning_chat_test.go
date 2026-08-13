//go:build integration

package storage_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestPlanningThreadLifecycle covers the #592 thread + message persistence:
// seq ordering, the updated_at-driven list order, rename, campaign scoping,
// and the thread-delete cascade taking its messages.
func TestPlanningThreadLifecycle(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	first, err := st.CreatePlanningThread(ctx, campaignID, "")
	if err != nil {
		t.Fatalf("CreatePlanningThread: %v", err)
	}
	second, err := st.CreatePlanningThread(ctx, campaignID, "Named thread")
	if err != nil {
		t.Fatalf("CreatePlanningThread (named): %v", err)
	}

	// Messages append at seq 1, 2, 3… and read back in order.
	for i, content := range []string{"first question", "first answer", "second question"} {
		role := storage.PlanningRoleUser
		if i == 1 {
			role = storage.PlanningRoleAssistant
		}
		if _, err := st.AppendPlanningMessage(ctx, campaignID, first.ID, role, content); err != nil {
			t.Fatalf("AppendPlanningMessage %d: %v", i, err)
		}
	}
	msgs, err := st.ListPlanningMessages(ctx, campaignID, first.ID)
	if err != nil {
		t.Fatalf("ListPlanningMessages: %v", err)
	}
	if len(msgs) != 3 || msgs[0].Seq != 1 || msgs[2].Seq != 3 || msgs[2].Content != "second question" {
		t.Fatalf("messages = %+v", msgs)
	}

	// The append bumped first's updated_at, so it now leads the list.
	threads, err := st.ListPlanningThreads(ctx, campaignID)
	if err != nil {
		t.Fatalf("ListPlanningThreads: %v", err)
	}
	if len(threads) != 2 || threads[0].ID != first.ID {
		t.Fatalf("thread order = %+v, want the appended-to thread first", threads)
	}

	// Rename round-trips.
	renamed, err := st.RenamePlanningThread(ctx, campaignID, first.ID, "Cult planning")
	if err != nil || renamed.Title != "Cult planning" {
		t.Fatalf("RenamePlanningThread: %v (%+v)", err, renamed)
	}

	// Cross-campaign scoping: everything answers ErrNotFound for a foreign
	// campaign id, and an append to a foreign (campaign, thread) pair is
	// structurally refused by the composite FK.
	otherCampaign := uuid.New()
	if _, err := st.GetPlanningThread(ctx, otherCampaign, first.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("cross-campaign Get = %v, want ErrNotFound", err)
	}
	if _, err := st.AppendPlanningMessage(ctx, otherCampaign, first.ID, storage.PlanningRoleUser, "x"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("cross-campaign Append = %v, want ErrNotFound", err)
	}
	if err := st.DeletePlanningThread(ctx, otherCampaign, first.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("cross-campaign Delete = %v, want ErrNotFound", err)
	}

	// Deleting a thread cascades its messages.
	if err := st.DeletePlanningThread(ctx, campaignID, first.ID); err != nil {
		t.Fatalf("DeletePlanningThread: %v", err)
	}
	var orphans int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM planning_message WHERE thread_id = $1`, first.ID).Scan(&orphans); err != nil {
		t.Fatalf("count orphans: %v", err)
	}
	if orphans != 0 {
		t.Errorf("thread delete left %d orphaned messages", orphans)
	}
	_ = second
}

// TestButlerChatGrantsSeeded pins the 00053 trigger + backfill: a campaign's
// auto-created Butler carries the ADR-0062 chat tool belt as SEPARATE rows
// beside the voice defaults, with propose_knowledge's campaign-wide scope.
func TestButlerChatGrantsSeeded(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	butler, err := st.GetButler(ctx, campaignID)
	if err != nil {
		t.Fatalf("GetButler: %v", err)
	}

	chat, err := st.ListToolGrantsFor(ctx, butler.ID, storage.GrantSurfaceChat)
	if err != nil {
		t.Fatalf("ListToolGrantsFor(chat): %v", err)
	}
	wantChat := []string{"appearances_of", "find_node", "propose_knowledge", "recap", "search_transcripts"}
	if len(chat) != len(wantChat) {
		t.Fatalf("chat grants = %d rows (%+v), want %d", len(chat), chat, len(wantChat))
	}
	for i, g := range chat {
		if g.ToolName != wantChat[i] {
			t.Errorf("chat grant %d = %q, want %q", i, g.ToolName, wantChat[i])
		}
		if g.Surface != storage.GrantSurfaceChat {
			t.Errorf("chat grant %q surface = %q", g.ToolName, g.Surface)
		}
		if g.ToolName == "propose_knowledge" {
			var scope struct {
				Scope string `json:"scope"`
			}
			if err := json.Unmarshal(g.Config, &scope); err != nil || scope.Scope != "campaign" {
				t.Errorf("propose_knowledge config = %s (%v), want campaign scope", g.Config, err)
			}
		}
	}

	// The voice defaults are untouched, and the two surfaces are disjoint: the
	// shared 'recap' name exists once per surface.
	voice, err := st.ListToolGrants(ctx, butler.ID)
	if err != nil {
		t.Fatalf("ListToolGrants(voice): %v", err)
	}
	wantVoice := []string{"dice", "kg_query", "recap", "transcript_search"}
	if len(voice) != len(wantVoice) {
		t.Fatalf("voice grants = %+v, want %v", voice, wantVoice)
	}
	for i, g := range voice {
		if g.ToolName != wantVoice[i] || g.Surface != storage.GrantSurfaceVoice {
			t.Errorf("voice grant %d = %q/%q, want %q/voice", i, g.ToolName, g.Surface, wantVoice[i])
		}
	}

	// Revoking the VOICE recap leaves the chat recap standing (the #592
	// separate-rows requirement, ADR-0029).
	if err := st.DeleteToolGrant(ctx, butler.ID, "recap"); err != nil {
		t.Fatalf("DeleteToolGrant(voice recap): %v", err)
	}
	chatAfter, err := st.ListToolGrantsFor(ctx, butler.ID, storage.GrantSurfaceChat)
	if err != nil {
		t.Fatalf("ListToolGrantsFor(chat) after revoke: %v", err)
	}
	if len(chatAfter) != len(wantChat) {
		t.Errorf("voice revoke also removed chat rows: %+v", chatAfter)
	}
}

// TestPlanningThreadsCampaignCascade pins the zero-code sweep: deleting the
// campaign row removes its planning threads and messages via the FK chain.
func TestPlanningThreadsCampaignCascade(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	thread, err := st.CreatePlanningThread(ctx, campaignID, "doomed")
	if err != nil {
		t.Fatalf("CreatePlanningThread: %v", err)
	}
	if _, err := st.AppendPlanningMessage(ctx, campaignID, thread.ID, storage.PlanningRoleUser, "gone with the campaign"); err != nil {
		t.Fatalf("AppendPlanningMessage: %v", err)
	}

	if _, err := st.ArchiveCampaign(ctx, tenantID, campaignID); err != nil {
		t.Fatalf("ArchiveCampaign: %v", err)
	}
	if err := st.DeleteCampaign(ctx, tenantID, campaignID); err != nil {
		t.Fatalf("DeleteCampaign: %v", err)
	}

	var threads, messages int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM planning_thread WHERE campaign_id = $1`, campaignID).Scan(&threads); err != nil {
		t.Fatalf("count threads: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM planning_message WHERE campaign_id = $1`, campaignID).Scan(&messages); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if threads != 0 || messages != 0 {
		t.Errorf("campaign delete left %d threads / %d messages", threads, messages)
	}
}
