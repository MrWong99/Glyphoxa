package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MrWong99/Glyphoxa/internal/bundle"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// RunExport is the `glyphoxa export` subcommand: it serializes one Campaign into
// a #287 bundle for headless backup/provisioning, mirroring seed.go's env
// conventions. Usage:
//
//	glyphoxa export -campaign <uuid> [-include-history] [-o <file>]
//
// Connection string: $GLYPHOXA_DATABASE_URL (or $DATABASE_URL). Unlike seed,
// export needs NO $GLYPHOXA_SECRET — the exporter reads through the storage
// allowlist and never decrypts a credential (ADR-0053 §2). Output defaults to
// stdout; -o writes the gzipped bundle to a file.
func RunExport(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	campaignStr := fs.String("campaign", "", "campaign UUID to export (required)")
	includeHistory := fs.Bool("include-history", false, "include Voice Session transcripts (default off; backup/migration)")
	outPath := fs.String("o", "", "output file (default stdout)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Validate the campaign id BEFORE opening the DB so a bad invocation fails fast
	// with no connection attempt (and the arg-parse unit test needs no Postgres).
	if *campaignStr == "" {
		return fmt.Errorf("export: -campaign <uuid> is required")
	}
	campaignID, err := uuid.Parse(*campaignStr)
	if err != nil {
		return fmt.Errorf("export: -campaign must be a UUID: %w", err)
	}

	dsn := databaseURL()
	if dsn == "" {
		return fmt.Errorf("export: set $GLYPHOXA_DATABASE_URL (or $DATABASE_URL)")
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("export: open db: %w", err)
	}
	defer pool.Close()

	b, err := bundle.Export(ctx, storage.New(pool), campaignID, bundle.ExportOptions{
		IncludeHistory: *includeHistory,
	})
	if err != nil {
		return err
	}

	var out io.Writer = os.Stdout
	if *outPath != "" {
		f, err := os.Create(*outPath)
		if err != nil {
			return fmt.Errorf("export: create %s: %w", *outPath, err)
		}
		defer f.Close()
		out = f
	}
	if err := bundle.Encode(out, b); err != nil {
		return fmt.Errorf("export: encode bundle: %w", err)
	}
	return nil
}
