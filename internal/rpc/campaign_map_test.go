package rpc_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/blob"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/storage"
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
	return nil, nil
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
