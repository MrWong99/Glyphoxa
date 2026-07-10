package rpc_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/highlight"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestArchiveCampaign_HappyPath: a valid, non-live campaign archives and the
// archived_at timestamp maps onto the wire.
func TestArchiveCampaign_HappyPath(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	id := uuid.New()
	archivedAt := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	store.archiveResult = storage.Campaign{ID: id, TenantID: uuid.New(), Name: "Old One", ArchivedAt: &archivedAt}
	client := mgmtClient(t, store, storage.User{DiscordUserID: "999"}, uuid.New(), nil)

	resp, err := client.ArchiveCampaign(context.Background(),
		connect.NewRequest(&managementv1.ArchiveCampaignRequest{Id: id.String()}))
	if err != nil {
		t.Fatalf("ArchiveCampaign: %v", err)
	}
	if len(store.archiveCalls) != 1 || store.archiveCalls[0] != id {
		t.Errorf("archive calls = %+v, want [%s]", store.archiveCalls, id)
	}
	if got := resp.Msg.GetCampaign().GetArchivedAt().AsTime(); !got.Equal(archivedAt) {
		t.Errorf("archived_at on wire = %v, want %v", got, archivedAt)
	}
}

func TestArchiveCampaign_InvalidID(t *testing.T) {
	t.Parallel()
	client := mgmtClient(t, newFakeStore(), storage.User{DiscordUserID: "999"}, uuid.New(), nil)
	_, err := client.ArchiveCampaign(context.Background(),
		connect.NewRequest(&managementv1.ArchiveCampaignRequest{Id: "nope"}))
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", got)
	}
}

func TestArchiveCampaign_UnknownIDNotFound(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.archiveErr = storage.ErrNotFound
	client := mgmtClient(t, store, storage.User{DiscordUserID: "999"}, uuid.New(), nil)
	_, err := client.ArchiveCampaign(context.Background(),
		connect.NewRequest(&managementv1.ArchiveCampaignRequest{Id: uuid.New().String()}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound", got)
	}
}

// TestArchiveCampaign_LiveSessionRefused: the campaign backing the LIVE Voice
// Session cannot be archived out from under it (#265).
func TestArchiveCampaign_LiveSessionRefused(t *testing.T) {
	t.Parallel()
	live := uuid.New()
	store := newFakeStore()
	client := mgmtClient(t, store, storage.User{DiscordUserID: "999"}, uuid.New(), liveMgr(live))

	_, err := client.ArchiveCampaign(context.Background(),
		connect.NewRequest(&managementv1.ArchiveCampaignRequest{Id: live.String()}))
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Errorf("code = %v, want FailedPrecondition", got)
	}
	// The store must never be asked to archive the live campaign.
	if len(store.archiveCalls) != 0 {
		t.Errorf("live campaign must not reach the store: %+v", store.archiveCalls)
	}
}

// TestArchiveCampaign_OtherCampaignWhileLiveAllowed: a DIFFERENT campaign than the
// live one archives fine — the guard is scoped to the live campaign only.
func TestArchiveCampaign_OtherCampaignWhileLiveAllowed(t *testing.T) {
	t.Parallel()
	live := uuid.New()
	other := uuid.New()
	store := newFakeStore()
	store.archiveResult = storage.Campaign{ID: other, Name: "Other"}
	client := mgmtClient(t, store, storage.User{DiscordUserID: "999"}, uuid.New(), liveMgr(live))

	if _, err := client.ArchiveCampaign(context.Background(),
		connect.NewRequest(&managementv1.ArchiveCampaignRequest{Id: other.String()})); err != nil {
		t.Fatalf("archiving a non-live campaign while live: %v", err)
	}
	if len(store.archiveCalls) != 1 || store.archiveCalls[0] != other {
		t.Errorf("archive calls = %+v, want [%s]", store.archiveCalls, other)
	}
}

