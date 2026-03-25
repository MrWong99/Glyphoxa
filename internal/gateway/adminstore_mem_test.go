package gateway

import (
	"context"
	"testing"
)

func TestMemAdminStore_UpdateTenant_NotFound(t *testing.T) {
	t.Parallel()

	store := NewMemAdminStore()
	err := store.UpdateTenant(context.Background(), Tenant{ID: "nonexistent"})
	if err == nil {
		t.Error("expected error when updating nonexistent tenant")
	}
}

func TestMemAdminStore_ListTenants_SortOrder(t *testing.T) {
	t.Parallel()

	store := NewMemAdminStore()
	ctx := context.Background()

	// Insert in reverse order.
	_ = store.CreateTenant(ctx, Tenant{ID: "charlie"})
	_ = store.CreateTenant(ctx, Tenant{ID: "alpha"})
	_ = store.CreateTenant(ctx, Tenant{ID: "bravo"})

	tenants, err := store.ListTenants(ctx)
	if err != nil {
		t.Fatalf("ListTenants: %v", err)
	}

	if len(tenants) != 3 {
		t.Fatalf("got %d tenants, want 3", len(tenants))
	}

	want := []string{"alpha", "bravo", "charlie"}
	for i, tenant := range tenants {
		if tenant.ID != want[i] {
			t.Errorf("tenant[%d].ID = %q, want %q", i, tenant.ID, want[i])
		}
	}
}

func TestMemAdminStore_CreateGetDelete(t *testing.T) {
	t.Parallel()

	store := NewMemAdminStore()
	ctx := context.Background()

	tenant := Tenant{ID: "test-tenant", BotToken: "secret"}
	if err := store.CreateTenant(ctx, tenant); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	got, err := store.GetTenant(ctx, "test-tenant")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if got.BotToken != "secret" {
		t.Errorf("got BotToken %q, want %q", got.BotToken, "secret")
	}

	if err := store.DeleteTenant(ctx, "test-tenant"); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}

	_, err = store.GetTenant(ctx, "test-tenant")
	if err == nil {
		t.Error("expected error after deletion")
	}
}

func TestMemAdminStore_GetTenant_NotFound(t *testing.T) {
	t.Parallel()

	store := NewMemAdminStore()
	_, err := store.GetTenant(context.Background(), "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent tenant")
	}
}

func TestMemAdminStore_DeleteTenant_NotFound(t *testing.T) {
	t.Parallel()

	store := NewMemAdminStore()
	err := store.DeleteTenant(context.Background(), "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent tenant")
	}
}

func TestMemAdminStore_CreateTenant_Duplicate(t *testing.T) {
	t.Parallel()

	store := NewMemAdminStore()
	ctx := context.Background()

	_ = store.CreateTenant(ctx, Tenant{ID: "dup"})
	err := store.CreateTenant(ctx, Tenant{ID: "dup"})
	if err == nil {
		t.Error("expected error for duplicate create")
	}
}
