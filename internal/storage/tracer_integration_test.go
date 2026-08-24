//go:build integration

package storage_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// recordingQueryMetrics is a concurrency-safe QueryMetrics sink: the pool may
// trace from any goroutine (including its own health-check connections).
type recordingQueryMetrics struct {
	mu      sync.Mutex
	records []string
}

func (r *recordingQueryMetrics) DBQuery(query string, _ time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, query)
}

// count returns how many queries recorded under family.
func (r *recordingQueryMetrics) count(family string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, f := range r.records {
		if f == family {
			n++
		}
	}
	return n
}

// reset drops everything recorded so far, so a test can assert on ONE call in
// isolation (seeding runs its own queries through the same traced pool).
func (r *recordingQueryMetrics) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = nil
}

// openTracedPool wires the pgx QueryTracer onto a pool exactly as the
// long-lived server pools do (pgxpool.ParseConfig → ConnConfig.Tracer →
// NewWithConfig), so these tests exercise the real attachment path.
func openTracedPool(t *testing.T, dsn string, rec storage.QueryMetrics) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("pgxpool.ParseConfig: %v", err)
	}
	cfg.ConnConfig.Tracer = storage.NewQueryTracer(rec)
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("pgxpool.NewWithConfig: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestQueryTracerRecordsHotFamiliesThroughPool is the #605 AC pin against a real
// Postgres: the annotated hot reads land under their registered family names,
// and a statement nobody annotated records as "other" rather than minting a new
// series from its SQL (ADR-0032).
func TestQueryTracerRecordsHotFamiliesThroughPool(t *testing.T) {
	dsn := startPostgres(t)
	seedCampaign(t, dsn) // migrate + a tenant/campaign we do not reuse here

	rec := &recordingQueryMetrics{}
	pool := openTracedPool(t, dsn, rec)
	st := storage.New(pool)
	ctx := context.Background()

	var campaignID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM campaign LIMIT 1`).Scan(&campaignID); err != nil {
		t.Fatalf("select campaign: %v", err)
	}
	vs, err := st.CreateVoiceSession(ctx, campaignID)
	if err != nil {
		t.Fatalf("CreateVoiceSession: %v", err)
	}

	// search_chunks — the ANN read inside the 250ms recall budget (ADR-0042).
	rec.reset()
	if _, err := st.SearchChunksByCampaign(ctx, campaignID, vec768(1, 0), 3); err != nil {
		t.Fatalf("SearchChunksByCampaign: %v", err)
	}
	if got := rec.count("search_chunks"); got != 1 {
		t.Errorf("search_chunks records = %d, want 1 (all: %v)", got, rec.records)
	}

	// first_line_at_or_after — the per-hit N+1 the command palette pays serially.
	rec.reset()
	if _, err := st.FirstLineIDAtOrAfter(ctx, vs.ID, time.Now()); err != nil && err != storage.ErrNotFound {
		t.Fatalf("FirstLineIDAtOrAfter: %v", err)
	}
	if got := rec.count("first_line_at_or_after"); got != 1 {
		t.Errorf("first_line_at_or_after records = %d, want 1 (all: %v)", got, rec.records)
	}

	// An unannotated read is still timed, but only under the catch-all family.
	rec.reset()
	if _, err := st.CountTranscriptLines(ctx, vs.ID); err != nil {
		t.Fatalf("CountTranscriptLines: %v", err)
	}
	if got := rec.count("other"); got != 1 {
		t.Errorf("other records = %d, want 1 (all: %v)", got, rec.records)
	}
}

// TestQueryTracerRecordsWriteFamilies pins the three annotated write paths
// (#605): the per-sentence Line upsert and per-chunk insert on the transcript
// hot path, and the voice-intent claim the claim-plane polls (ADR-0057).
func TestQueryTracerRecordsWriteFamilies(t *testing.T) {
	dsn := startPostgres(t)
	seedCampaign(t, dsn)

	rec := &recordingQueryMetrics{}
	pool := openTracedPool(t, dsn, rec)
	st := storage.New(pool)
	ctx := context.Background()

	var campaignID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM campaign LIMIT 1`).Scan(&campaignID); err != nil {
		t.Fatalf("select campaign: %v", err)
	}
	vs, err := st.CreateVoiceSession(ctx, campaignID)
	if err != nil {
		t.Fatalf("CreateVoiceSession: %v", err)
	}

	rec.reset()
	if err := st.UpsertTranscriptLine(ctx, storage.TranscriptLine{
		VoiceSessionID: vs.ID,
		CampaignID:     campaignID,
		LineID:         "line-1",
		Seq:            1,
		Who:            "Gundren",
		Tag:            "player",
		Kind:           "speech",
		TS:             time.Date(2026, 7, 5, 18, 0, 0, 0, time.UTC),
		Text:           "we ride at dawn",
	}); err != nil {
		t.Fatalf("UpsertTranscriptLine: %v", err)
	}
	if got := rec.count("upsert_transcript_line"); got != 1 {
		t.Errorf("upsert_transcript_line records = %d, want 1 (all: %v)", got, rec.records)
	}

	rec.reset()
	if _, err := st.InsertTranscriptChunk(ctx, storage.TranscriptChunk{
		CampaignID:     campaignID,
		VoiceSessionID: vs.ID,
		Content:        "we ride at dawn",
		StartedAt:      time.Date(2026, 7, 5, 18, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("InsertTranscriptChunk: %v", err)
	}
	if got := rec.count("insert_transcript_chunk"); got != 1 {
		t.Errorf("insert_transcript_chunk records = %d, want 1 (all: %v)", got, rec.records)
	}

	rec.reset()
	if _, err := st.ClaimVoiceSessionIntent(ctx, "instance-1"); err != nil && err != storage.ErrNotFound {
		t.Fatalf("ClaimVoiceSessionIntent: %v", err)
	}
	if got := rec.count("claim_voice_intent"); got != 1 {
		t.Errorf("claim_voice_intent records = %d, want 1 (all: %v)", got, rec.records)
	}
}
