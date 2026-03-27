// Package dbutil provides shared database utilities.
package dbutil

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RunMigrations applies the listed SQL migration files from fsys in order.
// The label is used in the log message emitted on success.
func RunMigrations(ctx context.Context, pool *pgxpool.Pool, files []string, fsys fs.FS, label string) error {
	for _, f := range files {
		data, err := fs.ReadFile(fsys, f)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", f, err)
		}
		if _, err := pool.Exec(ctx, string(data)); err != nil {
			return fmt.Errorf("exec migration %s: %w", f, err)
		}
	}
	slog.Info(label + ": migrations applied")
	return nil
}
