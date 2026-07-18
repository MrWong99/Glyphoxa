//go:build integration

package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestFirstTenantEmpty asserts FirstTenant returns ErrNotFound on a freshly
// migrated (tenant-less) database — the seed's "no tenant yet" branch.
func TestFirstTenantEmpty(t *testing.T) {
	st := migrated(t)
	ctx := context.Background()

	_, err := st.FirstTenant(ctx)
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("FirstTenant on empty DB = %v, want ErrNotFound", err)
	}
}

// TestFirstTenantReturnsEarliest asserts FirstTenant returns the earliest-created
// Tenant — the one the -bundle seed reuses so a bundle import lands beside any
// pre-existing tenant instead of minting a duplicate "Glyphoxa".
func TestFirstTenantReturnsEarliest(t *testing.T) {
	st := migrated(t)
	ctx := context.Background()

	firstID, err := st.CreateTenant(ctx, "Alpha")
	if err != nil {
		t.Fatalf("create first tenant: %v", err)
	}
	if _, err := st.CreateTenant(ctx, "Beta"); err != nil {
		t.Fatalf("create second tenant: %v", err)
	}

	got, err := st.FirstTenant(ctx)
	if err != nil {
		t.Fatalf("FirstTenant: %v", err)
	}
	if got.ID != firstID {
		t.Fatalf("FirstTenant = %q (%s), want earliest %q", got.Name, got.ID, firstID)
	}
}

// TestGetTenant is the by-id Tenant read GetCurrentUser's tenant-name lookup
// uses (ADR-0055): a known id returns the row, an unknown one ErrNotFound.
func TestGetTenant(t *testing.T) {
	st := migrated(t)
	ctx := context.Background()

	id, err := st.CreateTenant(ctx, "Alpha")
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	got, err := st.GetTenant(ctx, id)
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if got.ID != id || got.Name != "Alpha" {
		t.Fatalf("GetTenant = %+v, want id %s name Alpha", got, id)
	}

	if _, err := st.GetTenant(ctx, uuid.New()); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("GetTenant(unknown) err = %v, want ErrNotFound", err)
	}
}
