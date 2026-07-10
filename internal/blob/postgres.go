package blob

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Postgres is the v1 blob backend: bytea rows in the shared Postgres (ADR-0048).
// Every ADR-0034 deployment shape already shares Postgres, so a Voice Instance
// writes and a Web Instance serves with zero new configuration. bytea is scanned
// straight to []byte via pgx (no large-object API); the MaxSize cap keeps that
// honest.
type Postgres struct {
	pool *pgxpool.Pool
}

// NewPostgres builds a Postgres-backed Store over an existing pool. A nil pool
// is permitted (the Put pre-checks — size and key validation — run before any
// pool method is touched), which keeps the guard unit-testable without a DB.
func NewPostgres(pool *pgxpool.Pool) *Postgres {
	return &Postgres{pool: pool}
}

var _ Store = (*Postgres)(nil)

// Put stores the blob, upserting on key conflict (ADR-0048 semantics). It
// validates size and key BEFORE reading r or touching the DB, then reads
// EXACTLY size bytes plus one probe byte — a short or over-long stream is an
// error and no row is written. The tenant_id column is derived from the key. An
// upsert preserves the original created_at (so Meta.CreatedAt is birth time, not
// last-write time — ADR-0051 retention math depends on it).
func (p *Postgres) Put(ctx context.Context, key, contentType string, r io.Reader, size int64) error {
	if size < 0 {
		return fmt.Errorf("blob: negative size %d", size)
	}
	if size > MaxSize {
		return ErrTooLarge
	}
	tenantID, err := ValidateKey(key)
	if err != nil {
		return err
	}

	buf := make([]byte, size)
	if _, err := io.ReadFull(r, buf); err != nil {
		// io.EOF (zero read) or io.ErrUnexpectedEOF (partial) both mean the
		// stream was shorter than the declared size.
		return fmt.Errorf("blob: read payload for %s: %w", key, err)
	}
	// Probe one more byte: a non-EOF read means the stream was longer than
	// declared, which we reject rather than silently truncate.
	var probe [1]byte
	if _, perr := io.ReadFull(r, probe[:]); perr == nil {
		return fmt.Errorf("blob: payload for %s exceeds declared size %d", key, size)
	} else if !errors.Is(perr, io.EOF) {
		return fmt.Errorf("blob: probe payload for %s: %w", key, perr)
	}

	_, err = p.pool.Exec(ctx,
		`INSERT INTO blob (key, tenant_id, content_type, size, bytes)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (key) DO UPDATE SET
		     tenant_id    = EXCLUDED.tenant_id,
		     content_type = EXCLUDED.content_type,
		     size         = EXCLUDED.size,
		     bytes        = EXCLUDED.bytes`,
		key, tenantID, contentType, size, buf)
	if err != nil {
		return fmt.Errorf("blob: put %s: %w", key, err)
	}
	return nil
}

// Get returns the blob's bytes (buffered into an in-memory ReadCloser) and Meta,
// or ErrNotFound. An invalid key is ErrInvalidKey and never reaches SQL.
func (p *Postgres) Get(ctx context.Context, key string) (io.ReadCloser, Meta, error) {
	if _, err := ValidateKey(key); err != nil {
		return nil, Meta{}, err
	}
	var (
		contentType string
		size        int64
		createdAt   time.Time
		raw         []byte
	)
	err := p.pool.QueryRow(ctx,
		`SELECT content_type, size, bytes, created_at FROM blob WHERE key = $1`, key).
		Scan(&contentType, &size, &raw, &createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, Meta{}, ErrNotFound
	}
	if err != nil {
		return nil, Meta{}, fmt.Errorf("blob: get %s: %w", key, err)
	}
	meta := Meta{ContentType: contentType, Size: size, CreatedAt: createdAt}
	return io.NopCloser(bytes.NewReader(raw)), meta, nil
}

// Delete removes the blob at key. Deleting an absent key returns nil
// (idempotent) — the deterministic Key() plus this Delete are the lifecycle
// mechanism owning rows use (ADR-0048; wiring is E8's). An invalid key is
// ErrInvalidKey and never reaches SQL.
func (p *Postgres) Delete(ctx context.Context, key string) error {
	if _, err := ValidateKey(key); err != nil {
		return err
	}
	if _, err := p.pool.Exec(ctx, `DELETE FROM blob WHERE key = $1`, key); err != nil {
		return fmt.Errorf("blob: delete %s: %w", key, err)
	}
	return nil
}
