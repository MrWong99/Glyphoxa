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
	// List returns the keys of every stored blob whose key begins with prefix, in
	// ascending key order. It reads keys ONLY (never the bytes). It exists so the
	// process-wide reconciliation sweeps (e.g. the Highlight orphan-image sweep,
	// #406/#421) can enumerate blobs THROUGH the seam instead of querying a
	// backend's table directly — an S3 swap re-implements List and the sweep is
	// unchanged. An empty prefix lists everything; pass AllKeysPrefix to walk the
	// whole store.
	List(ctx context.Context, prefix string) ([]string, error)
}

// keyPrefix is the mandatory first segment of every key.
const keyPrefix = "t"

// AllKeysPrefix matches every valid key — all keys are tenant-rooted under
// keyPrefix. A process-wide sweep that carries no tenant (ADR-0049) passes it to
// List to enumerate the whole store, then filters by owner-kind/name in Go.
const AllKeysPrefix = keyPrefix + "/"

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

// KeyParts is the decoded structure of a canonical key
// (t/<tenant_id>/<owner-kind>/<owner-id>/<name>). ParseKey produces it so callers
// that must reason about a key's owner-kind/name (e.g. the reconciliation sweeps)
// do that in the blob package rather than re-deriving segment indices at the call
// site — the package owns key structure (ADR-0048).
type KeyParts struct {
	TenantID  uuid.UUID
	OwnerKind string
	OwnerID   uuid.UUID
	Name      string
}

// ValidateKey parses a key and returns the tenant id it encodes, or
// ErrInvalidKey. It is the narrow form of ParseKey the backend uses to derive the
// tenant_id column, so an invalid key never reaches SQL.
func ValidateKey(key string) (tenantID uuid.UUID, err error) {
	parts, err := ParseKey(key)
	if err != nil {
		return uuid.Nil, err
	}
	return parts.TenantID, nil
}

// ParseKey decodes a key into its parts, or ErrInvalidKey. A valid key is exactly
// five segments — "t"/<uuid>/<kind>/<owner-id>/<name> — with parseable, canonical
// tenant and owner uuids and non-empty kind and name.
func ParseKey(key string) (KeyParts, error) {
	segs := strings.Split(key, "/")
	if len(segs) != 5 {
		return KeyParts{}, ErrInvalidKey
	}
	if segs[0] != keyPrefix {
		return KeyParts{}, ErrInvalidKey
	}
	tenant, perr := uuid.Parse(segs[1])
	if perr != nil {
		return KeyParts{}, ErrInvalidKey
	}
	// uuid.Parse tolerates braces/urn/no-dash forms; require the canonical dashed
	// string so one logical blob has exactly one key. A non-canonical hand-built
	// key would otherwise become an orphan the E8 delete hook can never
	// reconstruct via Key().
	if segs[1] != tenant.String() {
		return KeyParts{}, ErrInvalidKey
	}
	// segs[2] = owner-kind, segs[3] = owner-id, segs[4] = name. Kind and name
	// must be non-empty; a slash inside either is impossible (it would raise the
	// segment count above five). The owner-id gets the same canonical-uuid
	// discipline as the tenant.
	if segs[2] == "" || segs[4] == "" {
		return KeyParts{}, ErrInvalidKey
	}
	owner, oerr := uuid.Parse(segs[3])
	if oerr != nil || segs[3] != owner.String() {
		return KeyParts{}, ErrInvalidKey
	}
	return KeyParts{TenantID: tenant, OwnerKind: segs[2], OwnerID: owner, Name: segs[4]}, nil
}
