package rpc_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/blob"
	"github.com/MrWong99/Glyphoxa/internal/imagegen"
	"github.com/MrWong99/Glyphoxa/internal/mapgen"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/providererr"
)

// The Maps and Pins handlers (#538, ADR-0060). The storage layer is covered
// against a real Postgres elsewhere; what is tested HERE is the part storage
// cannot see — the ordering between the blob seam and the row, who cleans up when
// the two disagree, and which failures the operator is told about versus which are
// reported as a server fault.

// memBlobs is an in-memory blob.Store that records every call, so a test can
// assert not just the end state but the ORDER the seam and the row were touched.
type memBlobs struct {
	mu      sync.Mutex
	objects map[string][]byte
	calls   []string
	putErr  error
	delErr  error
}

func newMemBlobs() *memBlobs { return &memBlobs{objects: map[string][]byte{}} }

func (m *memBlobs) Put(_ context.Context, key, _ string, r io.Reader, _ int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "put:"+key)
	if m.putErr != nil {
		return m.putErr
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.objects[key] = data
	return nil
}

func (m *memBlobs) Get(_ context.Context, key string) (io.ReadCloser, blob.Meta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objects[key]
	if !ok {
		return nil, blob.Meta{}, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), blob.Meta{Size: int64(len(data))}, nil
}

func (m *memBlobs) List(_ context.Context, prefix string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for k := range m.objects {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out, nil
}

func (m *memBlobs) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "delete:"+key)
	if m.delErr != nil {
		return m.delErr
	}
	delete(m.objects, key)
	return nil
}

func (m *memBlobs) keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.objects))
	for k := range m.objects {
		out = append(out, k)
	}
	return out
}

func (m *memBlobs) log() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.calls...)
}

// fakeMapStore is the campaignMapStore seam. createErr / setImageErr force the
// failure paths whose cleanup is the whole point of the ordering.
type fakeMapStore struct {
	maps         map[uuid.UUID]storage.CampaignMap
	created      []storage.NewCampaignMap
	updated      []storage.CampaignMapUpdate
	createErr    error
	updateErr    error
	setImageErr  error
	deleteKey    string
	deleteErr    error
	createPinErr error
	unpinned     []storage.KGNode
	pinned       int
}

func newFakeMapStore() *fakeMapStore {
	return &fakeMapStore{maps: map[uuid.UUID]storage.CampaignMap{}}
}

func (f *fakeMapStore) CreateMap(_ context.Context, m storage.NewCampaignMap) (storage.CampaignMap, error) {
	f.created = append(f.created, m)
	if f.createErr != nil {
		return storage.CampaignMap{}, f.createErr
	}
	out := storage.CampaignMap{
		ID: uuid.New(), CampaignID: m.CampaignID, Name: m.Name, BlobKey: m.BlobKey,
		WidthPx: m.WidthPx, HeightPx: m.HeightPx, GMPrivate: m.GMPrivate,
	}
	f.maps[out.ID] = out
	return out, nil
}

func (f *fakeMapStore) ListMaps(context.Context, uuid.UUID) ([]storage.CampaignMap, error) {
	return nil, nil
}

func (f *fakeMapStore) GetMap(_ context.Context, _, id uuid.UUID) (storage.CampaignMap, error) {
	m, ok := f.maps[id]
	if !ok {
		return storage.CampaignMap{}, storage.ErrNotFound
	}
	return m, nil
}

func (f *fakeMapStore) UpdateMap(_ context.Context, u storage.CampaignMapUpdate) (storage.CampaignMap, error) {
	f.updated = append(f.updated, u)
	if f.updateErr != nil {
		return storage.CampaignMap{}, f.updateErr
	}
	m := f.maps[u.ID]
	m.Name = u.Name
	f.maps[u.ID] = m
	return m, nil
}

func (f *fakeMapStore) SetMapImage(_ context.Context, _, id uuid.UUID, blobKey string, w, h int) (storage.CampaignMap, error) {
	if f.setImageErr != nil {
		return storage.CampaignMap{}, f.setImageErr
	}
	m, ok := f.maps[id]
	if !ok {
		return storage.CampaignMap{}, storage.ErrNotFound
	}
	m.BlobKey, m.WidthPx, m.HeightPx = blobKey, w, h
	f.maps[id] = m
	return m, nil
}

func (f *fakeMapStore) DeleteMap(_ context.Context, _, id uuid.UUID) (string, error) {
	if f.deleteErr != nil {
		return "", f.deleteErr
	}
	m, ok := f.maps[id]
	if !ok {
		return "", storage.ErrNotFound
	}
	delete(f.maps, id)
	f.deleteKey = m.BlobKey
	return m.BlobKey, nil
}

