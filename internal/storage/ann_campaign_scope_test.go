//go:build integration

package storage_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// The similarity searches are scoped to ONE Campaign, but the HNSW indexes span
// every Campaign of every Tenant. An approximate index scan walks the GLOBAL
// nearest neighbours (hnsw.ef_search = 40 candidates by default) and only then
// applies the campaign filter, so once more than ~40 rows of OTHER campaigns sit
// nearer the query than this campaign's rows, the filter discards every
// candidate and the search comes back empty: memory recall and the proposal
// similarity hint silently die for small campaigns on a busy deployment.
//
// These tests build exactly that shape: a target campaign whose rows are all
// FARTHER from the query than a crowd of another campaign's rows.

const (
	annTargetRows = 200  // rows in the searched campaign, all far from the query
	annCrowdRows  = 3000 // rows in another campaign, all nearer the query
	annK          = 8
)

// annOtherCampaign creates a second Campaign under tenantID.
func annOtherCampaign(t *testing.T, pool *pgxpool.Pool, tenantID uuid.UUID) uuid.UUID {
	t.Helper()
	var other uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO campaign (tenant_id, name) VALUES ($1, 'Crowd') RETURNING id`, tenantID).Scan(&other); err != nil {
		t.Fatalf("insert crowd campaign: %v", err)
	}
	return other
}

// annIndexPreferringStore returns a Store whose connections make the planner
// PREFER the ordered HNSW index scan (sorting is priced out). On a large
// multi-tenant table the planner picks that plan on cost alone; a CI-sized table
// would need hundreds of thousands of 768-dim rows to get there, so the test
// pins the planner's hand instead. A correct search must return the campaign's
// rows whatever plan the planner would like best.
func annIndexPreferringStore(t *testing.T, dsn string) *storage.Store {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["enable_sort"] = "off"
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("pgxpool.NewWithConfig: %v", err)
	}
	t.Cleanup(pool.Close)
	return storage.New(pool)
}

func TestSearchChunksByCampaign_SurvivesCrowdOfNearerForeignRows(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := annIndexPreferringStore(t, dsn)
	crowd := annOtherCampaign(t, pool, tenantID)

	// The crowd: almost parallel to the query (distance ~0).
	if _, err := pool.Exec(ctx,
		`INSERT INTO transcript_chunk (campaign_id, content, started_at, embedding, embedding_model)
		 SELECT $1, 'crowd ' || g, now(), ('[1,' || (g * 0.00001)::text || repeat(',0', 766) || ']')::vector, 'm'
		   FROM generate_series(1, $2::int) g`, crowd, annCrowdRows); err != nil {
		t.Fatalf("seed crowd chunks: %v", err)
	}
	// The target campaign: ~45° off the query and beyond (distance >= ~0.29),
	// nearest row first by g.
	if _, err := pool.Exec(ctx,
		`INSERT INTO transcript_chunk (campaign_id, content, started_at, embedding, embedding_model)
		 SELECT $1, 'target ' || g, now(), ('[1,' || (1 + g * 0.01)::text || repeat(',0', 766) || ']')::vector, 'm'
		   FROM generate_series(1, $2::int) g`, campaignID, annTargetRows); err != nil {
		t.Fatalf("seed target chunks: %v", err)
	}
	if _, err := pool.Exec(ctx, `ANALYZE transcript_chunk`); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	got, err := st.SearchChunksByCampaign(ctx, campaignID, vec768(1), annK)
	if err != nil {
		t.Fatalf("SearchChunksByCampaign: %v", err)
	}
	if len(got) != annK {
		t.Fatalf("got %d chunks, want %d: a crowd of nearer rows in ANOTHER campaign must not starve this campaign's search", len(got), annK)
	}
	for i, m := range got {
		if m.Chunk.CampaignID != campaignID {
			t.Errorf("match %d belongs to campaign %s, want %s", i, m.Chunk.CampaignID, campaignID)
		}
		if i > 0 && m.Distance < got[i-1].Distance {
			t.Errorf("match %d distance %v < previous %v, want nearest first", i, m.Distance, got[i-1].Distance)
		}
	}
	// Exactness: the campaign's true nearest row is "target 1".
	if got[0].Chunk.Content != "target 1" {
		t.Errorf("nearest = %q, want %q (the campaign's exact nearest row)", got[0].Chunk.Content, "target 1")
	}
}

func TestSimilarNodes_SurvivesCrowdOfNearerForeignRows(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := annIndexPreferringStore(t, dsn)
	crowd := annOtherCampaign(t, pool, tenantID)

	if _, err := pool.Exec(ctx,
		`INSERT INTO kg_node (campaign_id, node_type, name, embedding, embedding_model)
		 SELECT $1, 'note', 'crowd ' || g, ('[1,' || (g * 0.00001)::text || repeat(',0', 766) || ']')::vector, 'm'
		   FROM generate_series(1, $2::int) g`, crowd, annCrowdRows); err != nil {
		t.Fatalf("seed crowd nodes: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO kg_node (campaign_id, node_type, name, embedding, embedding_model)
		 SELECT $1, 'note', 'target ' || g, ('[1,' || (1 + g * 0.01)::text || repeat(',0', 766) || ']')::vector, 'm'
		   FROM generate_series(1, $2::int) g`, campaignID, annTargetRows); err != nil {
		t.Fatalf("seed target nodes: %v", err)
	}
	if _, err := pool.Exec(ctx, `ANALYZE kg_node`); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	got, err := st.SimilarNodes(ctx, campaignID, vec768(1), annK)
	if err != nil {
		t.Fatalf("SimilarNodes: %v", err)
	}
	if len(got) != annK {
		t.Fatalf("got %d nodes, want %d: a crowd of nearer rows in ANOTHER campaign must not starve this campaign's search", len(got), annK)
	}
	for i, n := range got {
		if n.CampaignID != campaignID {
			t.Errorf("node %d belongs to campaign %s, want %s", i, n.CampaignID, campaignID)
		}
	}
	if got[0].Name != "target 1" {
		t.Errorf("nearest = %q, want %q (the campaign's exact nearest node)", got[0].Name, "target 1")
	}
}
