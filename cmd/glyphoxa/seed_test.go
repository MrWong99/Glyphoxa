package main

import (
	"context"
	"log/slog"
	"strings"
	"testing"
)

// TestRunSeed_BundleMissingFile is TEST 2 (arg parse): `seed -bundle <path>` with
// a nonexistent file fails fast with an actionable error naming the path, before
// any DB connection — a mistyped bundle path should not need Postgres to report.
func TestRunSeed_BundleMissingFile(t *testing.T) {
	err := RunSeed(context.Background(), slog.Default(),
		[]string{"-bundle", "/no/such/demo.glyphoxa.json"})
	if err == nil || !strings.Contains(err.Error(), "/no/such/demo.glyphoxa.json") {
		t.Fatalf("missing bundle file: err=%v, want mention of the path", err)
	}
}

// TestRunSeed_BundleEmptyPath errors on an explicit empty `-bundle ""` rather
// than silently falling through to the legacy demo-NPC seed — a flag given with
// no value is a mistake, not a request for the legacy path.
func TestRunSeed_BundleEmptyPath(t *testing.T) {
	err := RunSeed(context.Background(), slog.Default(), []string{"-bundle", ""})
	if err == nil || !strings.Contains(err.Error(), "-bundle") {
		t.Fatalf("empty -bundle: err=%v, want a -bundle complaint (not legacy fallthrough)", err)
	}
}

// TestRunSeed_UnknownFlag rejects an unknown flag rather than silently falling
// through to the legacy demo-NPC path.
func TestRunSeed_UnknownFlag(t *testing.T) {
	err := RunSeed(context.Background(), slog.Default(), []string{"-nope"})
	if err == nil {
		t.Fatal("unknown flag: want error, got nil")
	}
}