func (f *fakeMapStore) MapAncestors(context.Context, uuid.UUID, uuid.UUID) ([]storage.CampaignMap, error) {
	return nil, nil
}

func (f *fakeMapStore) CreatePin(_ context.Context, n storage.NewMapPin) (storage.MapPin, error) {
	f.pinned++
	if f.createPinErr != nil {
		return storage.MapPin{}, f.createPinErr
	}
	return storage.MapPin{ID: uuid.New(), MapID: n.MapID, NodeID: n.NodeID, X: n.X, Y: n.Y}, nil
}

func (f *fakeMapStore) ListPins(context.Context, uuid.UUID, uuid.UUID) ([]storage.MapPin, error) {
	return nil, nil
}

func (f *fakeMapStore) UpdatePin(_ context.Context, u storage.MapPinUpdate) (storage.MapPin, error) {
	return storage.MapPin{ID: u.ID, X: u.X, Y: u.Y}, nil
}

func (f *fakeMapStore) DeletePin(context.Context, uuid.UUID, uuid.UUID) error { return nil }

func (f *fakeMapStore) UnpinnedNodes(context.Context, uuid.UUID, uuid.UUID) ([]storage.KGNode, error) {
	return f.unpinned, nil
}

// mapClient composes a CampaignServer over the map fake plus a blob seam.
func mapClient(t *testing.T, store *fakeMapStore, blobs blob.Store) (managementv1connect.CampaignServiceClient, uuid.UUID) {
	t.Helper()
	campaignID := uuid.New()
	tenantID := uuid.New()
	active := &fakeActive{campaign: storage.Campaign{ID: campaignID, TenantID: tenantID, Name: "Saltmarsh"}}
	srv := rpc.NewCampaignServerWith(rpc.CampaignStores{Active: active, Maps: store})
	if blobs != nil {
		srv.SetBlobs(blobs)
	}
	inject := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			ctx = auth.WithUser(ctx, storage.User{DiscordUserID: "999"})
			ctx = auth.WithTenant(ctx, tenantID)
			return next(ctx, req)
		}
	})
	return campaignClientServe(t, srv, connect.WithInterceptors(inject)), tenantID
}

// pngBytes is a minimal valid PNG header the image validator accepts.
var pngBytes = append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, make([]byte, 64)...)

func createReq(name string) *managementv1.CreateMapRequest {
	return &managementv1.CreateMapRequest{
		Name: name, ImageBytes: pngBytes, ContentType: "image/png",
		WidthPx: 1200, HeightPx: 800,
	}
}

// TestCreateMap_BlobLandsBeforeRow: the bytes are written FIRST, then the row.
// A row that references bytes which do not exist is a broken map forever; a blob
// with no row is invisible and finite. The ordering is the whole design, so it is
// asserted directly rather than inferred from the end state.
func TestCreateMap_BlobLandsBeforeRow(t *testing.T) {
	t.Parallel()
	store := newFakeMapStore()
	blobs := newMemBlobs()
	client, tenantID := mapClient(t, store, blobs)

	resp, err := client.CreateMap(context.Background(), connect.NewRequest(createReq("Saltmarsh")))
	if err != nil {
		t.Fatalf("CreateMap: %v", err)
	}
	if len(store.created) != 1 {
		t.Fatalf("row not created: %+v", store.created)
	}
	log := blobs.log()
	if len(log) != 1 || !strings.HasPrefix(log[0], "put:") {
		t.Fatalf("blob call log = %v, want a single put before the row", log)
	}
	// The key must carry the tenant prefix (ADR-0048 makes it mandatory).
	if !strings.Contains(store.created[0].BlobKey, tenantID.String()) {
		t.Errorf("blob key %q omits the tenant prefix", store.created[0].BlobKey)
	}
	if resp.Msg.GetMap().GetName() != "Saltmarsh" {
		t.Errorf("name on wire = %q", resp.Msg.GetMap().GetName())
	}
}

