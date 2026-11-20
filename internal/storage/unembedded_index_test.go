//go:build integration

package storage_test

import (
	"context"
	"strings"
	"testing"
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
