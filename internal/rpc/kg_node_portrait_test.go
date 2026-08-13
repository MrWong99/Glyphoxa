package rpc_test

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/blob"
	"github.com/MrWong99/Glyphoxa/internal/portraitgen"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// The Node portrait handlers (#590). The storage layer is covered against a
// real Postgres elsewhere; what is tested HERE is the part storage cannot see —
// the ordering between the blob seam and the row, who cleans up when the two
// disagree, and the draft flow's write-nothing contract.

// fakePortraitStore is the nodePortraitStore seam. setErr forces the row
// failure whose orphan cleanup is the point of the blob-first ordering.
type fakePortraitStore struct {
	node   storage.KGNode
	oldKey string
	setErr error

	setCampaign uuid.UUID
	setKeys     []string
}

func (f *fakePortraitStore) SetNodePortrait(_ context.Context, campaignID, nodeID uuid.UUID, blobKey string) (storage.KGNode, string, error) {
	f.setCampaign = campaignID
	f.setKeys = append(f.setKeys, blobKey)
	if f.setErr != nil {
		return storage.KGNode{}, "", f.setErr
	}
	if nodeID != f.node.ID {
		return storage.KGNode{}, "", storage.ErrNotFound
	}
	old := f.node.PortraitBlobKey
	if f.oldKey != "" {
		old = f.oldKey
	}
	f.node.PortraitBlobKey = blobKey
	return f.node, old, nil
}

// fakePortraitGen is the PortraitGenerator seam.
type fakePortraitGen struct {
	res   portraitgen.Result
	err   error
	calls int
	last  portraitgen.Input
}

func (f *fakePortraitGen) Generate(_ context.Context, _ storage.Campaign, in portraitgen.Input) (portraitgen.Result, error) {
	f.calls++
	f.last = in
	return f.res, f.err
}

// portraitClient composes a CampaignServer over the portrait fake plus a blob
// seam and an optional generator.
func portraitClient(t *testing.T, store *fakePortraitStore, blobs blob.Store, gen rpc.PortraitGenerator) managementv1connect.CampaignServiceClient {
	t.Helper()
	campaignID := uuid.New()
	tenantID := uuid.New()
	active := &fakeActive{campaign: storage.Campaign{ID: campaignID, TenantID: tenantID, Name: "Saltmarsh"}}
	srv := rpc.NewCampaignServerWith(rpc.CampaignStores{Active: active, Portraits: store})
	if blobs != nil {
		srv.SetBlobs(blobs)
	}
	if gen != nil {
		srv.SetPortraitGenerator(gen)
	}
	inject := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			ctx = auth.WithUser(ctx, storage.User{DiscordUserID: "999"})
			ctx = auth.WithTenant(ctx, tenantID)
			return next(ctx, req)
		}
	})
	return campaignClientServe(t, srv, connect.WithInterceptors(inject))
}

func portraitNode() storage.KGNode {
	return storage.KGNode{ID: uuid.New(), CampaignID: uuid.New(), Type: storage.KGNodeNPC, Name: "Bart"}
}

// TestGenerateNodePortrait_ReturnsDraftAndStoresNothing: the draft flow's
// whole contract — bytes to the browser, no blob, no row.
func TestGenerateNodePortrait_ReturnsDraftAndStoresNothing(t *testing.T) {
	t.Parallel()
	node := portraitNode()
	store := &fakePortraitStore{node: node}
	blobs := newMemBlobs()
	gen := &fakePortraitGen{res: portraitgen.Result{
		Data: []byte{1, 2, 3}, ContentType: "image/png", Model: "m", Prompt: "the full prompt",
	}}
	client := portraitClient(t, store, blobs, gen)

	res, err := client.GenerateNodePortrait(context.Background(),
		connect.NewRequest(&managementv1.GenerateNodePortraitRequest{
			NodeId: node.ID.String(), Prompt: "mid-laugh",
		}))
	if err != nil {
		t.Fatalf("GenerateNodePortrait: %v", err)
	}
	if len(res.Msg.GetImageBytes()) == 0 || res.Msg.GetPrompt() != "the full prompt" {
		t.Fatalf("draft came back wrong: %+v", res.Msg)
	}
	if gen.last.NodeID != node.ID || gen.last.Prompt != "mid-laugh" {
		t.Errorf("engine input = %+v", gen.last)
	}
	if len(blobs.log()) != 0 {
		t.Errorf("the draft touched the blob seam: %v", blobs.log())
	}
	if len(store.setKeys) != 0 {
		t.Error("the draft touched the row")
	}
}