// TestCreateMap_InsertFailureDropsTheOrphanBlob: when the row fails, the bytes it
// would have pointed at are deleted inline.
//
// There is no background reconciliation for map keys — the campaign sweep walks
// campaign_map ROWS, and this insert produced none — so if this delete does not
// happen the bytes are unreachable forever.
func TestCreateMap_InsertFailureDropsTheOrphanBlob(t *testing.T) {
	t.Parallel()
	store := newFakeMapStore()
	store.createErr = errors.New("boom")
	blobs := newMemBlobs()
	client, _ := mapClient(t, store, blobs)

	if _, err := client.CreateMap(context.Background(), connect.NewRequest(createReq("Saltmarsh"))); err == nil {
		t.Fatal("CreateMap should fail when the row insert fails")
	}
	if got := blobs.keys(); len(got) != 0 {
		t.Fatalf("orphaned blobs survive a failed insert: %v", got)
	}
}

// TestCreateMap_CrossCampaignParentIsNotFound: a parent Map or anchor Node from
// another Campaign is refused by the composite FKs. That is the operator naming
// something absent — NotFound, not a 500 with an "internal error" in the log.
func TestCreateMap_CrossCampaignParentIsNotFound(t *testing.T) {
	t.Parallel()
	store := newFakeMapStore()
	store.createErr = storage.ErrNotFound
	client, _ := mapClient(t, store, newMemBlobs())

	_, err := client.CreateMap(context.Background(), connect.NewRequest(createReq("Saltmarsh")))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound", got)
	}
}

// TestUpdateMap_NameCapApplies: the rename path enforces the same bound the create
// path does. A cap applied only on create is a cap a rename removes.
func TestUpdateMap_NameCapApplies(t *testing.T) {
	t.Parallel()
	store := newFakeMapStore()
	client, _ := mapClient(t, store, newMemBlobs())

	_, err := client.UpdateMap(context.Background(), connect.NewRequest(&managementv1.UpdateMapRequest{
		Id: uuid.New().String(), Name: strings.Repeat("x", 5_000),
	}))
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", got)
	}
	if len(store.updated) != 0 {
		t.Errorf("an over-long name reached the store: %+v", store.updated)
	}
}

// TestReplaceMapImage_DropsTheSupersededBlob: re-uploading points the row at new
// bytes and deletes the old ones, so a map that is re-scanned five times does not
// keep five images alive.
func TestReplaceMapImage_DropsTheSupersededBlob(t *testing.T) {
	t.Parallel()
	store := newFakeMapStore()
	blobs := newMemBlobs()
	client, _ := mapClient(t, store, blobs)

	created, err := client.CreateMap(context.Background(), connect.NewRequest(createReq("Saltmarsh")))
	if err != nil {
		t.Fatalf("CreateMap: %v", err)
	}
	first := created.Msg.GetMap().GetId()
	oldKey := store.maps[uuid.MustParse(first)].BlobKey

	if _, err := client.ReplaceMapImage(context.Background(), connect.NewRequest(&managementv1.ReplaceMapImageRequest{
		Id: first, ImageBytes: pngBytes, ContentType: "image/png", WidthPx: 2400, HeightPx: 1600,
	})); err != nil {
		t.Fatalf("ReplaceMapImage: %v", err)
	}
	for _, k := range blobs.keys() {
		if k == oldKey {
			t.Fatalf("the superseded image %q was not dropped", oldKey)
		}
	}
	if got := store.maps[uuid.MustParse(first)].WidthPx; got != 2400 {
		t.Errorf("row still carries the old dimensions: %d", got)
	}
}

// TestCreateMap_NoBlobSeamIsUnimplemented: a composition with no blob backend says
// so, rather than writing a row that points at bytes nothing ever stored.
func TestCreateMap_NoBlobSeamIsUnimplemented(t *testing.T) {
	t.Parallel()
	store := newFakeMapStore()
	client, _ := mapClient(t, store, nil)

	_, err := client.CreateMap(context.Background(), connect.NewRequest(createReq("Saltmarsh")))
	if got := connect.CodeOf(err); got != connect.CodeUnimplemented {
		t.Errorf("code = %v, want Unimplemented", got)
	}
	if len(store.created) != 0 {
		t.Errorf("a row was created with no bytes behind it: %+v", store.created)
	}
}

// TestCreatePin_DuplicateIsAlreadyExists: one Pin per Node per Map, reported as a
// conflict the GM can understand rather than a server fault.
func TestCreatePin_DuplicateIsAlreadyExists(t *testing.T) {
	t.Parallel()
	store := newFakeMapStore()
	store.createPinErr = storage.ErrConflict
	client, _ := mapClient(t, store, newMemBlobs())

	_, err := client.CreatePin(context.Background(), connect.NewRequest(&managementv1.CreatePinRequest{
		MapId: uuid.New().String(), NodeId: uuid.New().String(), X: 0.5, Y: 0.5,
	}))
	if got := connect.CodeOf(err); got != connect.CodeAlreadyExists {
		t.Errorf("code = %v, want AlreadyExists", got)
	}
}

