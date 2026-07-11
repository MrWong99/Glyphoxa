//go:build integration

package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestArchiveCampaign proves the archive write against a real Postgres (#269):
// archived_at is stamped ~now, every operator's durable selection pointing at the
// campaign is cleared (the "archived durable selection is treated as absent"
// decision, #265), and a re-archive keeps the ORIGINAL timestamp (COALESCE
// idempotence — the audit trail of when it was first archived).
func TestArchiveCampaign(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	// An operator has this campaign as their durable /glyphoxa use selection.
	if err := st.SetActiveCampaign(ctx, gmSnowflake, campaignID); err != nil {
		t.Fatalf("SetActiveCampaign: %v", err)
	}

	before := time.Now()
	archived, err := st.ArchiveCampaign(ctx, campaignID)
	if err != nil {
		t.Fatalf("ArchiveCampaign: %v", err)
	}
	if archived.ArchivedAt == nil {
		t.Fatalf("archived_at not set after archive")
	}
	if archived.ArchivedAt.Before(before.Add(-time.Minute)) || archived.ArchivedAt.After(time.Now().Add(time.Minute)) {
		t.Errorf("archived_at = %v, want ~now", archived.ArchivedAt)
	}

	// The durable selection pointing at the archived campaign was cleared.
	if _, err := st.GetActiveCampaignForUser(ctx, gmSnowflake); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("durable selection after archive = %v, want ErrNotFound (cleared)", err)
	}

	// Re-archiving keeps the original timestamp (COALESCE idempotence).
	firstTS := *archived.ArchivedAt
	rearchived, err := st.ArchiveCampaign(ctx, campaignID)
	if err != nil {
		t.Fatalf("ArchiveCampaign (re-archive): %v", err)
	}
	if rearchived.ArchivedAt == nil || !rearchived.ArchivedAt.Equal(firstTS) {
		t.Errorf("re-archive timestamp = %v, want the original %v (idempotent)", rearchived.ArchivedAt, firstTS)
	}

	// An unknown id is ErrNotFound.
	if _, err := st.ArchiveCampaign(ctx, uuid.New()); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("ArchiveCampaign(random) = %v, want ErrNotFound", err)
	}
}

// TestArchivedExcludedFromResolution pins the exclusion decisions (#265): an
// archived campaign drops out of ListCampaigns (and therefore the /glyphoxa use
// autocomplete, which shares the query), stays in ListAllCampaigns, and is skipped
// by the GetActiveCampaign most-recent fallback — so an only-archived DB resolves
// to ErrNotFound.
func TestArchivedExcludedFromResolution(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, first := seedCampaign(t, dsn) // "Lost Mine"
	ctx := context.Background()
	st := storage.New(pool)

	second := insertCampaign(t, st, tenantID, "Alpha Quest")

	// Archive the most-recently-created one; it must leave the default list but
	// stay in the archive-inclusive list.
	if _, err := st.ArchiveCampaign(ctx, second); err != nil {
		t.Fatalf("ArchiveCampaign: %v", err)
	}

	active, err := st.ListCampaigns(ctx)
	if err != nil {
		t.Fatalf("ListCampaigns: %v", err)
	}
	if len(active) != 1 || active[0].ID != first {
		t.Errorf("ListCampaigns = %+v, want only the active campaign %s", active, first)
	}

	all, err := st.ListAllCampaigns(ctx)
	if err != nil {
		t.Fatalf("ListAllCampaigns: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("ListAllCampaigns len = %d, want 2 (active + archived)", len(all))
	}

	// The most-recent fallback skips the archived campaign → resolves the active one.
	def, err := st.GetActiveCampaign(ctx)
	if err != nil || def.ID != first {
		t.Errorf("GetActiveCampaign = %s, %v; want the active campaign %s (archived skipped)", def.ID, err, first)
	}

	// Archive the last active campaign too → nothing resolves.
	if _, err := st.ArchiveCampaign(ctx, first); err != nil {
		t.Fatalf("ArchiveCampaign(first): %v", err)
	}
	if _, err := st.GetActiveCampaign(ctx); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("GetActiveCampaign with only archived = %v, want ErrNotFound", err)
	}
}

// TestGetActiveCampaignForUserNeverReturnsArchived proves the durable selection
// read excludes an archived target (#269): even a still-set pointer at an archived
// campaign resolves to ErrNotFound so the caller falls to the fallback.
func TestGetActiveCampaignForUserNeverReturnsArchived(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	if err := st.SetActiveCampaign(ctx, gmSnowflake, campaignID); err != nil {
		t.Fatalf("SetActiveCampaign: %v", err)
	}
	// Archive directly via SQL so the pointer is NOT cleared (isolating the read's
	// own archived filter from ArchiveCampaign's pointer-clearing).
	if _, err := pool.Exec(ctx, `UPDATE campaign SET archived_at = now() WHERE id = $1`, campaignID); err != nil {
		t.Fatalf("archive via SQL: %v", err)
	}
	if _, err := st.GetActiveCampaignForUser(ctx, gmSnowflake); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("GetActiveCampaignForUser with archived target = %v, want ErrNotFound", err)
	}
}