// TestUnarchiveCampaign_HappyPath: unarchive returns the reactivated campaign
// (archived_at unset) with no live-guard.
func TestUnarchiveCampaign_HappyPath(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	id := uuid.New()
	store.unarchiveResult = storage.Campaign{ID: id, Name: "Back"}
	client := mgmtClient(t, store, storage.User{DiscordUserID: "999"}, uuid.New(), nil)

	resp, err := client.UnarchiveCampaign(context.Background(),
		connect.NewRequest(&managementv1.UnarchiveCampaignRequest{Id: id.String()}))
	if err != nil {
		t.Fatalf("UnarchiveCampaign: %v", err)
	}
	if resp.Msg.GetCampaign().GetArchivedAt() != nil {
		t.Errorf("archived_at should be unset after unarchive: %+v", resp.Msg.GetCampaign())
	}
	if len(store.unarchiveCalls) != 1 || store.unarchiveCalls[0] != id {
		t.Errorf("unarchive calls = %+v, want [%s]", store.unarchiveCalls, id)
	}
}

func TestUnarchiveCampaign_UnknownIDNotFound(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.unarchiveErr = storage.ErrNotFound
	client := mgmtClient(t, store, storage.User{DiscordUserID: "999"}, uuid.New(), nil)
	_, err := client.UnarchiveCampaign(context.Background(),
		connect.NewRequest(&managementv1.UnarchiveCampaignRequest{Id: uuid.New().String()}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound", got)
	}
}

// TestDeleteCampaign_HappyPath: a valid, archived, non-live campaign deletes.
func TestDeleteCampaign_HappyPath(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	id := uuid.New()
	client := mgmtClient(t, store, storage.User{DiscordUserID: "999"}, uuid.New(), nil)

	if _, err := client.DeleteCampaign(context.Background(),
		connect.NewRequest(&managementv1.DeleteCampaignRequest{Id: id.String()})); err != nil {
		t.Fatalf("DeleteCampaign: %v", err)
	}
	if len(store.deleteCalls) != 1 || store.deleteCalls[0] != id {
		t.Errorf("delete calls = %+v, want [%s]", store.deleteCalls, id)
	}
}

// fakeClipSweeper records the highlight-clip sweep a campaign hard delete runs.
type fakeClipSweeper struct {
	keys    []string
	deleted []string
	listErr error
}

func (f *fakeClipSweeper) CampaignClipKeys(context.Context, uuid.UUID) ([]string, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.keys, nil
}

func (f *fakeClipSweeper) DeleteClip(_ context.Context, key string) error {
	f.deleted = append(f.deleted, key)
	return nil
}

// TestDeleteCampaign_SweepsHighlightClips: a successful hard delete drops every
// highlight clip through the blob seam (#308, ADR-0048).
func TestDeleteCampaign_SweepsHighlightClips(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	id := uuid.New()
	sweeper := &fakeClipSweeper{keys: []string{"k1", "k2"}}
	srv := rpc.NewCampaignServer(store)
	srv.SetHighlightClipSweeper(sweeper)

	if _, err := srv.DeleteCampaign(context.Background(),
		connect.NewRequest(&managementv1.DeleteCampaignRequest{Id: id.String()})); err != nil {
		t.Fatalf("DeleteCampaign: %v", err)
	}
	if len(store.deleteCalls) != 1 {
		t.Fatalf("campaign not deleted: %+v", store.deleteCalls)
	}
	if len(sweeper.deleted) != 2 || sweeper.deleted[0] != "k1" || sweeper.deleted[1] != "k2" {
		t.Fatalf("clips not swept: %v", sweeper.deleted)
	}
}

// TestDeleteCampaign_EnqueuesDurableClipSweep: a hard delete enqueues the durable
// blob-sweep job carrying the listed clip keys, in the delete's own transaction
// (#308, ADR-0049) — the backstop that survives a crash after the row cascade.
func TestDeleteCampaign_EnqueuesDurableClipSweep(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	id := uuid.New()
	sweeper := &fakeClipSweeper{keys: []string{"k1", "k2"}}
	srv := rpc.NewCampaignServer(store)
	srv.SetHighlightClipSweeper(sweeper)

	if _, err := srv.DeleteCampaign(context.Background(),
		connect.NewRequest(&managementv1.DeleteCampaignRequest{Id: id.String()})); err != nil {
		t.Fatalf("DeleteCampaign: %v", err)
	}
	if store.deleteJobKind != highlight.JobKindSweepCampaignClips {
		t.Fatalf("durable sweep job not enqueued: kind=%q", store.deleteJobKind)
	}
	// The payload must carry exactly the listed clip keys.
	var p struct {
		ClipKeys []string `json:"clip_keys"`
	}
	if err := json.Unmarshal(store.deleteJobPayload, &p); err != nil {
		t.Fatalf("sweep payload not JSON: %v", err)
	}
	if len(p.ClipKeys) != 2 || p.ClipKeys[0] != "k1" || p.ClipKeys[1] != "k2" {
		t.Fatalf("sweep payload keys = %v, want [k1 k2]", p.ClipKeys)
	}
}

// TestDeleteCampaign_NoClipsNoJob: a campaign with no highlight clips takes the
// plain delete path (no sweep job to enqueue).
func TestDeleteCampaign_NoClipsNoJob(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	sweeper := &fakeClipSweeper{} // no keys
	srv := rpc.NewCampaignServer(store)
	srv.SetHighlightClipSweeper(sweeper)

	if _, err := srv.DeleteCampaign(context.Background(),
		connect.NewRequest(&managementv1.DeleteCampaignRequest{Id: uuid.New().String()})); err != nil {
		t.Fatalf("DeleteCampaign: %v", err)
	}
	if store.deleteJobKind != "" {
		t.Fatalf("no-clip delete should enqueue no job, got kind=%q", store.deleteJobKind)
	}
	if len(store.deleteCalls) != 1 {
		t.Fatalf("campaign not deleted: %+v", store.deleteCalls)
	}
}

// TestDeleteCampaign_RefusedKeepsClips: a refused delete (not archived) must NOT
// drop any clips (the campaign is still live).
func TestDeleteCampaign_RefusedKeepsClips(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.deleteCampaignErr = storage.ErrNotArchived
	sweeper := &fakeClipSweeper{keys: []string{"k1"}}
	srv := rpc.NewCampaignServer(store)
	srv.SetHighlightClipSweeper(sweeper)

	_, err := srv.DeleteCampaign(context.Background(),
		connect.NewRequest(&managementv1.DeleteCampaignRequest{Id: uuid.New().String()}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("want FailedPrecondition, got %v", err)
	}
	if len(sweeper.deleted) != 0 {
		t.Fatalf("refused delete swept clips: %v", sweeper.deleted)
	}
}

func TestDeleteCampaign_InvalidID(t *testing.T) {
	t.Parallel()
	client := mgmtClient(t, newFakeStore(), storage.User{DiscordUserID: "999"}, uuid.New(), nil)
	_, err := client.DeleteCampaign(context.Background(),
		connect.NewRequest(&managementv1.DeleteCampaignRequest{Id: "nope"}))
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", got)
	}
}

// TestDeleteCampaign_NotArchivedRefused: a non-archived campaign is refused with
// FailedPrecondition (archive first, #265).
func TestDeleteCampaign_NotArchivedRefused(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.deleteCampaignErr = storage.ErrNotArchived
	client := mgmtClient(t, store, storage.User{DiscordUserID: "999"}, uuid.New(), nil)
	_, err := client.DeleteCampaign(context.Background(),
		connect.NewRequest(&managementv1.DeleteCampaignRequest{Id: uuid.New().String()}))
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Errorf("code = %v, want FailedPrecondition", got)
	}
}