// TestCreatePin_CrossCampaignIsNotFound: the composite FKs refuse a Map or Node
// from another Campaign, and the refusal reaches the operator as NotFound.
func TestCreatePin_CrossCampaignIsNotFound(t *testing.T) {
	t.Parallel()
	store := newFakeMapStore()
	store.createPinErr = storage.ErrNotFound
	client, _ := mapClient(t, store, newMemBlobs())

	_, err := client.CreatePin(context.Background(), connect.NewRequest(&managementv1.CreatePinRequest{
		MapId: uuid.New().String(), NodeId: uuid.New().String(), X: 0.5, Y: 0.5,
	}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound", got)
	}
}

// TestDeleteMap_DropsTheImageThroughTheSeam: deleting a Map frees its bytes.
// The blob has no FK, so nothing else ever would.
func TestDeleteMap_DropsTheImageThroughTheSeam(t *testing.T) {
	t.Parallel()
	store := newFakeMapStore()
	blobs := newMemBlobs()
	client, _ := mapClient(t, store, blobs)

	created, err := client.CreateMap(context.Background(), connect.NewRequest(createReq("Saltmarsh")))
	if err != nil {
		t.Fatalf("CreateMap: %v", err)
	}
	if _, err := client.DeleteMap(context.Background(), connect.NewRequest(&managementv1.DeleteMapRequest{
		Id: created.Msg.GetMap().GetId(),
	})); err != nil {
		t.Fatalf("DeleteMap: %v", err)
	}
	if got := blobs.keys(); len(got) != 0 {
		t.Fatalf("the map image survived the delete: %v", got)
	}
}

// seedKeylessMap inserts the row an imageless bundle import leaves: a Map with
// every field but a picture. It is inserted straight into the fake because no RPC
// can produce one — CreateMap requires bytes.
func seedKeylessMap(store *fakeMapStore) uuid.UUID {
	id := uuid.New()
	store.maps[id] = storage.CampaignMap{
		ID: id, Name: "Saltmarsh", BlobKey: "", WidthPx: 1200, HeightPx: 800,
	}
	return id
}

// TestKeylessMap_NeverAsksTheSeamToDeleteNothing. A Map restored from an imageless
// bundle carries an EMPTY blob_key, and blob.Delete("") is ErrInvalidKey — not the
// idempotent no-op an absent key gets. Handing "" to the seam therefore warns about
// reclaimable bytes that never existed, on the two most ordinary things a GM does
// to such a map: delete it, or upload the picture it is missing. The second is
// worse, because it is the repair path — the log would claim a leak on the very
// action that fixed the map.
//
// Asserted on the seam's CALL LOG rather than on the returned error: the point is
// that the handler does not ASK, which stays true whatever a backend answers.
func TestKeylessMap_NeverAsksTheSeamToDeleteNothing(t *testing.T) {
	t.Parallel()
	for name, act := range map[string]func(context.Context, managementv1connect.CampaignServiceClient, string) error{
		"delete": func(ctx context.Context, c managementv1connect.CampaignServiceClient, id string) error {
			_, err := c.DeleteMap(ctx, connect.NewRequest(&managementv1.DeleteMapRequest{Id: id}))
			return err
		},
		"replace image": func(ctx context.Context, c managementv1connect.CampaignServiceClient, id string) error {
			_, err := c.ReplaceMapImage(ctx, connect.NewRequest(&managementv1.ReplaceMapImageRequest{
				Id: id, ImageBytes: pngBytes, ContentType: "image/png", WidthPx: 1200, HeightPx: 800,
			}))
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := newFakeMapStore()
			blobs := newMemBlobs()
			client, _ := mapClient(t, store, blobs)
			id := seedKeylessMap(store)

			if err := act(context.Background(), client, id.String()); err != nil {
				t.Fatalf("%s on a keyless map: %v", name, err)
			}
			for _, call := range blobs.log() {
				if call == "delete:" {
					t.Errorf("%s asked the seam to delete the empty key", name)
				}
			}
		})
	}
}