// TestUnarchiveCampaign proves the reverse: clearing archived_at returns the
// campaign to ListCampaigns (#269).
func TestUnarchiveCampaign(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	if _, err := st.ArchiveCampaign(ctx, campaignID); err != nil {
		t.Fatalf("ArchiveCampaign: %v", err)
	}
	if got, _ := st.ListCampaigns(ctx); len(got) != 0 {
		t.Fatalf("after archive, ListCampaigns len = %d, want 0", len(got))
	}

	un, err := st.UnarchiveCampaign(ctx, campaignID)
	if err != nil {
		t.Fatalf("UnarchiveCampaign: %v", err)
	}
	if un.ArchivedAt != nil {
		t.Errorf("archived_at = %v after unarchive, want nil", un.ArchivedAt)
	}
	got, err := st.ListCampaigns(ctx)
	if err != nil || len(got) != 1 || got[0].ID != campaignID {
		t.Errorf("after unarchive, ListCampaigns = %+v, %v; want the reactivated campaign", got, err)
	}

	// An unknown id is ErrNotFound.
	if _, err := st.UnarchiveCampaign(ctx, uuid.New()); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("UnarchiveCampaign(random) = %v, want ErrNotFound", err)
	}
}

// TestDeleteCampaignCascade proves the hard delete of an archived campaign cascades
// through the entire owned graph (#269): campaign + Agents + Tool Grants + KG
// Nodes/Edges + Voice Sessions + Transcript Lines + Transcript Chunks all vanish in
// one DELETE. A non-archived campaign is refused with ErrNotArchived (its row
// survives); an unknown id is ErrNotFound.
func TestDeleteCampaignCascade(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	// Seed one of every child so the cascade chain is proven, not assumed.
	// Butler is auto-created by the trigger; add a Character NPC + a Tool Grant.
	var charID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO agents (campaign_id, agent_role, name, address_only)
		 VALUES ($1, 'character', 'Bart', false) RETURNING id`, campaignID).Scan(&charID); err != nil {
		t.Fatalf("insert character: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO tool_agent_grant (agent_id, tool_name) VALUES ($1, 'dice')`, charID); err != nil {
		t.Fatalf("insert tool grant: %v", err)
	}

	// KG: two nodes + an edge between them.
	var nodeA, nodeB uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO kg_node (campaign_id, node_type, name) VALUES ($1, 'npc', 'Goblin') RETURNING id`,
		campaignID).Scan(&nodeA); err != nil {
		t.Fatalf("insert node A: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO kg_node (campaign_id, node_type, name) VALUES ($1, 'location', 'Cave') RETURNING id`,
		campaignID).Scan(&nodeB); err != nil {
		t.Fatalf("insert node B: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO kg_edge (campaign_id, from_node_id, to_node_id, edge_type)
		 VALUES ($1, $2, $3, 'resides_in')`, campaignID, nodeA, nodeB); err != nil {
		t.Fatalf("insert edge: %v", err)
	}

	// Voice Session + a Transcript Line under it.
	var sessionID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO voice_sessions (campaign_id, status) VALUES ($1, 'ended') RETURNING id`,
		campaignID).Scan(&sessionID); err != nil {
		t.Fatalf("insert voice session: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO transcript_line (voice_session_id, campaign_id, line_id, seq, who, kind, ts, text)
		 VALUES ($1, $2, 'u:1', 1, 'Player', 'player', now(), 'hello there')`,
		sessionID, campaignID); err != nil {
		t.Fatalf("insert transcript line: %v", err)
	}

	// Transcript Chunk (campaign-scoped).
	if _, err := pool.Exec(ctx,
		`INSERT INTO transcript_chunk (campaign_id, voice_session_id, content)
		 VALUES ($1, $2, 'chunk content')`, campaignID, sessionID); err != nil {
		t.Fatalf("insert transcript chunk: %v", err)
	}

	// A Session Highlight under the same session (#308): its voice_session_id FK is
	// ON DELETE RESTRICT, but campaign_id CASCADEs, so the campaign hard-delete removes
	// the highlight row BEFORE the cascaded voice_sessions delete — proving RESTRICT is
	// inert on the campaign-delete path (it only blocks a direct session delete).
	seedHighlight(t, st, tenantID, sessionID, campaignID, storage.HighlightCandidate)

	// Delete before archiving is refused; the row survives.
	if err := st.DeleteCampaign(ctx, campaignID); !errors.Is(err, storage.ErrNotArchived) {
		t.Fatalf("DeleteCampaign(non-archived) = %v, want ErrNotArchived", err)
	}
	if _, err := st.GetCampaign(ctx, campaignID); err != nil {
		t.Fatalf("campaign should survive a refused delete: %v", err)
	}

	// Archive, then hard-delete for real.
	if _, err := st.ArchiveCampaign(ctx, campaignID); err != nil {
		t.Fatalf("ArchiveCampaign: %v", err)
	}
	if err := st.DeleteCampaign(ctx, campaignID); err != nil {
		t.Fatalf("DeleteCampaign(archived): %v", err)
	}

	// Every child is gone via the FK cascade chain.
	for _, tc := range []struct {
		name  string
		query string
		arg   any
	}{
		{"campaign", `SELECT count(*) FROM campaign WHERE id = $1`, campaignID},
		{"agents", `SELECT count(*) FROM agents WHERE campaign_id = $1`, campaignID},
		{"tool_agent_grant", `SELECT count(*) FROM tool_agent_grant WHERE agent_id = $1`, charID},
		{"kg_node", `SELECT count(*) FROM kg_node WHERE campaign_id = $1`, campaignID},
		{"kg_edge", `SELECT count(*) FROM kg_edge WHERE campaign_id = $1`, campaignID},
		{"voice_sessions", `SELECT count(*) FROM voice_sessions WHERE campaign_id = $1`, campaignID},
		{"transcript_line", `SELECT count(*) FROM transcript_line WHERE campaign_id = $1`, campaignID},
		{"transcript_chunk", `SELECT count(*) FROM transcript_chunk WHERE campaign_id = $1`, campaignID},
		{"highlight", `SELECT count(*) FROM highlight WHERE campaign_id = $1`, campaignID},
	} {
		var n int
		if err := pool.QueryRow(ctx, tc.query, tc.arg).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", tc.name, err)
		}
		if n != 0 {
			t.Errorf("%s rows after cascade delete = %d, want 0", tc.name, n)
		}
	}
	_ = tenantID

	// An unknown id is ErrNotFound.
	if err := st.DeleteCampaign(ctx, uuid.New()); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("DeleteCampaign(random) = %v, want ErrNotFound", err)
	}
}

// TestDeleteCampaignWithJob_Atomic proves the delete + follow-up job enqueue are one
// transaction (#308): a committed delete leaves exactly one pending job carrying the
// payload, and a REFUSED delete (not archived) enqueues NO job — no orphan sweep of a
// surviving campaign.
func TestDeleteCampaignWithJob_Atomic(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	const kind = "highlight.sweep_campaign_clips"
	payload := []byte(`{"clip_keys":["k1","k2"]}`)

	countJobs := func() int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM job WHERE kind = $1`, kind).Scan(&n); err != nil {
			t.Fatalf("count jobs: %v", err)
		}
		return n
	}

	// Not archived → refused, and NO job enqueued (the tx rolled back).
	if err := st.DeleteCampaignWithJob(ctx, campaignID, kind, payload); !errors.Is(err, storage.ErrNotArchived) {
		t.Fatalf("DeleteCampaignWithJob(non-archived) = %v, want ErrNotArchived", err)
	}
	if n := countJobs(); n != 0 {
		t.Fatalf("refused delete enqueued %d jobs, want 0", n)
	}

	// Archive, then delete-with-job for real → campaign gone AND one job enqueued.
	if _, err := st.ArchiveCampaign(ctx, campaignID); err != nil {
		t.Fatalf("ArchiveCampaign: %v", err)
	}
	if err := st.DeleteCampaignWithJob(ctx, campaignID, kind, payload); err != nil {
		t.Fatalf("DeleteCampaignWithJob(archived): %v", err)
	}
	if _, err := st.GetCampaign(ctx, campaignID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("campaign should be gone: %v", err)
	}
	if n := countJobs(); n != 1 {
		t.Fatalf("committed delete enqueued %d jobs, want 1", n)
	}
	var gotPayload []byte
	if err := pool.QueryRow(ctx, `SELECT payload FROM job WHERE kind = $1`, kind).Scan(&gotPayload); err != nil {
		t.Fatalf("read job payload: %v", err)
	}
	if string(gotPayload) != `{"clip_keys": ["k1", "k2"]}` && string(gotPayload) != string(payload) {
		t.Fatalf("job payload = %s, want the carried clip keys", gotPayload)
	}
}