func TestDeleteCampaign_UnknownIDNotFound(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.deleteCampaignErr = storage.ErrNotFound
	client := mgmtClient(t, store, storage.User{DiscordUserID: "999"}, uuid.New(), nil)
	_, err := client.DeleteCampaign(context.Background(),
		connect.NewRequest(&managementv1.DeleteCampaignRequest{Id: uuid.New().String()}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound", got)
	}
}

// TestDeleteCampaign_LiveSessionRefused: the live session's campaign cannot be
// deleted (#265) — refused before it reaches the store.
func TestDeleteCampaign_LiveSessionRefused(t *testing.T) {
	t.Parallel()
	live := uuid.New()
	store := newFakeStore()
	client := mgmtClient(t, store, storage.User{DiscordUserID: "999"}, uuid.New(), liveMgr(live))

	_, err := client.DeleteCampaign(context.Background(),
		connect.NewRequest(&managementv1.DeleteCampaignRequest{Id: live.String()}))
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Errorf("code = %v, want FailedPrecondition", got)
	}
	if len(store.deleteCalls) != 0 {
		t.Errorf("live campaign must not reach the store: %+v", store.deleteCalls)
	}
}

// TestListCampaigns_IncludeArchivedRouting pins the include_archived flag routing
// (#269): true → ListAllCampaigns (archive-inclusive), false → ListCampaigns
// (active only), and an archived campaign's archived_at maps onto the wire.
func TestListCampaigns_IncludeArchivedRouting(t *testing.T) {
	t.Parallel()
	archivedAt := time.Date(2026, 7, 9, 8, 0, 0, 0, time.UTC)
	store := newFakeStore()
	store.campaignList = []storage.Campaign{{ID: uuid.New(), Name: "Active"}}
	store.allCampaignList = []storage.Campaign{
		{ID: uuid.New(), Name: "Active"},
		{ID: uuid.New(), Name: "Archived", ArchivedAt: &archivedAt},
	}
	client := mgmtClient(t, store, storage.User{DiscordUserID: "999"}, uuid.New(), nil)

	// Default (false) → active only.
	activeResp, err := client.ListCampaigns(context.Background(),
		connect.NewRequest(&managementv1.ListCampaignsRequest{}))
	if err != nil {
		t.Fatalf("ListCampaigns(default): %v", err)
	}
	if len(activeResp.Msg.GetCampaigns()) != 1 {
		t.Errorf("default list len = %d, want 1 (active only)", len(activeResp.Msg.GetCampaigns()))
	}

	// include_archived=true → archive-inclusive, and archived_at is on the wire.
	allResp, err := client.ListCampaigns(context.Background(),
		connect.NewRequest(&managementv1.ListCampaignsRequest{IncludeArchived: true}))
	if err != nil {
		t.Fatalf("ListCampaigns(include_archived): %v", err)
	}
	got := allResp.Msg.GetCampaigns()
	if len(got) != 2 {
		t.Fatalf("archive-inclusive list len = %d, want 2", len(got))
	}
	if got[0].GetArchivedAt() != nil {
		t.Errorf("active campaign archived_at should be unset: %+v", got[0])
	}
	if got[1].GetArchivedAt() == nil || !got[1].GetArchivedAt().AsTime().Equal(archivedAt) {
		t.Errorf("archived campaign archived_at = %v, want %v", got[1].GetArchivedAt(), archivedAt)
	}
}

// TestSetActiveCampaign_ArchivedRefused: selecting an archived campaign as the
// Active Campaign is refused (#269) — it is excluded from resolution, so the
// selection would silently resolve to a different campaign.
func TestSetActiveCampaign_ArchivedRefused(t *testing.T) {
	t.Parallel()
	archivedAt := time.Date(2026, 7, 9, 8, 0, 0, 0, time.UTC)
	id := uuid.New()
	store := newFakeStore()
	store.campaignsByID = map[uuid.UUID]storage.Campaign{id: {ID: id, Name: "Archived", ArchivedAt: &archivedAt}}
	client := mgmtClient(t, store, storage.User{DiscordUserID: "999"}, uuid.New(), nil)

	_, err := client.SetActiveCampaign(context.Background(),
		connect.NewRequest(&managementv1.SetActiveCampaignRequest{CampaignId: id.String()}))
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Errorf("code = %v, want FailedPrecondition", got)
	}
	// The archived target must never be persisted as a selection.
	if len(store.setActiveCalls) != 0 {
		t.Errorf("archived target must not persist a selection: %+v", store.setActiveCalls)
	}
}