// TestReplaceMapImage_RepairsAKeylessMap is the other half: the repair must
// actually work, so the no-delete assertion above is not passing because the whole
// call bailed out early.
func TestReplaceMapImage_RepairsAKeylessMap(t *testing.T) {
	t.Parallel()
	store := newFakeMapStore()
	blobs := newMemBlobs()
	client, _ := mapClient(t, store, blobs)
	id := seedKeylessMap(store)

	resp, err := client.ReplaceMapImage(context.Background(), connect.NewRequest(&managementv1.ReplaceMapImageRequest{
		Id: id.String(), ImageBytes: pngBytes, ContentType: "image/png", WidthPx: 1200, HeightPx: 800,
	}))
	if err != nil {
		t.Fatalf("ReplaceMapImage: %v", err)
	}
	if !resp.Msg.GetMap().GetHasImage() {
		t.Error("the repaired map still reports no image")
	}
	if got := blobs.keys(); len(got) != 1 {
		t.Errorf("blobs after repair = %v, want exactly the uploaded one", got)
	}
}

// TestListMaps_HasImageFollowsTheKey: has_image is what tells the Maps tab to draw
// a "no image" surface instead of requesting a picture that is not there, so it
// must track the key and not merely default to true.
func TestListMaps_HasImageFollowsTheKey(t *testing.T) {
	t.Parallel()
	store := newFakeMapStore()
	blobs := newMemBlobs()
	client, _ := mapClient(t, store, blobs)

	created, err := client.CreateMap(context.Background(), connect.NewRequest(createReq("Saltmarsh")))
	if err != nil {
		t.Fatalf("CreateMap: %v", err)
	}
	if !created.Msg.GetMap().GetHasImage() {
		t.Error("a map created with bytes reports no image")
	}

	id := seedKeylessMap(store)
	view, err := client.GetMapView(context.Background(), connect.NewRequest(&managementv1.GetMapViewRequest{Id: id.String()}))
	if err != nil {
		t.Fatalf("GetMapView: %v", err)
	}
	if view.Msg.GetMap().GetHasImage() {
		t.Error("a map with no blob key claims to have an image")
	}
}

// ── Generated maps (#541) ─────────────────────────────────────────────────────

// fakeMapGen stands in for the mapgen engine at the handler seam.
type fakeMapGen struct {
	res     mapgen.Result
	err     error
	gotIn   mapgen.Input
	picked  []uuid.UUID
	pickErr error
	calls   int
}

func (f *fakeMapGen) Generate(_ context.Context, _ storage.Campaign, in mapgen.Input) (mapgen.Result, error) {
	f.calls++
	f.gotIn = in
	return f.res, f.err
}

func (f *fakeMapGen) SuggestPins(_ context.Context, _ storage.Campaign, _ uuid.UUID, _ []storage.KGNode) ([]uuid.UUID, error) {
	f.calls++
	return f.picked, f.pickErr
}