// TestGenerateNodePortrait_NoKeyIsFailedPrecondition: the GM is told to save a
// key, with the code the Configuration screen's deep-link keys off.
func TestGenerateNodePortrait_NoKeyIsFailedPrecondition(t *testing.T) {
	t.Parallel()
	node := portraitNode()
	gen := &fakePortraitGen{err: portraitgen.ErrNotConfigured}
	client := portraitClient(t, &fakePortraitStore{node: node}, newMemBlobs(), gen)

	_, err := client.GenerateNodePortrait(context.Background(),
		connect.NewRequest(&managementv1.GenerateNodePortraitRequest{NodeId: node.ID.String()}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("code = %v, want FailedPrecondition (%v)", connect.CodeOf(err), err)
	}
	if !strings.Contains(err.Error(), "Configuration") {
		t.Errorf("the refusal is not actionable: %v", err)
	}
}

// TestGenerateNodePortrait_PrivateOrMissingEntryIsNotFound: the seed read's
// gm_private filter surfaces as the same 404 a missing entry gets — existence
// is never leaked, and the message says both possibilities.
func TestGenerateNodePortrait_PrivateOrMissingEntryIsNotFound(t *testing.T) {
	t.Parallel()
	node := portraitNode()
	gen := &fakePortraitGen{err: storage.ErrNotFound}
	client := portraitClient(t, &fakePortraitStore{node: node}, newMemBlobs(), gen)

	_, err := client.GenerateNodePortrait(context.Background(),
		connect.NewRequest(&managementv1.GenerateNodePortraitRequest{NodeId: node.ID.String()}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("code = %v, want NotFound (%v)", connect.CodeOf(err), err)
	}
}

// TestGenerateNodePortrait_UnwiredEngineIsUnavailable: a deployment with no
// image provider reports honestly; portraits still upload.
func TestGenerateNodePortrait_UnwiredEngineIsUnavailable(t *testing.T) {
	t.Parallel()
	node := portraitNode()
	client := portraitClient(t, &fakePortraitStore{node: node}, newMemBlobs(), nil)

	_, err := client.GenerateNodePortrait(context.Background(),
		connect.NewRequest(&managementv1.GenerateNodePortraitRequest{NodeId: node.ID.String()}))
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("code = %v, want Unavailable (%v)", connect.CodeOf(err), err)
	}
}

// TestSetNodePortrait_BlobFirstRowSecondOldKeyReleased: the apply path's whole
// ordering contract in one flow — put the new bytes, repoint the row, then and
// only then drop the superseded blob.
func TestSetNodePortrait_BlobFirstRowSecondOldKeyReleased(t *testing.T) {
	t.Parallel()
	node := portraitNode()
	tenantForKey := uuid.New()
	oldKey, err := blob.Key(tenantForKey, "node", uuid.New(), "portrait")
	if err != nil {
		t.Fatalf("blob.Key: %v", err)
	}
	node.PortraitBlobKey = oldKey
	store := &fakePortraitStore{node: node}
	blobs := newMemBlobs()
	blobs.objects[oldKey] = []byte("old bytes")
	client := portraitClient(t, store, blobs, nil)

	res, err := client.SetNodePortrait(context.Background(),
		connect.NewRequest(&managementv1.SetNodePortraitRequest{
			NodeId: node.ID.String(), ImageBytes: []byte{9, 9, 9}, ContentType: "image/png",
		}))
	if err != nil {
		t.Fatalf("SetNodePortrait: %v", err)
	}
	if !res.Msg.GetNode().GetHasPortrait() {
		t.Error("the response node does not report its portrait")
	}

	log := blobs.log()
	if len(log) != 2 || !strings.HasPrefix(log[0], "put:") || log[1] != "delete:"+oldKey {
		t.Fatalf("seam order = %v, want [put:<new>, delete:<old>]", log)
	}
	// The row was repointed BETWEEN the two seam calls, at the new key.
	if len(store.setKeys) != 1 || store.setKeys[0] == "" || store.setKeys[0] == oldKey {
		t.Fatalf("row keys = %v, want one fresh key", store.setKeys)
	}
	if keys := blobs.keys(); len(keys) != 1 || keys[0] != store.setKeys[0] {
		t.Errorf("surviving blobs = %v, want exactly the row's key", keys)
	}
}

// TestSetNodePortrait_RowFailureDropsTheOrphan: there is no background
// reconciliation for node keys — the inline delete IS the cleanup.
func TestSetNodePortrait_RowFailureDropsTheOrphan(t *testing.T) {
	t.Parallel()
	node := portraitNode()
	store := &fakePortraitStore{node: node, setErr: storage.ErrNotFound}
	blobs := newMemBlobs()
	client := portraitClient(t, store, blobs, nil)

	_, err := client.SetNodePortrait(context.Background(),
		connect.NewRequest(&managementv1.SetNodePortraitRequest{
			NodeId: node.ID.String(), ImageBytes: []byte{9}, ContentType: "image/png",
		}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("code = %v, want NotFound (%v)", connect.CodeOf(err), err)
	}
	if keys := blobs.keys(); len(keys) != 0 {
		t.Errorf("orphaned bytes survived the failed row write: %v", keys)
	}
}

// TestSetNodePortrait_RejectsNonImagesBeforeTheSeam: validation happens before
// any byte moves.
func TestSetNodePortrait_RejectsNonImagesBeforeTheSeam(t *testing.T) {
	t.Parallel()
	node := portraitNode()
	blobs := newMemBlobs()
	client := portraitClient(t, &fakePortraitStore{node: node}, blobs, nil)

	for name, req := range map[string]*managementv1.SetNodePortraitRequest{
		"empty bytes": {NodeId: node.ID.String(), ContentType: "image/png"},
		"not an image": {
			NodeId: node.ID.String(), ImageBytes: []byte("<svg onload=alert(1)>"), ContentType: "text/html",
		},
		// image/* is not enough (#591 review): SVG is scriptable, and the portrait
		// mount serves the stored type same-origin — accepting it would be stored
		// XSS. The allowlist is raster-only.
		"scriptable svg": {
			NodeId: node.ID.String(), ImageBytes: []byte("<svg onload=alert(1)/>"), ContentType: "image/svg+xml",
		},
	} {
		_, err := client.SetNodePortrait(context.Background(), connect.NewRequest(req))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: code = %v, want InvalidArgument", name, connect.CodeOf(err))
		}
	}
	if len(blobs.log()) != 0 {
		t.Errorf("a rejected upload touched the seam: %v", blobs.log())
	}
}

// TestClearNodePortrait_ClearsTheRowAndReleasesTheBytes.
func TestClearNodePortrait_ClearsTheRowAndReleasesTheBytes(t *testing.T) {
	t.Parallel()
	node := portraitNode()
	oldKey, err := blob.Key(uuid.New(), "node", uuid.New(), "portrait")
	if err != nil {
		t.Fatalf("blob.Key: %v", err)
	}
	node.PortraitBlobKey = oldKey
	store := &fakePortraitStore{node: node}
	blobs := newMemBlobs()
	blobs.objects[oldKey] = []byte("old bytes")
	client := portraitClient(t, store, blobs, nil)

	res, err := client.ClearNodePortrait(context.Background(),
		connect.NewRequest(&managementv1.ClearNodePortraitRequest{NodeId: node.ID.String()}))
	if err != nil {
		t.Fatalf("ClearNodePortrait: %v", err)
	}
	if res.Msg.GetNode().GetHasPortrait() {
		t.Error("the response node still reports a portrait")
	}
	if len(store.setKeys) != 1 || store.setKeys[0] != "" {
		t.Errorf("row keys = %v, want one clearing write", store.setKeys)
	}
	if keys := blobs.keys(); len(keys) != 0 {
		t.Errorf("the cleared portrait's bytes were not released: %v", keys)
	}
}

// TestDeleteNode_ReleasesThePortraitBlob: deleting an entry releases its
// portrait bytes through the seam (ADR-0048) — after the DELETE, nothing in the
// database names them.
func TestDeleteNode_ReleasesThePortraitBlob(t *testing.T) {
	t.Parallel()
	key, err := blob.Key(uuid.New(), "node", uuid.New(), "portrait")
	if err != nil {
		t.Fatalf("blob.Key: %v", err)
	}
	node := storage.KGNode{ID: uuid.New(), Type: storage.KGNodeNPC, Name: "Bart", PortraitBlobKey: key}
	active := newFakeActive()
	active.campaign = storage.Campaign{ID: uuid.New(), Name: "Saltmarsh"}
	store := &fakeKGNodeStore{fakeActive: active, nodes: []storage.KGNode{node}}
	blobs := newMemBlobs()
	blobs.objects[key] = []byte("bytes")

	srv := rpc.NewCampaignServerWith(rpc.CampaignStores{Active: store, KGNodes: store})
	srv.SetBlobs(blobs)
	client := campaignClientServe(t, srv)

	if _, err := client.DeleteNode(context.Background(),
		connect.NewRequest(&managementv1.DeleteNodeRequest{Id: node.ID.String()})); err != nil {
		t.Fatalf("DeleteNode: %v", err)
	}
	if keys := blobs.keys(); len(keys) != 0 {
		t.Errorf("the deleted entry's portrait bytes were not released: %v", keys)
	}
}
