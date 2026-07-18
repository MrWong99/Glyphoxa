//go:build integration

package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestCampaignTenantScopeIsolation pins the #473 cross-tenant isolation for every
// tenant-scoped campaign read/write on the web/RPC path: a campaign owned by one
// tenant is invisible from another — reads and writes yield ErrNotFound, never a
// permission-style error that would confirm the id exists (self-signup design §0a).
//
// It seeds ONE store (a single testcontainer) with two tenants each owning one
// active campaign, and drives every scoped surface as subtests. The read subtests
// share campaignA/campaignB; the mutation subtests each create their OWN throwaway
// campaign so archiving/deleting never disturbs the shared fixtures. Consolidated
// into one container to keep the (container-per-test) storage suite under its
// wall-clock budget.
func TestCampaignTenantScopeIsolation(t *testing.T) {
	st := migrated(t)
	ctx := context.Background()

	tenantA, err := st.CreateTenant(ctx, "Tenant A")
	if err != nil {
		t.Fatalf("create tenant A: %v", err)
	}
	tenantB, err := st.CreateTenant(ctx, "Tenant B")
	if err != nil {
		t.Fatalf("create tenant B: %v", err)
	}
	newCampaign := func(tenantID uuid.UUID, name string) uuid.UUID {
		t.Helper()
		id, err := st.CreateCampaign(ctx, storage.NewCampaign{TenantID: tenantID, Name: name, System: "dnd5e", Language: "en"})
		if err != nil {
			t.Fatalf("create campaign %q: %v", name, err)
		}
		return id
	}
	campaignA := newCampaign(tenantA, "Alpha")
	campaignB := newCampaign(tenantB, "Beta")

	t.Run("GetCampaignInTenant", func(t *testing.T) {
		got, err := st.GetCampaignInTenant(ctx, tenantA, campaignA)
		if err != nil || got.ID != campaignA {
			t.Fatalf("GetCampaignInTenant(own) = %s, %v; want %s", got.ID, err, campaignA)
		}
		if _, err := st.GetCampaignInTenant(ctx, tenantB, campaignA); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("GetCampaignInTenant(foreign) = %v, want ErrNotFound", err)
		}
		if _, err := st.GetCampaignInTenant(ctx, tenantA, uuid.New()); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("GetCampaignInTenant(unknown) = %v, want ErrNotFound", err)
		}
	})

	t.Run("GetActiveCampaignInTenant", func(t *testing.T) {
		a, err := st.GetActiveCampaignInTenant(ctx, tenantA)
		if err != nil || a.ID != campaignA {
			t.Errorf("tenant A fallback = %s, %v; want its own %s", a.ID, err, campaignA)
		}
		b, err := st.GetActiveCampaignInTenant(ctx, tenantB)
		if err != nil || b.ID != campaignB {
			t.Errorf("tenant B fallback = %s, %v; want its own %s", b.ID, err, campaignB)
		}
		// A third, campaign-less tenant resolves to ErrNotFound — never another
		// tenant's most-recent.
		empty, err := st.CreateTenant(ctx, "Empty")
		if err != nil {
			t.Fatalf("create empty tenant: %v", err)
		}
		if _, err := st.GetActiveCampaignInTenant(ctx, empty); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("GetActiveCampaignInTenant(empty) = %v, want ErrNotFound", err)
		}
	})

	t.Run("GetActiveCampaignForUserInTenant", func(t *testing.T) {
		// The operator durably selected tenant A's campaign (the write is FK-checked,
		// not tenant-checked — matching /glyphoxa use, migration 00014).
		const op = "op-777"
		if err := st.SetActiveCampaign(ctx, op, campaignA); err != nil {
			t.Fatalf("SetActiveCampaign: %v", err)
		}
		got, err := st.GetActiveCampaignForUserInTenant(ctx, tenantA, op)
		if err != nil || got.ID != campaignA {
			t.Errorf("scoped selection = %s, %v; want %s", got.ID, err, campaignA)
		}
		// From tenant B the selection points at A's campaign → absent (no pivot).
		if _, err := st.GetActiveCampaignForUserInTenant(ctx, tenantB, op); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("GetActiveCampaignForUserInTenant(B) = %v, want ErrNotFound", err)
		}
	})

	t.Run("ListCampaignsInTenant", func(t *testing.T) {
		a, err := st.ListCampaignsInTenant(ctx, tenantA)
		if err != nil {
			t.Fatalf("ListCampaignsInTenant(A): %v", err)
		}
		for _, c := range a {
			if c.TenantID != tenantA {
				t.Errorf("tenant A list leaked campaign from tenant %s", c.TenantID)
			}
		}
		b, err := st.ListAllCampaignsInTenant(ctx, tenantB)
		if err != nil {
			t.Fatalf("ListAllCampaignsInTenant(B): %v", err)
		}
		for _, c := range b {
			if c.TenantID != tenantB {
				t.Errorf("tenant B list-all leaked campaign from tenant %s", c.TenantID)
			}
		}
	})

	t.Run("UpdateCampaign", func(t *testing.T) {
		victim := newCampaign(tenantA, "Rename Victim")
		// Tenant B cannot rename tenant A's campaign.
		if _, err := st.UpdateCampaign(ctx, storage.CampaignUpdate{
			TenantID: tenantB, ID: victim, Name: "Hijacked", System: "x", Language: "x",
		}); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("UpdateCampaign(cross-tenant) = %v, want ErrNotFound", err)
		}
		got, err := st.GetCampaignInTenant(ctx, tenantA, victim)
		if err != nil || got.Name != "Rename Victim" {
			t.Errorf("victim after cross-tenant update = %+v, %v; want unchanged name", got, err)
		}
		// The owner's update lands.
		upd, err := st.UpdateCampaign(ctx, storage.CampaignUpdate{
			TenantID: tenantA, ID: victim, Name: "Renamed", System: "dnd5e", Language: "en",
		})
		if err != nil || upd.Name != "Renamed" {
			t.Errorf("own update = %+v, %v; want name Renamed", upd, err)
		}
	})

	t.Run("ArchiveUnarchive", func(t *testing.T) {
		victim := newCampaign(tenantA, "Archive Victim")
		if _, err := st.ArchiveCampaign(ctx, tenantB, victim); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("ArchiveCampaign(cross-tenant) = %v, want ErrNotFound", err)
		}
		got, err := st.GetCampaignInTenant(ctx, tenantA, victim)
		if err != nil || got.ArchivedAt != nil {
			t.Errorf("victim after cross-tenant archive = %+v, %v; want untouched/active", got, err)
		}
		if _, err := st.ArchiveCampaign(ctx, tenantA, victim); err != nil {
			t.Fatalf("ArchiveCampaign(own): %v", err)
		}
		if _, err := st.UnarchiveCampaign(ctx, tenantB, victim); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("UnarchiveCampaign(cross-tenant) = %v, want ErrNotFound", err)
		}
		if _, err := st.UnarchiveCampaign(ctx, tenantA, victim); err != nil {
			t.Fatalf("UnarchiveCampaign(own): %v", err)
		}
	})

	t.Run("DeleteCampaign", func(t *testing.T) {
		victim := newCampaign(tenantA, "Delete Victim")
		if _, err := st.ArchiveCampaign(ctx, tenantA, victim); err != nil {
			t.Fatalf("ArchiveCampaign(own): %v", err)
		}
		// Tenant B cannot see (let alone delete) A's archived campaign — a foreign id
		// disambiguates to ErrNotFound, never ErrNotArchived.
		if err := st.DeleteCampaign(ctx, tenantB, victim); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("DeleteCampaign(cross-tenant) = %v, want ErrNotFound", err)
		}
		if _, err := st.GetCampaignInTenant(ctx, tenantA, victim); err != nil {
			t.Errorf("victim gone after cross-tenant delete: %v", err)
		}
		// Owner deletes its own archived campaign.
		if err := st.DeleteCampaign(ctx, tenantA, victim); err != nil {
			t.Fatalf("DeleteCampaign(own): %v", err)
		}
		if _, err := st.GetCampaignInTenant(ctx, tenantA, victim); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("campaign after own delete = %v, want ErrNotFound", err)
		}
	})
}
