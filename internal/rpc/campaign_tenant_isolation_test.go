package rpc_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// The RPC-tier cross-tenant isolation regressions (#473): a principal in tenant B
// must never reach a campaign owned by tenant A. Every campaign-scoped handler
// threads auth.TenantID(ctx) into a scoped store read/write, so a foreign id
// resolves to CodeNotFound — never a permission error that would confirm the id
// exists (self-signup design §0a). The fakes enforce the WHERE tenant_id guard
// (a campaign whose TenantID differs from the ctx tenant reads back absent).

// TestSetActiveCampaign_CrossTenantNotFound: tenant B selecting tenant A's campaign
// is CodeNotFound and never persists the durable selection.
func TestSetActiveCampaign_CrossTenantNotFound(t *testing.T) {
	t.Parallel()
	tenantA := uuid.New()
	tenantB := uuid.New()
	victim := storage.Campaign{ID: uuid.New(), TenantID: tenantA, Name: "A's campaign"}
	store := newFakeManagementStore()
	store.campaignsByID = map[uuid.UUID]storage.Campaign{victim.ID: victim}
	// The caller is in tenant B.
	client := mgmtClient(t, store, storage.User{DiscordUserID: "intruder"}, tenantB, nil)

	_, err := client.SetActiveCampaign(context.Background(),
		connect.NewRequest(&managementv1.SetActiveCampaignRequest{CampaignId: victim.ID.String()}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("SetActiveCampaign(cross-tenant) code = %v, want NotFound", got)
	}
	if len(store.setActiveCalls) != 0 {
		t.Errorf("cross-tenant selection must not persist: %+v", store.setActiveCalls)
	}
}

// TestUpdateCampaign_CrossTenantNotFound: tenant B renaming tenant A's campaign is
// CodeNotFound (the scoped UPDATE matches nothing).
func TestUpdateCampaign_CrossTenantNotFound(t *testing.T) {
	t.Parallel()
	tenantA := uuid.New()
	tenantB := uuid.New()
	victim := storage.Campaign{ID: uuid.New(), TenantID: tenantA, Name: "A's campaign"}
	store := newFakeManagementStore()
	store.campaignsByID = map[uuid.UUID]storage.Campaign{victim.ID: victim}
	client := mgmtClient(t, store, storage.User{DiscordUserID: "intruder"}, tenantB, nil)

	_, err := client.UpdateCampaign(context.Background(),
		connect.NewRequest(&managementv1.UpdateCampaignRequest{Id: victim.ID.String(), Name: "Hijacked"}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("UpdateCampaign(cross-tenant) code = %v, want NotFound", got)
	}
}

// TestArchiveDeleteUnarchive_CrossTenantNotFound: the destructive archive-lifecycle
// handlers all refuse a foreign-tenant id with CodeNotFound, and thread the ctx
// tenant into the store (so the scoped WHERE clause does the refusing).
func TestArchiveDeleteUnarchive_CrossTenantNotFound(t *testing.T) {
	t.Parallel()
	tenantB := uuid.New()
	foreignID := uuid.New()
	store := newFakeArchiveStore()
	store.archiveErr = storage.ErrNotFound
	store.unarchiveErr = storage.ErrNotFound
	store.deleteCampaignErr = storage.ErrNotFound
	client := archiveClient(t, store, storage.User{DiscordUserID: "intruder"}, tenantB, nil)
	ctx := context.Background()

	if _, err := client.ArchiveCampaign(ctx,
		connect.NewRequest(&managementv1.ArchiveCampaignRequest{Id: foreignID.String()})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("ArchiveCampaign(cross-tenant) code = %v, want NotFound", connect.CodeOf(err))
	}
	if store.archiveTenant != tenantB {
		t.Errorf("archive threaded tenant %s, want ctx tenant %s", store.archiveTenant, tenantB)
	}

	if _, err := client.UnarchiveCampaign(ctx,
		connect.NewRequest(&managementv1.UnarchiveCampaignRequest{Id: foreignID.String()})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("UnarchiveCampaign(cross-tenant) code = %v, want NotFound", connect.CodeOf(err))
	}
	if store.unarchiveTenant != tenantB {
		t.Errorf("unarchive threaded tenant %s, want ctx tenant %s", store.unarchiveTenant, tenantB)
	}

	if _, err := client.DeleteCampaign(ctx,
		connect.NewRequest(&managementv1.DeleteCampaignRequest{Id: foreignID.String()})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("DeleteCampaign(cross-tenant) code = %v, want NotFound", connect.CodeOf(err))
	}
	if store.deleteTenant != tenantB {
		t.Errorf("delete threaded tenant %s, want ctx tenant %s", store.deleteTenant, tenantB)
	}
}

// TestListCampaigns_ScopedToTenant: the picker list threads the ctx tenant into the
// scoped store read (so the query returns only the caller's own campaigns, #473).
func TestListCampaigns_ScopedToTenant(t *testing.T) {
	t.Parallel()
	tenantID := uuid.New()
	store := newFakeManagementStore()
	store.campaignList = []storage.Campaign{{ID: uuid.New(), TenantID: tenantID, Name: "Mine"}}
	client := mgmtClient(t, store, storage.User{DiscordUserID: "op"}, tenantID, nil)

	if _, err := client.ListCampaigns(context.Background(),
		connect.NewRequest(&managementv1.ListCampaignsRequest{})); err != nil {
		t.Fatalf("ListCampaigns: %v", err)
	}
	if store.listTenant != tenantID {
		t.Errorf("ListCampaigns threaded tenant %s, want ctx tenant %s", store.listTenant, tenantID)
	}

	// include_archived routes to the archive-inclusive scoped list, still tenant-scoped.
	store.listTenant = uuid.Nil
	if _, err := client.ListCampaigns(context.Background(),
		connect.NewRequest(&managementv1.ListCampaignsRequest{IncludeArchived: true})); err != nil {
		t.Fatalf("ListCampaigns(include_archived): %v", err)
	}
	if store.listTenant != tenantID {
		t.Errorf("ListAllCampaigns threaded tenant %s, want ctx tenant %s", store.listTenant, tenantID)
	}
}
