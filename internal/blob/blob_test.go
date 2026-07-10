package blob_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/blob"
)

// TestKeyRoundTrip proves Key() builds the canonical tenant-scoped path and that
// ValidateKey recovers the tenant id from it — the package owns both
// construction and validation (ADR-0048: the storage layer never accepts a key
// without a tenant prefix).
func TestKeyRoundTrip(t *testing.T) {
	tenant := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	owner := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	key, err := blob.Key(tenant, "highlight", owner, "clip.opus")
	if err != nil {
		t.Fatalf("Key: %v", err)
	}
	want := "t/11111111-1111-1111-1111-111111111111/highlight/22222222-2222-2222-2222-222222222222/clip.opus"
	if key != want {
		t.Fatalf("Key = %q, want %q", key, want)
	}

	gotTenant, err := blob.ValidateKey(key)
	if err != nil {
		t.Fatalf("ValidateKey: %v", err)
	}
	if gotTenant != tenant {
		t.Fatalf("ValidateKey tenant = %s, want %s", gotTenant, tenant)
	}
}

// TestKeyRejectsBadOwnerAndName covers construction-side validation: an empty
// owner-kind or name (or a name with a path separator) can never yield a valid
// key.
func TestKeyRejectsBadOwnerAndName(t *testing.T) {
	tenant := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	owner := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	cases := map[string]struct {
		kind, name string
	}{
		"empty kind": {"", "clip.opus"},
		"empty name": {"highlight", ""},
		"slash name": {"highlight", "sub/clip.opus"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := blob.Key(tenant, tc.kind, owner, tc.name); !errors.Is(err, blob.ErrInvalidKey) {
				t.Fatalf("Key(%q,%q) err = %v, want ErrInvalidKey", tc.kind, tc.name, err)
			}
		})
	}
}

// TestValidateKeyRejectsMalformed exercises the validation side directly: a key
// must be exactly five segments "t"/<uuid>/<kind>/<owner-id>/<name>, kind+name
// non-empty. Anything else is ErrInvalidKey and must never reach SQL.
func TestValidateKeyRejectsMalformed(t *testing.T) {
	cases := map[string]string{
		"missing t prefix": "x/11111111-1111-1111-1111-111111111111/highlight/22222222-2222-2222-2222-222222222222/clip.opus",
		"non-uuid tenant":  "t/not-a-uuid/highlight/22222222-2222-2222-2222-222222222222/clip.opus",
		"empty kind":       "t/11111111-1111-1111-1111-111111111111//22222222-2222-2222-2222-222222222222/clip.opus",
		"empty name":       "t/11111111-1111-1111-1111-111111111111/highlight/22222222-2222-2222-2222-222222222222/",
		"four segments":    "t/11111111-1111-1111-1111-111111111111/highlight/clip.opus",
		"six segments":     "t/11111111-1111-1111-1111-111111111111/highlight/22222222-2222-2222-2222-222222222222/sub/clip.opus",
	}
	for name, key := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := blob.ValidateKey(key); !errors.Is(err, blob.ErrInvalidKey) {
				t.Fatalf("ValidateKey(%q) err = %v, want ErrInvalidKey", key, err)
			}
		})
	}
}

// TestPutPreChecks proves Put rejects a bad size or a prefix-less key BEFORE it
// touches the reader or the DB — so a nil pool is enough to observe the guard
// (ADR-0048: invalid key never reaches SQL; the cap is enforced at Put).
func TestPutPreChecks(t *testing.T) {
	tenant := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	owner := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	goodKey, err := blob.Key(tenant, "highlight", owner, "clip.opus")
	if err != nil {
		t.Fatalf("Key: %v", err)
	}

	// A nil-pool Postgres must be constructible; the pre-checks fire before any
	// pool method is called.
	store := blob.NewPostgres(nil)
	ctx := context.Background()

	t.Run("over cap", func(t *testing.T) {
		err := store.Put(ctx, goodKey, "audio/opus", strings.NewReader("x"), blob.MaxSize+1)
		if !errors.Is(err, blob.ErrTooLarge) {
			t.Fatalf("Put over cap err = %v, want ErrTooLarge", err)
		}
	})
	t.Run("negative size", func(t *testing.T) {
		err := store.Put(ctx, goodKey, "audio/opus", strings.NewReader("x"), -1)
		if err == nil {
			t.Fatal("Put negative size err = nil, want error")
		}
	})
	t.Run("invalid key", func(t *testing.T) {
		err := store.Put(ctx, "no-prefix", "audio/opus", bytes.NewReader([]byte("x")), 1)
		if !errors.Is(err, blob.ErrInvalidKey) {
			t.Fatalf("Put invalid key err = %v, want ErrInvalidKey", err)
		}
	})
}
