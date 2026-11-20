//go:build integration

package storage_test

import (
	"context"
	"strings"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestUnembeddedBacklogQueriesAreIndexable pins migration 00057: the backfill
// worker's ordered backlog read and the backlog gauges' count are both served by
// the partial `(created_at, id) WHERE embedding IS NULL` indexes. The plans are
// read with sequential scans priced out, which proves the predicate and the sort
// order of each query MATCH its index (a mismatched index would still fall back
// to a sequential scan + sort) without needing a table big enough for the
// planner to prefer the index on cost.
func TestUnembeddedBacklogQueriesAreIndexable(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, _ := seedCampaign(t, dsn)
	ctx := context.Background()

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SET enable_seqscan = off`); err != nil {
		t.Fatalf("set enable_seqscan: %v", err)
	}

	for _, tc := range []struct {
		name, query, index string
		noSort             bool
	}{
		{"chunk list", `SELECT id FROM transcript_chunk WHERE embedding IS NULL ORDER BY created_at, id LIMIT 16`, "transcript_chunk_unembedded_idx", true},
		{"chunk count", `SELECT count(*) FROM transcript_chunk WHERE embedding IS NULL`, "transcript_chunk_unembedded_idx", false},
		{"node list", `SELECT id FROM kg_node WHERE embedding IS NULL ORDER BY created_at, id LIMIT 16`, "kg_node_unembedded_idx", true},
		{"node count", `SELECT count(*) FROM kg_node WHERE embedding IS NULL`, "kg_node_unembedded_idx", false},
	} {
		rows, err := conn.Query(ctx, `EXPLAIN `+tc.query)
		if err != nil {
			t.Fatalf("%s: explain: %v", tc.name, err)
		}
		var plan strings.Builder
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatalf("%s: scan plan: %v", tc.name, err)
			}
			plan.WriteString(line + "\n")
		}
		rows.Close()
		if !strings.Contains(plan.String(), tc.index) {
			t.Errorf("%s: plan does not use %s:\n%s", tc.name, tc.index, plan.String())
		}
		if tc.noSort && strings.Contains(plan.String(), "Sort") {
			t.Errorf("%s: plan still sorts, want the index to supply the (created_at, id) order:\n%s", tc.name, plan.String())
		}
	}
}

// TestMigration00057_AppliesAfter00056And00058 is the order production sees:
// 00056 and 00058 are already applied when 00057 arrives. It must apply cleanly
// out of order, leaving the backlog indexes in place and the global HNSW indexes
// gone.
func TestMigration00057_AppliesAfter00056And00058(t *testing.T) {
	dsn := startPostgres(t)
	db := openSQL(t, dsn)
	ctx := context.Background()

	provider, err := storage.NewMigrationProvider(db)
	if err != nil {
		t.Fatalf("NewMigrationProvider: %v", err)
	}
	if _, err := provider.UpTo(ctx, 56); err != nil {
		t.Fatalf("migrate up to 00056: %v", err)
	}
	if _, err := provider.ApplyVersion(ctx, 58, true); err != nil {
		t.Fatalf("apply 00058 ahead of 00057: %v", err)
	}
	if err := storage.EnsureCurrent(ctx, db); err == nil || !strings.Contains(err.Error(), "57") {
		t.Fatalf("EnsureCurrent = %v, want it to name the unapplied 57", err)
	}
	if err := storage.MigrateUp(ctx, db); err != nil {
		t.Fatalf("MigrateUp with 00057 pending behind 00058: %v", err)
	}
	if err := storage.EnsureCurrent(ctx, db); err != nil {
		t.Fatalf("EnsureCurrent after MigrateUp: %v", err)
	}

	for index, want := range map[string]bool{
		"transcript_chunk_unembedded_idx":     true,
		"kg_node_unembedded_idx":              true,
		"transcript_chunk_embedding_hnsw_idx": false,
		"kg_node_embedding_hnsw_idx":          false,
	} {
		var exists bool
		if err := db.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = $1)`, index).Scan(&exists); err != nil {
			t.Fatalf("look up index %s: %v", index, err)
		}
		if exists != want {
			t.Errorf("index %s exists = %v, want %v", index, exists, want)
		}
	}
}