func genClient(t *testing.T, store *fakeMapStore, gen *fakeMapGen) managementv1connect.CampaignServiceClient {
	t.Helper()
	campaignID := uuid.New()
	tenantID := uuid.New()
	active := &fakeActive{campaign: storage.Campaign{ID: campaignID, TenantID: tenantID, Name: "Saltmarsh"}}
	srv := rpc.NewCampaignServerWith(rpc.CampaignStores{Active: active, Maps: store})
	srv.SetBlobs(newMemBlobs())
	if gen != nil {
		srv.SetMapGenerator(gen)
		srv.SetMapPinSuggester(gen)
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

// TestGenerateMapImage_WritesNothing is AC1, and it is the whole design: the
// bytes exist only in the GM's browser until they choose to save them, so a
// discarded draft cannot leave a row or an orphaned blob behind.
func TestGenerateMapImage_WritesNothing(t *testing.T) {
	t.Parallel()
	store := newFakeMapStore()
	gen := &fakeMapGen{res: mapgen.Result{
		Data: pngBytes, ContentType: "image/png", Model: "gemini-2.5-flash-image",
		Prompt: "Draw a top-down fantasy map …",
	}}
	client := genClient(t, store, gen)

	resp, err := client.GenerateMapImage(context.Background(),
		connect.NewRequest(&managementv1.GenerateMapImageRequest{Prompt: "a fishing town"}))
	if err != nil {
		t.Fatalf("GenerateMapImage: %v", err)
	}
	if len(resp.Msg.GetImageBytes()) == 0 {
		t.Fatal("no draft bytes came back")
	}
	if resp.Msg.GetModel() != "gemini-2.5-flash-image" {
		t.Errorf("model = %q", resp.Msg.GetModel())
	}
	// The composed prompt travels back so the GM sees what was actually asked for.
	if resp.Msg.GetPrompt() == "" {
		t.Error("the composed prompt was not returned")
	}
	if len(store.created) != 0 || len(store.maps) != 0 {
		t.Fatalf("generation created a map row: %+v", store.created)
	}
}

// TestGenerateMapImage_TooLargeIsInvalidArgument is AC5. NOT Unavailable:
// Unavailable invites a retry, and retrying re-bills the identical oversize
// generation.
func TestGenerateMapImage_TooLargeIsInvalidArgument(t *testing.T) {
	t.Parallel()
	gen := &fakeMapGen{err: imagegen.ErrImageTooLarge}
	client := genClient(t, newFakeMapStore(), gen)

	_, err := client.GenerateMapImage(context.Background(),
		connect.NewRequest(&managementv1.GenerateMapImageRequest{Prompt: "an entire continent"}))
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", got)
	}
}

// TestGenerateMapImage_QuotaIsResourceExhausted is the reported bug: a Gemini key
// that is VALID but out of quota came back as "check the image provider
// configuration", sending the GM to fix the one thing that was not broken. The
// key was accepted; the fix is billing or waiting.
func TestGenerateMapImage_QuotaIsResourceExhausted(t *testing.T) {
	t.Parallel()
	gen := &fakeMapGen{err: &imagegen.StatusError{
		StatusCode: http.StatusTooManyRequests,
		Body:       `{"error":{"code":429,"message":"You exceeded your current quota"}}`,
	}}
	client := genClient(t, newFakeMapStore(), gen)

	_, err := client.GenerateMapImage(context.Background(),
		connect.NewRequest(&managementv1.GenerateMapImageRequest{Prompt: "a town"}))
	if got := connect.CodeOf(err); got != connect.CodeResourceExhausted {
		t.Errorf("code = %v, want ResourceExhausted", got)
	}
	if strings.Contains(err.Error(), "configuration") {
		t.Errorf("a quota failure still blames the configuration: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "quota") {
		t.Errorf("the message does not name what is actually wrong: %q", err.Error())
	}
}

// TestGenerateMapImage_AuthFailureIsUnavailable is the other half: a REJECTED key
// is the one case where "check the image provider configuration" is true advice,
// so that wording (and its code) must survive the quota split.
func TestGenerateMapImage_AuthFailureIsUnavailable(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		gen := &fakeMapGen{err: &imagegen.StatusError{StatusCode: status, Body: `{"error":{"message":"API key not valid"}}`}}
		client := genClient(t, newFakeMapStore(), gen)

		_, err := client.GenerateMapImage(context.Background(),
			connect.NewRequest(&managementv1.GenerateMapImageRequest{Prompt: "a town"}))
		if got := connect.CodeOf(err); got != connect.CodeUnavailable {
			t.Errorf("HTTP %d: code = %v, want Unavailable", status, got)
		}
		if !strings.Contains(err.Error(), "image provider configuration") {
			t.Errorf("HTTP %d: a rejected key should point at the configuration: %q", status, err.Error())
		}
	}
}

// TestGenerateMapImage_ProviderFaultBlamesTheProvider: a 5xx is neither the key
// nor the quota, and telling the GM to check either is a wild goose chase.
func TestGenerateMapImage_ProviderFaultBlamesTheProvider(t *testing.T) {
	t.Parallel()
	gen := &fakeMapGen{err: &imagegen.StatusError{StatusCode: http.StatusBadGateway, Body: "upstream connect error"}}
	client := genClient(t, newFakeMapStore(), gen)

	_, err := client.GenerateMapImage(context.Background(),
		connect.NewRequest(&managementv1.GenerateMapImageRequest{Prompt: "a town"}))
	if got := connect.CodeOf(err); got != connect.CodeUnavailable {
		t.Errorf("code = %v, want Unavailable", got)
	}
	if strings.Contains(err.Error(), "configuration") {
		t.Errorf("a provider-side 502 still blames the configuration: %q", err.Error())
	}
}

// TestGenerateMapImage_UnknownFailureGuessesNothing: an error with no upstream
// status (a transport failure, a decode error) must not fabricate a cause.
func TestGenerateMapImage_UnknownFailureGuessesNothing(t *testing.T) {
	t.Parallel()
	gen := &fakeMapGen{err: errors.New("dial tcp: i/o timeout")}
	client := genClient(t, newFakeMapStore(), gen)

	_, err := client.GenerateMapImage(context.Background(),
		connect.NewRequest(&managementv1.GenerateMapImageRequest{Prompt: "a town"}))
	if got := connect.CodeOf(err); got != connect.CodeUnavailable {
		t.Errorf("code = %v, want Unavailable", got)
	}
	if strings.Contains(err.Error(), "configuration") {
		t.Errorf("an unknown failure still blames the configuration: %q", err.Error())
	}
}

// TestSuggestMapPins_QuotaIsResourceExhausted: the pin suggester shares
// mapGenerateErr, so it must not regress to the configuration sentence either.
//
// The injected error is a *providererr.HTTPError and NOT an imagegen.StatusError,
// because that is what this path actually produces: suggestions run on the TEXT
// provider (mapgen/suggest.go → assist.CallText → the LLM adapters, ADR-0044), and
// a test that faked an image error here would certify a branch that is dead in
// production — the "one sentinel across a shared seam" trap this repo has already
// paid for once (see mapgen.ErrNotConfigured's comment).
func TestSuggestMapPins_QuotaIsResourceExhausted(t *testing.T) {
	t.Parallel()
	store := newFakeMapStore()
	mapID := uuid.New()
	anchor := uuid.New()
	store.maps[mapID] = storage.CampaignMap{ID: mapID, Name: "Saltmarsh", AnchorNodeID: uuid.NullUUID{UUID: anchor, Valid: true}}
	store.unpinned = []storage.KGNode{{ID: uuid.New(), Name: "The Snapping Line", Type: storage.KGNodeLocation}}
	gen := &fakeMapGen{pickErr: &providererr.HTTPError{
		Op: "anthropic.Complete", StatusCode: http.StatusTooManyRequests,
		Status: "429 Too Many Requests", Body: `{"type":"error","error":{"type":"rate_limit_error"}}`,
	}}
	client := genClient(t, store, gen)

	_, err := client.SuggestMapPins(context.Background(),
		connect.NewRequest(&managementv1.SuggestMapPinsRequest{MapId: mapID.String()}))
	if got := connect.CodeOf(err); got != connect.CodeResourceExhausted {
		t.Errorf("code = %v, want ResourceExhausted", got)
	}
	// The rate-limited provider is the LLM one, and blaming the image provider —
	// which was never called — sends the GM to the wrong half of Configuration.
	if !strings.Contains(err.Error(), "LLM provider") {
		t.Errorf("the wrong provider is named: %q", err.Error())
	}
}

// TestSuggestMapPins_FailureIsNotReportedAsMapGeneration: one mapper, two ops. A
// failed pin suggestion that announces itself as a failed map generation sends
// the GM looking at a map that is fine.
func TestSuggestMapPins_FailureIsNotReportedAsMapGeneration(t *testing.T) {
	t.Parallel()
	store := newFakeMapStore()
	mapID := uuid.New()
	anchor := uuid.New()
	store.maps[mapID] = storage.CampaignMap{ID: mapID, Name: "Saltmarsh", AnchorNodeID: uuid.NullUUID{UUID: anchor, Valid: true}}
	store.unpinned = []storage.KGNode{{ID: uuid.New(), Name: "The Snapping Line", Type: storage.KGNodeLocation}}
	gen := &fakeMapGen{pickErr: errors.New("dial tcp: i/o timeout")}
	client := genClient(t, store, gen)

	_, err := client.SuggestMapPins(context.Background(),
		connect.NewRequest(&managementv1.SuggestMapPinsRequest{MapId: mapID.String()}))
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "map generation") {
		t.Errorf("a pin-suggestion failure reports itself as map generation: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "pin suggestions") {
		t.Errorf("the message does not name what failed: %q", err.Error())
	}
}

// TestSuggestMapPins_AuthFailureNamesTheLLMKey: the same split on the credential
// side — a rejected LLM key must not send the GM to the image provider's key.
func TestSuggestMapPins_AuthFailureNamesTheLLMKey(t *testing.T) {
	t.Parallel()
	store := newFakeMapStore()
	mapID := uuid.New()
	anchor := uuid.New()
	store.maps[mapID] = storage.CampaignMap{ID: mapID, Name: "Saltmarsh", AnchorNodeID: uuid.NullUUID{UUID: anchor, Valid: true}}
	store.unpinned = []storage.KGNode{{ID: uuid.New(), Name: "The Snapping Line", Type: storage.KGNodeLocation}}
	gen := &fakeMapGen{pickErr: &providererr.HTTPError{
		Op: "anthropic.Complete", StatusCode: http.StatusUnauthorized,
		Status: "401 Unauthorized", Body: `{"error":{"message":"invalid x-api-key"}}`,
	}}
	client := genClient(t, store, gen)

	_, err := client.SuggestMapPins(context.Background(),
		connect.NewRequest(&managementv1.SuggestMapPinsRequest{MapId: mapID.String()}))
	if got := connect.CodeOf(err); got != connect.CodeUnavailable {
		t.Errorf("code = %v, want Unavailable", got)
	}
	if !strings.Contains(err.Error(), "LLM provider configuration") {
		t.Errorf("a rejected LLM key should point at the LLM configuration: %q", err.Error())
	}
	if strings.Contains(err.Error(), "image provider") {
		t.Errorf("a rejected LLM key blames the image provider: %q", err.Error())
	}
}

func TestGenerateMapImage_NoKeyIsActionable(t *testing.T) {
	t.Parallel()
	client := genClient(t, newFakeMapStore(), &fakeMapGen{err: mapgen.ErrNotConfigured})
	_, err := client.GenerateMapImage(context.Background(),
		connect.NewRequest(&managementv1.GenerateMapImageRequest{Prompt: "a town"}))
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Errorf("code = %v, want FailedPrecondition", got)
	}
}

