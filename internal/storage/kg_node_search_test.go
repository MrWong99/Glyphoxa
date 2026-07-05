//go:build integration

package storage_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// searchNames pulls the names out of a search result in rank order.
func searchNames(nodes []storage.KGNode) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.Name
	}
	return out
}

// TestSearchNodes_RanksScopesAndIncludesPrivate is #131 AC1 + AC4 (storage
// grain): a keyword query returns the Campaign's matching Nodes ranked by
// relevance (a name hit outranks a body hit via the A>B fts weights), a prefix
// term matches a longer word (typeahead), the search is Campaign-scoped (another
// Campaign's identical match never leaks), and gm_private Nodes ARE included
// (GM-facing search). An empty query yields no matches and no error.
func TestSearchNodes_RanksScopesAndIncludesPrivate(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	// Name hit vs body hit for the SAME term "dragon": the A-weighted name must
	// outrank the B-weighted body.
	nameHit, err := st.CreateNode(ctx, storage.NewKGNode{
		CampaignID: campaignID, Type: storage.KGNodeCharacter, Name: "Dragon of the North", Body: "A cold wyrm.",
	})
	if err != nil {
		t.Fatalf("CreateNode nameHit: %v", err)
	}
	bodyHit, err := st.CreateNode(ctx, storage.NewKGNode{
		CampaignID: campaignID, Type: storage.KGNodeNote, Name: "Traveler's rumor", Body: "They say a dragon sleeps nearby.",
	})
	if err != nil {
		t.Fatalf("CreateNode bodyHit: %v", err)
	}
	// A gm_private Node that also matches — must appear in GM-facing results. Its
	// match is in the BODY (weight B), so it never competes with the name hit for
	// the top rank slot; the assertion below is purely name-A vs body-B.
	secret, err := st.CreateNode(ctx, storage.NewKGNode{
		CampaignID: campaignID, Type: storage.KGNodePlotThread, Name: "The hidden pact", Body: "the dragon's true name is secret", GMPrivate: true,
	})
	if err != nil {
		t.Fatalf("CreateNode secret: %v", err)
	}
	// A non-matching Node stays out of results.
	if _, err := st.CreateNode(ctx, storage.NewKGNode{
		CampaignID: campaignID, Type: storage.KGNodeLocation, Name: "Quiet Harbor", Body: "Ships dock here.",
	}); err != nil {
		t.Fatalf("CreateNode harbor: %v", err)
	}

	// A second Campaign whose Node matches "dragon" identically — must not leak.
	var campaign2 uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO campaign (tenant_id, name, system, language)
		 VALUES ($1, 'Other', 'dnd5e', 'en') RETURNING id`, tenantID).Scan(&campaign2); err != nil {
		t.Fatalf("insert campaign2: %v", err)
	}
	leak, err := st.CreateNode(ctx, storage.NewKGNode{
		CampaignID: campaign2, Type: storage.KGNodeCharacter, Name: "Dragon of the East", Body: "warm wyrm",
	})
	if err != nil {
		t.Fatalf("CreateNode leak: %v", err)
	}

	got, err := st.SearchNodes(ctx, campaignID, "dragon", 50)
	if err != nil {
		t.Fatalf("SearchNodes: %v", err)
	}
	ids := make(map[uuid.UUID]bool, len(got))
	for _, n := range got {
		ids[n.ID] = true
	}
	if len(got) != 3 {
		t.Fatalf("got %d matches %v, want 3 (nameHit, bodyHit, private secret)", len(got), searchNames(got))
	}
	// AC1: name hit ranks above body hit (A > B weights).
	if got[0].ID != nameHit.ID {
		t.Errorf("rank[0] = %q, want the name hit %q (A weight beats B)", got[0].Name, nameHit.Name)
	}
	if !ids[bodyHit.ID] {
		t.Errorf("body hit %q missing from results %v", bodyHit.Name, searchNames(got))
	}
	// AC4: gm_private Node is present in GM-facing search.
	if !ids[secret.ID] {
		t.Errorf("gm_private match %q missing — GM-facing search must include it", secret.Name)
	}
	// Campaign scope: campaign2's identical match never leaks.
	if ids[leak.ID] {
		t.Errorf("cross-campaign leak: %q returned for campaign1", leak.Name)
	}

	// Typeahead: a prefix term matches a longer word ("brid" → "Bridge").
	bridge, err := st.CreateNode(ctx, storage.NewKGNode{
		CampaignID: campaignID, Type: storage.KGNodeLocation, Name: "Stone Bridge", Body: "spans the gorge",
	})
	if err != nil {
		t.Fatalf("CreateNode bridge: %v", err)
	}
	pref, err := st.SearchNodes(ctx, campaignID, "brid", 50)
	if err != nil {
		t.Fatalf("SearchNodes prefix: %v", err)
	}
	if len(pref) != 1 || pref[0].ID != bridge.ID {
		t.Errorf("prefix 'brid' = %v, want only %q (typeahead prefix match)", searchNames(pref), bridge.Name)
	}

	// An empty / all-punctuation query is a no-op: no matches, no error.
	for _, q := range []string{"", "   ", "!@#$"} {
		empty, err := st.SearchNodes(ctx, campaignID, q, 50)
		if err != nil {
			t.Errorf("SearchNodes(%q) err = %v, want nil", q, err)
		}
		if len(empty) != 0 {
			t.Errorf("SearchNodes(%q) = %v, want no matches", q, searchNames(empty))
		}
	}
}

// TestSearchNodes_UsesFtsIndex is #131 AC2: the fts match is served by the
// kg_node_fts_idx GIN index, not a substring scan. On a tiny table the planner
// prefers the campaign_id btree and applies fts as a mere filter, so the EXPLAIN
// isolates the fts predicate ALONE (no campaign scope) with enable_seqscan off:
// the GIN index is then the only path the planner can take for `fts @@ q`, and it
// must show — a substring (ILIKE) implementation could never produce this plan.
func TestSearchNodes_UsesFtsIndex(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	for _, name := range []string{"Stone Bridge", "Iron Bridge", "Old Well"} {
		if _, err := st.CreateNode(ctx, storage.NewKGNode{
			CampaignID: campaignID, Type: storage.KGNodeLocation, Name: name,
		}); err != nil {
			t.Fatalf("CreateNode %q: %v", name, err)
		}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// SET LOCAL only affects this transaction; it forces the index path to show.
	if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
		t.Fatalf("disable seqscan: %v", err)
	}
	rows, err := tx.Query(ctx,
		`EXPLAIN SELECT id FROM kg_node WHERE fts @@ to_tsquery('simple', 'brid:*')`)
	if err != nil {
		t.Fatalf("EXPLAIN: %v", err)
	}
	defer rows.Close()

	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan plan line: %v", err)
		}
		plan.WriteString(line)
		plan.WriteString("\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("plan rows: %v", err)
	}
	if !strings.Contains(plan.String(), "kg_node_fts_idx") {
		t.Errorf("query plan does not use the fts GIN index (substring scan?):\n%s", plan.String())
	}
}
