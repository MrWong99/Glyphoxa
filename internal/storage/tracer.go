package storage

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// QueryMetrics is the storage-side seam for the query-latency histogram (#605).
// *observe.PrometheusRecorder satisfies it; storage depends on this local
// interface rather than on internal/observe, so the DB layer stays free of a
// metrics dependency and a test can substitute a sink.
type QueryMetrics interface {
	// DBQuery records one completed query under a BOUNDED family label.
	DBQuery(query string, d time.Duration)
}

// Query family labels (#605, ADR-0032): a small const registry so the `query`
// label can never grow with the SQL text. An unannotated query records as
// famOther — raw SQL and args NEVER become label values.
const (
	famSearchChunks          = "search_chunks"
	famFirstLineAtOrAfter    = "first_line_at_or_after"
	famUpsertTranscriptLine  = "upsert_transcript_line"
	famInsertTranscriptChunk = "insert_transcript_chunk"
	famClaimVoiceIntent      = "claim_voice_intent"
	famOther                 = "other"
)

// queryFamilyKey is the unexported context key carrying the family label.
type queryFamilyKey struct{}

// withQueryFamily annotates ctx so any query issued under it records against
// family. Annotation travels by context, not by rewriting the SQL, so the
// statement text (and pgx's prepared-statement cache) is untouched.
func withQueryFamily(ctx context.Context, family string) context.Context {
	return context.WithValue(ctx, queryFamilyKey{}, family)
}

// queryFamily reads the annotation, defaulting to famOther.
func queryFamily(ctx context.Context) string {
	if f, ok := ctx.Value(queryFamilyKey{}).(string); ok {
		return f
	}
	return famOther
}

// queryStartKey carries the start instant from TraceQueryStart to TraceQueryEnd.
type queryStartKey struct{}

// QueryTracer implements pgx.QueryTracer, timing every query the pool runs and
// recording it under its bounded family label (#605). It is deliberately
// alloc-light on the hot path — the ANN search it measures runs inside the
// 250ms recall budget (ADR-0042), so End does a context read and one Observe:
// no logging, no map allocation, no SQL inspection.
//
// SendBatch (pgx.BatchTracer) is out of scope: this tracer only implements
// QueryTracer, so batched statements are not timed.
type QueryTracer struct {
	rec QueryMetrics
	now func() time.Time
}

// NewQueryTracer returns a tracer recording into rec. Attach it to a pool via
// pgxpool.Config.ConnConfig.Tracer.
func NewQueryTracer(rec QueryMetrics) *QueryTracer {
	return &QueryTracer{rec: rec, now: time.Now}
}

// TraceQueryStart stashes the start instant on the returned context.
func (t *QueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, queryStartKey{}, t.now())
}

// TraceQueryEnd records the elapsed time under the ctx's query family.
func (t *QueryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	start, ok := ctx.Value(queryStartKey{}).(time.Time)
	if !ok {
		return
	}
	t.rec.DBQuery(queryFamily(ctx), t.now().Sub(start))
}

var _ pgx.QueryTracer = (*QueryTracer)(nil)
