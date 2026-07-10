// Package blob is the object-storage seam (ADR-0048). Binary payloads —
// Highlight clips, generated images, future audio extracts and export bundles —
// live behind a small streaming Store interface so the backend can change (v1
// Postgres bytea → S3 growth path) without touching callers.
//
// Keys are tenant-scoped paths — t/<tenant_id>/<owner-kind>/<owner-id>/<name> —
// and the tenant prefix is MANDATORY: the package owns both construction (Key)
// and validation (ValidateKey), and the backend derives the tenant_id column
// FROM the key, so a key without a valid tenant prefix can never reach SQL.
//
// Deletion goes through the seam, not FK cascade (ADR-0048): a deterministic
// Key() plus a seam Delete are the mechanism owning rows use to drop their
// blobs. Retention semantics are ADR-0051's, not this package's.
package blob

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
)

// MaxSize is the per-blob size cap enforced at Put (ADR-0048: 32 MiB to start).
// The cap is what keeps a bytea backend honest — anything larger forces the
// backend conversation instead of silently bloating the DB.
const MaxSize int64 = 32 << 20

var (
	// ErrNotFound is returned by Get/Delete semantics where a key is absent.
	// (Delete is idempotent and returns nil for an absent key; ErrNotFound is
	// the Get signal.)
	ErrNotFound = errors.New("blob: not found")
	// ErrTooLarge is returned by Put when the declared size exceeds MaxSize.
	ErrTooLarge = errors.New("blob: payload exceeds size cap")
	// ErrInvalidKey is returned when a key is missing or malformed — no tenant
	// prefix, non-uuid tenant, wrong segment count, or an empty/slash-bearing
	// owner-kind or name.
	ErrInvalidKey = errors.New("blob: invalid key")
)

// Meta is the stored metadata for a blob, returned alongside its bytes on Get.
type Meta struct {
	ContentType string
	Size        int64
	CreatedAt   time.Time
}

// Store is the blob seam. Signatures are streaming even where a backend buffers,
// so backends can change without touching callers (ADR-0048).
type Store interface {
	// Put stores the blob at key, reading EXACTLY size bytes from r. Put on an
	// existing key is an upsert. size<0 or size>MaxSize, or an invalid key, is
	// an error before any read.
	Put(ctx context.Context, key, contentType string, r io.Reader, size int64) error
	// Get returns the blob's bytes and Meta, or ErrNotFound.
	Get(ctx context.Context, key string) (io.ReadCloser, Meta, error)
	// Delete removes the blob at key. Deleting an absent key returns nil
	// (idempotent).
	Delete(ctx context.Context, key string) error
}

// keyPrefix is the mandatory first segment of every key.
const keyPrefix = "t"

// Key builds the canonical tenant-scoped key
// t/<tenant_id>/<owner-kind>/<owner-id>/<name>. It is deterministic — the same
// inputs always yield the same key — so an owning row can reconstruct its blob
// key for Delete (ADR-0048 lifecycle hook). ownerKind and name must be
// non-empty and name must not contain a path separator, else ErrInvalidKey.
func Key(tenantID uuid.UUID, ownerKind string, ownerID uuid.UUID, name string) (string, error) {
	if ownerKind == "" || strings.Contains(ownerKind, "/") {
		return "", ErrInvalidKey
	}
	if name == "" || strings.Contains(name, "/") {
		return "", ErrInvalidKey
	}
	key := strings.Join([]string{keyPrefix, tenantID.String(), ownerKind, ownerID.String(), name}, "/")
	// Round-trip through ValidateKey so construction can never emit a key the
	// backend would reject.
	if _, err := ValidateKey(key); err != nil {
		return "", err
	}
	return key, nil
}

// ValidateKey parses a key and returns the tenant id it encodes, or
// ErrInvalidKey. A valid key is exactly five segments —
// "t"/<uuid>/<kind>/<owner-id>/<name> — with a parseable tenant uuid and
// non-empty kind and name. The backend calls this and derives the tenant_id
// column from the result, so an invalid key never reaches SQL.
func ValidateKey(key string) (tenantID uuid.UUID, err error) {
	segs := strings.Split(key, "/")
	if len(segs) != 5 {
		return uuid.Nil, ErrInvalidKey
	}
	if segs[0] != keyPrefix {
		return uuid.Nil, ErrInvalidKey
	}
	tenant, perr := uuid.Parse(segs[1])
	if perr != nil {
		return uuid.Nil, ErrInvalidKey
	}
	// segs[2] = owner-kind, segs[3] = owner-id, segs[4] = name. Kind and name
	// must be non-empty; a slash inside either is impossible (it would raise the
	// segment count above five).
	if segs[2] == "" || segs[4] == "" {
		return uuid.Nil, ErrInvalidKey
	}
	return tenant, nil
}
