package main

import (
	"context"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestNewTracedPoolAttachesQueryTracer pins the wiring half of #605: a
// long-lived server pool carries the storage QueryTracer on its connection
// config, so every query it runs is timed. Without this the histogram exists but
// never gets a sample. No connection is made — pgxpool connects lazily.
func TestNewTracedPoolAttachesQueryTracer(t *testing.T) {
	pool, err := newTracedPool(context.Background(),
		"postgres://u:p@127.0.0.1:1/db?sslmode=disable", observe.NewPrometheusRecorder())
	if err != nil {
		t.Fatalf("newTracedPool: %v", err)
	}
	defer pool.Close()

	if _, ok := pool.Config().ConnConfig.Tracer.(*storage.QueryTracer); !ok {
		t.Fatalf("pool tracer = %T, want *storage.QueryTracer", pool.Config().ConnConfig.Tracer)
	}
}

// TestNewTracedPoolRejectsBadDSN keeps the DSN parse error attributable rather
// than surfacing as a nil pool later.
func TestNewTracedPoolRejectsBadDSN(t *testing.T) {
	if _, err := newTracedPool(context.Background(), "://nonsense", observe.NewPrometheusRecorder()); err == nil {
		t.Fatal("want error for an unparsable DSN, got nil")
	}
}
