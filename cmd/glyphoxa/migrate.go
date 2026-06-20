package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"text/tabwriter"

	// pgx stdlib driver: goose needs a database/sql handle (ADR-0031).
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// migrateUsage documents the `glyphoxa migrate` subcommand (ADR-0031). This is
// a self-contained entry point; wiring it into the root command + Mode
// dispatcher belongs to the control-plane task (#6).
const migrateUsage = `usage: glyphoxa migrate <up|down|status|version>

  up       apply all pending migrations (advisory-locked)
  down     roll back the most recently applied migration
  status   show each migration's applied/pending state
  version  print the current schema version

Connection string is read from $GLYPHOXA_DATABASE_URL (or $DATABASE_URL).`

// RunMigrate is the entry point for the `migrate` subcommand. args are the
// arguments after "migrate" (e.g. ["up"]). The control-plane task wires this
// into the root command; `all` Mode startup instead calls storage.MigrateUp
// directly (ADR-0031).
func RunMigrate(ctx context.Context, args []string) error {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, migrateUsage)
		return fmt.Errorf("migrate: missing subcommand")
	}

	// Help is a pure documentation path: it must succeed with no database and
	// no network (e.g. `docker run … glyphoxa migrate --help` in a fresh image),
	// so it short-circuits before the DSN check. Usage goes to stdout — it's the
	// requested output here, not an error diagnostic.
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Println(migrateUsage)
		return nil
	}

	dsn := os.Getenv("GLYPHOXA_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		return fmt.Errorf("migrate: set $GLYPHOXA_DATABASE_URL (or $DATABASE_URL)")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("migrate: open db: %w", err)
	}
	defer db.Close()

	switch args[0] {
	case "up":
		if err := storage.MigrateUp(ctx, db); err != nil {
			return err
		}
		fmt.Println("migrate: up complete")
		return nil

	case "down":
		if err := storage.MigrateDown(ctx, db); err != nil {
			return err
		}
		fmt.Println("migrate: down complete")
		return nil

	case "status":
		statuses, err := storage.Status(ctx, db)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "VERSION\tSTATE\tSOURCE")
		for _, s := range statuses {
			state := "pending"
			if s.Applied {
				state = "applied"
			}
			fmt.Fprintf(w, "%d\t%s\t%s\n", s.Version, state, s.Source)
		}
		return w.Flush()

	case "version":
		v, err := storage.Version(ctx, db)
		if err != nil {
			return err
		}
		fmt.Printf("schema version: %d\n", v)
		return nil

	default:
		fmt.Fprintln(os.Stderr, migrateUsage)
		return fmt.Errorf("migrate: unknown subcommand %q", args[0])
	}
}
