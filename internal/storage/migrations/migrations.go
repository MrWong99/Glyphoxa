// Package migrations holds the embedded SQL migration files for the Glyphoxa
// schema. Files are sequential, zero-padded (00001_init.sql, …) with up + down
// in the same file via goose annotations (ADR-0031). They are embedded into the
// single binary and applied through goose's library API — never a separate
// migration toolchain.
package migrations

import "embed"

// FS is the embedded migration filesystem consumed by the goose provider.
//
//go:embed *.sql
var FS embed.FS
