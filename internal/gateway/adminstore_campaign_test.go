package gateway

import (
	"context"
	"testing"
)

func TestMemAdminStore_CampaignIDAndDMRoleID_RoundTrip(t *testing.T) {
	t.Parallel()

	store := NewMemAdminStore()
	ctx := context.Background()

	tenant := Tenant{
		ID:         "test-tenant",
		CampaignID: "campaign-42",
		DMRoleID:   "role-99",
	}
	if err := store.CreateTenant(ctx, tenant); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	got, err := store.GetTenant(ctx, "test-tenant")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if got.CampaignID != "campaign-42" {
		t.Errorf("CampaignID = %q, want %q", got.CampaignID, "campaign-42")
	}
	if got.DMRoleID != "role-99" {
		t.Errorf("DMRoleID = %q, want %q", got.DMRoleID, "role-99")
	}
}

func TestMemAdminStore_UpdateCampaignID(t *testing.T) {
	t.Parallel()

	store := NewMemAdminStore()
	ctx := context.Background()

	_ = store.CreateTenant(ctx, Tenant{ID: "upd", CampaignID: "old"})

	updated := Tenant{ID: "upd", CampaignID: "new-campaign", DMRoleID: "new-role"}
	if err := store.UpdateTenant(ctx, updated); err != nil {
		t.Fatalf("UpdateTenant: %v", err)
	}

	got, _ := store.GetTenant(ctx, "upd")
	if got.CampaignID != "new-campaign" {
		t.Errorf("CampaignID = %q, want %q", got.CampaignID, "new-campaign")
	}
	if got.DMRoleID != "new-role" {
		t.Errorf("DMRoleID = %q, want %q", got.DMRoleID, "new-role")
	}
}

func TestMemAdminStore_ListTenants_IncludesCampaignFields(t *testing.T) {
	t.Parallel()

	store := NewMemAdminStore()
	ctx := context.Background()

	_ = store.CreateTenant(ctx, Tenant{ID: "a", CampaignID: "c1", DMRoleID: "r1"})
	_ = store.CreateTenant(ctx, Tenant{ID: "b", CampaignID: "c2", DMRoleID: "r2"})

	tenants, err := store.ListTenants(ctx)
	if err != nil {
		t.Fatalf("ListTenants: %v", err)
	}
	if len(tenants) != 2 {
		t.Fatalf("got %d tenants, want 2", len(tenants))
	}

	// tenants sorted by ID: a, b
	if tenants[0].CampaignID != "c1" {
		t.Errorf("tenants[0].CampaignID = %q, want %q", tenants[0].CampaignID, "c1")
	}
	if tenants[1].DMRoleID != "r2" {
		t.Errorf("tenants[1].DMRoleID = %q, want %q", tenants[1].DMRoleID, "r2")
	}
}
