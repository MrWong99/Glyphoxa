package storage

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// fakeQueryMetrics records the (family, duration) pairs the tracer emits. Not
// concurrency-guarded: the unit tests drive it from one goroutine.
type fakeQueryMetrics struct {
	families  []string
	durations []time.Duration
}

func (f *fakeQueryMetrics) DBQuery(query string, d time.Duration) {
	f.families = append(f.families, query)
	f.durations = append(f.durations, d)
}

// fakeClock advances a fixed step on every read, so a Start/End pair yields an
// exact, assertable elapsed time.
type fakeClock struct {
	now  time.Time
	step time.Duration
}

func (c *fakeClock) Now() time.Time {
	t := c.now
	c.now = c.now.Add(c.step)
	return t
}

// TestQueryTracerRecordsAnnotatedFamily pins the tracer's core contract (#605):
// a ctx annotated with a query family records under that family name, with the
// elapsed time between TraceQueryStart and TraceQueryEnd.
func TestQueryTracerRecordsAnnotatedFamily(t *testing.T) {
	rec := &fakeQueryMetrics{}
	clock := &fakeClock{now: time.Unix(0, 0), step: 7 * time.Millisecond}
	tr := NewQueryTracer(rec)
	tr.now = clock.Now

	ctx := withQueryFamily(context.Background(), famSearchChunks)
	ctx = tr.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{})
	tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})

	if len(rec.families) != 1 {
		t.Fatalf("want 1 record, got %d", len(rec.families))
	}
	if rec.families[0] != "search_chunks" {
		t.Errorf("family = %q, want search_chunks", rec.families[0])
	}
	if rec.durations[0] != 7*time.Millisecond {
		t.Errorf("duration = %v, want 7ms", rec.durations[0])
	}
}

// TestQueryTracerUnannotatedRecordsOther pins the cardinality floor (ADR-0032):
// every query the pool runs is timed, but one nobody annotated lands in the
// single catch-all bucket rather than minting a label from its SQL.
func TestQueryTracerUnannotatedRecordsOther(t *testing.T) {
	rec := &fakeQueryMetrics{}
	clock := &fakeClock{now: time.Unix(0, 0), step: 3 * time.Millisecond}
	tr := NewQueryTracer(rec)
	tr.now = clock.Now

	ctx := tr.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{})
	tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})

	if len(rec.families) != 1 {
		t.Fatalf("want 1 record, got %d", len(rec.families))
	}
	if rec.families[0] != "other" {
		t.Errorf("family = %q, want other", rec.families[0])
	}
	if rec.durations[0] != 3*time.Millisecond {
		t.Errorf("duration = %v, want 3ms", rec.durations[0])
	}
}

// TestQueryFamilySurvivesDerivedContext pins that the annotation rides the
// context chain: storage methods derive ctx (timeouts, cancellation) between the
// annotation site and the query, so a WithTimeout child must still record under
// the annotated family — otherwise every budgeted read silently becomes "other".
func TestQueryFamilySurvivesDerivedContext(t *testing.T) {
	rec := &fakeQueryMetrics{}
	clock := &fakeClock{now: time.Unix(0, 0), step: time.Millisecond}
	tr := NewQueryTracer(rec)
	tr.now = clock.Now

	ctx := withQueryFamily(context.Background(), famFirstLineAtOrAfter)
	child, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()

	child = tr.TraceQueryStart(child, nil, pgx.TraceQueryStartData{})
	tr.TraceQueryEnd(child, nil, pgx.TraceQueryEndData{})

	if len(rec.families) != 1 || rec.families[0] != "first_line_at_or_after" {
		t.Fatalf("families = %v, want [first_line_at_or_after]", rec.families)
	}
}
