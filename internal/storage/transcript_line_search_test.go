//go:build integration

package storage_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// searchLineTexts pulls the text out of a search result in rank order.
func searchLineTexts(lines []storage.TranscriptLine) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.Text
	}
	return out
}

// seedLine upserts one transcript Line for a session under a campaign at a given
// second, returning nothing (the test asserts on search results, not the row).
func seedLine(t *testing.T, st *storage.Store, vsID, campaignID uuid.UUID, lineID string, seq int64, ts time.Time, text string) {
	t.Helper()
	if err := st.UpsertTranscriptLine(context.Background(), storage.TranscriptLine{
		VoiceSessionID: vsID, CampaignID: campaignID, LineID: lineID, Seq: seq,
		Who: "Player / DM", Kind: "player", TS: ts, Text: text,
	}); err != nil {
		t.Fatalf("UpsertTranscriptLine %s: %v", lineID, err)
	}
}

// TestSearchTranscriptLines is #120 AC1 + AC5: a full-text query over the
// Active Campaign's persisted transcript returns ranked matches (a line that
// mentions the term more often outranks a single mention), the search is
// Campaign-scoped so another Campaign's identical match NEVER leaks (AC5), and
// an empty / all-punctuation query is a no-op (no matches, no error). Injected
// tsquery operators are neutralized by BuildTSQuery — a query full of &, |,
// parens and quotes must not error.
func TestSearchTranscriptLines(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	ts := func(sec int) time.Time { return time.Date(2026, 6, 27, 18, 0, sec, 0, time.UTC) }

	vs, err := st.CreateVoiceSession(ctx, campaignID)
	if err != nil {
		t.Fatalf("CreateVoiceSession: %v", err)
	}

	// Two matching lines: the term appears twice in one (higher ts_rank) and once
	// in the other, so the ranking is deterministic. A third line never matches.
	seedLine(t, st, vs.ID, campaignID, "u:1", 1, ts(1), "The dragon breathes fire and the dragon roars")
	seedLine(t, st, vs.ID, campaignID, "u:2", 2, ts(2), "Long ago I once saw a dragon far away")
	seedLine(t, st, vs.ID, campaignID, "u:3", 3, ts(3), "We rested quietly at the inn")

	// A second Campaign whose line matches "dragon" identically — must not leak.
	var campaign2 uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO campaign (tenant_id, name, system, language)
		 VALUES ($1, 'Other', 'dnd5e', 'en') RETURNING id`, tenantID).Scan(&campaign2); err != nil {
		t.Fatalf("insert campaign2: %v", err)
	}
	vs2, err := st.CreateVoiceSession(ctx, campaign2)
	if err != nil {
		t.Fatalf("CreateVoiceSession campaign2: %v", err)
	}
	seedLine(t, st, vs2.ID, campaign2, "u:1", 1, ts(1), "A different dragon guards the eastern gate")

	got, err := st.SearchTranscriptLines(ctx, campaignID, "dragon", 50)
	if err != nil {
		t.Fatalf("SearchTranscriptLines: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d matches %v, want 2 (both campaign-1 dragon lines, no leak)", len(got), searchLineTexts(got))
	}
	// AC1: the twice-mentioning line outranks the single mention.
	if got[0].LineID != "u:1" {
		t.Errorf("rank[0] = %q (%s), want the twice-mentioning line u:1", got[0].Text, got[0].LineID)
	}
	// AC5: no cross-campaign row leaks — every hit is campaign-1's session.
	for _, l := range got {
		if l.VoiceSessionID != vs.ID {
			t.Errorf("cross-campaign leak: line %q belongs to session %s, not campaign-1 %s", l.Text, l.VoiceSessionID, vs.ID)
		}
	}

	// Typeahead: a prefix term matches a longer word ("drag" → "dragon").
	pref, err := st.SearchTranscriptLines(ctx, campaignID, "drag", 50)
	if err != nil {
		t.Fatalf("SearchTranscriptLines prefix: %v", err)
	}
	if len(pref) != 2 {
		t.Errorf("prefix 'drag' = %v, want the two dragon lines (typeahead prefix)", searchLineTexts(pref))
	}

	// An empty / all-punctuation query is a no-op: no matches, no error.
	for _, q := range []string{"", "   ", "!@#$"} {
		empty, err := st.SearchTranscriptLines(ctx, campaignID, q, 50)
		if err != nil {
			t.Errorf("SearchTranscriptLines(%q) err = %v, want nil", q, err)
		}
		if len(empty) != 0 {
			t.Errorf("SearchTranscriptLines(%q) = %v, want no matches", q, searchLineTexts(empty))
		}
	}

	// tsquery hardening: injected operators are sanitized to term separators by
	// BuildTSQuery, so a query full of specials must not error — it still finds the
	// two dragon lines (the surviving words AND-join).
	hard, err := st.SearchTranscriptLines(ctx, campaignID, `dragon & fire | (roars) "!"`, 50)
	if err != nil {
		t.Fatalf("SearchTranscriptLines with tsquery specials errored (must sanitize, not 500): %v", err)
	}
	if len(hard) != 1 || hard[0].LineID != "u:1" {
		t.Errorf("specials query = %v, want only u:1 (dragon & fire & roars all in u:1)", searchLineTexts(hard))
	}

	// limit caps the result set.
	one, err := st.SearchTranscriptLines(ctx, campaignID, "dragon", 1)
	if err != nil {
		t.Fatalf("SearchTranscriptLines limit=1: %v", err)
	}
	if len(one) != 1 {
		t.Errorf("limit=1 returned %d rows, want 1", len(one))
	}
}

// TestSearchTranscriptLines_UsesFtsIndex is #120's index proof: the fts match is
// served by the transcript_line_fts_idx GIN index, not a substring scan. On a
// tiny table the planner would prefer a seqscan, so enable_seqscan is turned off
// inside a transaction to force the index path to show — an ILIKE substring
// implementation could never produce this plan.
func TestSearchTranscriptLines_UsesFtsIndex(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	vs, err := st.CreateVoiceSession(ctx, campaignID)
	if err != nil {
		t.Fatalf("CreateVoiceSession: %v", err)
	}
	for i, text := range []string{"the dragon roars", "a quiet inn", "old stone bridge"} {
		seedLine(t, st, vs.ID, campaignID, "u:"+string(rune('1'+i)), int64(i+1), time.Now(), text)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
		t.Fatalf("disable seqscan: %v", err)
	}
	rows, err := tx.Query(ctx,
		`EXPLAIN SELECT id FROM transcript_line WHERE fts @@ to_tsquery('simple', 'drag:*')`)
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
	if !strings.Contains(plan.String(), "transcript_line_fts_idx") {
		t.Errorf("query plan does not use the fts GIN index (substring scan?):\n%s", plan.String())
	}
}