func TestGenerateMapImage_UnwiredIsUnavailable(t *testing.T) {
	t.Parallel()
	client := genClient(t, newFakeMapStore(), nil)
	_, err := client.GenerateMapImage(context.Background(),
		connect.NewRequest(&managementv1.GenerateMapImageRequest{Prompt: "a town"}))
	if got := connect.CodeOf(err); got != connect.CodeUnavailable {
		t.Errorf("code = %v, want Unavailable", got)
	}
}

func TestGenerateMapImage_EmptyPromptNeverReachesTheProvider(t *testing.T) {
	t.Parallel()
	gen := &fakeMapGen{}
	client := genClient(t, newFakeMapStore(), gen)
	_, err := client.GenerateMapImage(context.Background(),
		connect.NewRequest(&managementv1.GenerateMapImageRequest{Prompt: "   "}))
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", got)
	}
	if gen.calls != 0 {
		t.Error("an empty prompt spent a generation")
	}
}

// TestSuggestMapPins_OnlyReturnsUnpinnedCandidates is AC3's first half: the
// suggestions are entries the GM could already have pinned, never new ones and
// never positions.
func TestSuggestMapPins_OnlyReturnsUnpinnedCandidates(t *testing.T) {
	t.Parallel()
	store := newFakeMapStore()
	mapID := uuid.New()
	anchor := uuid.New()
	store.maps[mapID] = storage.CampaignMap{
		ID: mapID, Name: "Saltmarsh",
		AnchorNodeID: uuid.NullUUID{UUID: anchor, Valid: true},
	}
	inn := storage.KGNode{ID: uuid.New(), Name: "The Rusty Anchor", Type: storage.KGNodeLocation}
	store.unpinned = []storage.KGNode{inn}

	// The engine names the real candidate AND a uuid that is not in the set — a
	// hallucinated id must be dropped, not trusted into the response.
	gen := &fakeMapGen{picked: []uuid.UUID{inn.ID, uuid.New()}}
	client := genClient(t, store, gen)

	resp, err := client.SuggestMapPins(context.Background(),
		connect.NewRequest(&managementv1.SuggestMapPinsRequest{MapId: mapID.String()}))
	if err != nil {
		t.Fatalf("SuggestMapPins: %v", err)
	}
	if len(resp.Msg.GetSuggestions()) != 1 {
		t.Fatalf("suggestions = %+v, want only the real candidate", resp.Msg.GetSuggestions())
	}
	if resp.Msg.GetSuggestions()[0].GetNodeId() != inn.ID.String() {
		t.Errorf("wrong node suggested: %+v", resp.Msg.GetSuggestions()[0])
	}
	// Nothing was pinned. AC3: a suggestion requires a GM drag before it persists.
	if store.pinned != 0 {
		t.Fatalf("suggesting created %d pins", store.pinned)
	}
}

// TestSuggestMapPins_NoAnchorIsRefusedWithAReason: without an anchor there is no
// prose to read, and guessing from the map's name alone is not suggesting.
func TestSuggestMapPins_NoAnchorIsRefusedWithAReason(t *testing.T) {
	t.Parallel()
	store := newFakeMapStore()
	mapID := uuid.New()
	store.maps[mapID] = storage.CampaignMap{ID: mapID, Name: "Saltmarsh"}
	gen := &fakeMapGen{}
	client := genClient(t, store, gen)

	_, err := client.SuggestMapPins(context.Background(),
		connect.NewRequest(&managementv1.SuggestMapPinsRequest{MapId: mapID.String()}))
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Errorf("code = %v, want FailedPrecondition", got)
	}
	if gen.calls != 0 {
		t.Error("an anchorless map still spent a call")
	}
}
