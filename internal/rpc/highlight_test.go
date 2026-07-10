package rpc_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// fakeHighlightStore is a tenant-scoped in-memory highlight store for the RPC tests.
type fakeHighlightStore struct {
	mu       sync.Mutex
	tenantID uuid.UUID
	rows     map[uuid.UUID]storage.Highlight
}

func newFakeHighlightStore(tenantID uuid.UUID) *fakeHighlightStore {
	return &fakeHighlightStore{tenantID: tenantID, rows: map[uuid.UUID]storage.Highlight{}}
}

func (f *fakeHighlightStore) put(h storage.Highlight) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[h.ID] = h
}

func (f *fakeHighlightStore) ListHighlights(_ context.Context, tenantID, sessionID uuid.UUID) ([]storage.Highlight, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []storage.Highlight
	for _, h := range f.rows {
		if h.TenantID == tenantID && h.VoiceSessionID == sessionID {
			out = append(out, h)
		}
	}
	return out, nil
}

func (f *fakeHighlightStore) GetHighlight(_ context.Context, tenantID, id uuid.UUID) (storage.Highlight, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.rows[id]
	if !ok || h.TenantID != tenantID {
		return storage.Highlight{}, storage.ErrNotFound
	}
	return h, nil
}

func (f *fakeHighlightStore) PromoteHighlight(_ context.Context, tenantID, id uuid.UUID) (storage.Highlight, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.rows[id]
	if !ok || h.TenantID != tenantID {
		return storage.Highlight{}, storage.ErrNotFound
	}
	if h.PromotedAt == nil {
		now := time.Now()
		h.PromotedAt = &now
	}
	h.Status = storage.HighlightPromoted
	f.rows[id] = h
	return h, nil
}

func (f *fakeHighlightStore) DeleteHighlight(_ context.Context, tenantID, id uuid.UUID) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.rows[id]
	if !ok || h.TenantID != tenantID {
		return "", storage.ErrNotFound
	}
	delete(f.rows, id)
	return h.ClipKey, nil
}

// fakeRPCBlobs records the keys deleted through the seam.
type fakeRPCBlobs struct {
	mu      sync.Mutex
	deleted []string
}

func (f *fakeRPCBlobs) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, key)
	return nil
}

// newHighlightClient mounts a SessionServer with the highlight seam wired and an
// authenticated operator on tenantID. sstore drives the Active-Campaign resolution
// (#308 campaign scoping) and the voice-session ownership lookups; a nil sstore
// falls back to a plain activeStore() (the legacy tenant-only tests).
func newHighlightClient(t *testing.T, tenantID uuid.UUID, hstore rpc.HighlightStore, blobs *fakeRPCBlobs, sstore *fakeSessionStore) managementv1connect.SessionServiceClient {
	t.Helper()
	if sstore == nil {
		sstore = activeStore()
	}
	inject := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			ctx = auth.WithTenant(ctx, tenantID)
			return next(ctx, req)
		}
	})
	srv := rpc.NewSessionServer(&fakeSessionManager{}, sstore, nil, nil)
	srv.SetHighlights(hstore, blobs)
	mux := http.NewServeMux()
	mux.Handle(srv.Handler(connect.WithInterceptors(inject)))
	httpSrv := httptest.NewServer(mux)
	t.Cleanup(httpSrv.Close)
	return managementv1connect.NewSessionServiceClient(http.DefaultClient, httpSrv.URL, connect.WithProtoJSON())
}

// campaignStore returns a fakeSessionStore whose resolved Active Campaign is
// campaignID and that owns the given voice sessions (each mapped to campaignID), so
// the highlight RPCs' cross-campaign checks have a session to look up.
func campaignSessionStore(campaignID uuid.UUID, sessionIDs ...uuid.UUID) *fakeSessionStore {
	s := &fakeSessionStore{
		campaign:      storage.Campaign{ID: campaignID, TenantID: uuid.New(), Name: "Active"},
		latestErr:     storage.ErrNotFound,
		voiceSessions: map[uuid.UUID]storage.VoiceSession{},
	}
	for _, id := range sessionIDs {
		s.voiceSessions[id] = storage.VoiceSession{ID: id, CampaignID: campaignID}
	}
	return s
}

func seedRPCHighlight(store *fakeHighlightStore, tenantID, sessionID, campaignID uuid.UUID, status string) storage.Highlight {
	id := uuid.New()
	h := storage.Highlight{
		ID:              id,
		TenantID:        tenantID,
		VoiceSessionID:  sessionID,
		CampaignID:      campaignID,
		Status:          status,
		StartsAt:        time.Now().Add(-15 * time.Second),
		EndsAt:          time.Now().Add(5 * time.Second),
		Score:           9,
		Excerpt:         "nat 20",
		ClipKey:         "t/" + tenantID.String() + "/highlight/" + id.String() + "/clip.wav",
		ClipContentType: "audio/wav",
		ClipSizeBytes:   1234,
	}
	store.put(h)
	return h
}

func TestRPCHighlight_ListAndGet_TenantScoped(t *testing.T) {
	tenantID := uuid.New()
	sessionID := uuid.New()
	campaignID := uuid.New()
	store := newFakeHighlightStore(tenantID)
	h := seedRPCHighlight(store, tenantID, sessionID, campaignID, storage.HighlightCandidate)
	// A different tenant's row that must never surface.
	seedRPCHighlight(store, uuid.New(), sessionID, campaignID, storage.HighlightCandidate)

	client := newHighlightClient(t, tenantID, store, &fakeRPCBlobs{}, campaignSessionStore(campaignID, sessionID))

	list, err := client.ListHighlights(context.Background(),
		connect.NewRequest(&managementv1.ListHighlightsRequest{VoiceSessionId: sessionID.String()}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Msg.GetHighlights()) != 1 || list.Msg.GetHighlights()[0].GetId() != h.ID.String() {
		t.Fatalf("list not tenant-scoped: %+v", list.Msg.GetHighlights())
	}

	got, err := client.GetHighlight(context.Background(),
		connect.NewRequest(&managementv1.GetHighlightRequest{Id: h.ID.String()}))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Msg.GetHighlight().GetClipContentType() != "audio/wav" {
		t.Fatalf("get returned wrong row: %+v", got.Msg.GetHighlight())
	}
}

func TestRPCHighlight_Get_ForeignTenantNotFound(t *testing.T) {
	tenantID := uuid.New()
	campaignID := uuid.New()
	store := newFakeHighlightStore(tenantID)
	// A highlight owned by a DIFFERENT tenant.
	other := seedRPCHighlight(store, uuid.New(), uuid.New(), campaignID, storage.HighlightCandidate)

	client := newHighlightClient(t, tenantID, store, &fakeRPCBlobs{}, campaignSessionStore(campaignID))
	_, err := client.GetHighlight(context.Background(),
		connect.NewRequest(&managementv1.GetHighlightRequest{Id: other.ID.String()}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("want CodeNotFound, got %v", err)
	}
}

func TestRPCHighlight_Promote(t *testing.T) {
	tenantID := uuid.New()
	campaignID := uuid.New()
	store := newFakeHighlightStore(tenantID)
	h := seedRPCHighlight(store, tenantID, uuid.New(), campaignID, storage.HighlightCandidate)

	client := newHighlightClient(t, tenantID, store, &fakeRPCBlobs{}, campaignSessionStore(campaignID))
	res, err := client.PromoteHighlight(context.Background(),
		connect.NewRequest(&managementv1.PromoteHighlightRequest{Id: h.ID.String()}))
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if res.Msg.GetHighlight().GetStatus() != storage.HighlightPromoted {
		t.Fatalf("want promoted, got %q", res.Msg.GetHighlight().GetStatus())
	}
	if res.Msg.GetHighlight().GetPromotedAt() == nil {
		t.Fatalf("promoted_at not set")
	}
}

func TestRPCHighlight_Delete_BlobThenRow(t *testing.T) {
	tenantID := uuid.New()
	campaignID := uuid.New()
	store := newFakeHighlightStore(tenantID)
	h := seedRPCHighlight(store, tenantID, uuid.New(), campaignID, storage.HighlightCandidate)
	blobs := &fakeRPCBlobs{}

	client := newHighlightClient(t, tenantID, store, blobs, campaignSessionStore(campaignID))
	if _, err := client.DeleteHighlight(context.Background(),
		connect.NewRequest(&managementv1.DeleteHighlightRequest{Id: h.ID.String()})); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(blobs.deleted) != 1 || blobs.deleted[0] != h.ClipKey {
		t.Fatalf("clip not deleted through seam: %v", blobs.deleted)
	}
	if _, err := store.GetHighlight(context.Background(), tenantID, h.ID); err == nil {
		t.Fatalf("row not deleted")
	}
	// Deleting again is NotFound.
	_, err := client.DeleteHighlight(context.Background(),
		connect.NewRequest(&managementv1.DeleteHighlightRequest{Id: h.ID.String()}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("double delete: want NotFound, got %v", err)
	}
}

// TestRPCHighlight_CrossCampaign_NotFound is the campaign-scoping posture (#308,
// #342/#353/#356): a highlight (or its session) belonging to ANOTHER campaign than
// the resolved Active Campaign is CodeNotFound on every RPC — existence never
// leaked, exactly like GenerateRecap.
func TestRPCHighlight_CrossCampaign_NotFound(t *testing.T) {
	tenantID := uuid.New()
	activeCampaign := uuid.New()
	otherCampaign := uuid.New()
	otherSession := uuid.New()
	store := newFakeHighlightStore(tenantID)
	// A row owned by the SAME tenant but a DIFFERENT campaign + session.
	foreign := seedRPCHighlight(store, tenantID, otherSession, otherCampaign, storage.HighlightCandidate)

	// The active campaign resolves to activeCampaign; otherSession is registered as
	// belonging to otherCampaign so List can see it is cross-campaign.
	sstore := campaignSessionStore(activeCampaign)
	sstore.voiceSessions[otherSession] = storage.VoiceSession{ID: otherSession, CampaignID: otherCampaign}
	client := newHighlightClient(t, tenantID, store, &fakeRPCBlobs{}, sstore)
	ctx := context.Background()

	if _, err := client.ListHighlights(ctx,
		connect.NewRequest(&managementv1.ListHighlightsRequest{VoiceSessionId: otherSession.String()})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("List cross-campaign: want NotFound, got %v", err)
	}
	if _, err := client.GetHighlight(ctx,
		connect.NewRequest(&managementv1.GetHighlightRequest{Id: foreign.ID.String()})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("Get cross-campaign: want NotFound, got %v", err)
	}
	if _, err := client.PromoteHighlight(ctx,
		connect.NewRequest(&managementv1.PromoteHighlightRequest{Id: foreign.ID.String()})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("Promote cross-campaign: want NotFound, got %v", err)
	}
	// A cross-campaign row must never be promoted.
	if got, _ := store.GetHighlight(ctx, tenantID, foreign.ID); got.Status != storage.HighlightCandidate {
		t.Fatalf("cross-campaign Promote mutated the row: %q", got.Status)
	}
	if _, err := client.DeleteHighlight(ctx,
		connect.NewRequest(&managementv1.DeleteHighlightRequest{Id: foreign.ID.String()})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("Delete cross-campaign: want NotFound, got %v", err)
	}
	// A cross-campaign row must never be deleted.
	if _, err := store.GetHighlight(ctx, tenantID, foreign.ID); err != nil {
		t.Fatalf("cross-campaign Delete removed the row: %v", err)
	}
}

// TestRPCHighlight_List_UnparsableSessionNotFound: an unparsable voice_session_id
// names no session in the Active Campaign, so it is CodeNotFound (align with
// GenerateRecap's "names nothing" posture), not InvalidArgument.
func TestRPCHighlight_List_UnparsableSessionNotFound(t *testing.T) {
	tenantID := uuid.New()
	campaignID := uuid.New()
	store := newFakeHighlightStore(tenantID)
	client := newHighlightClient(t, tenantID, store, &fakeRPCBlobs{}, campaignSessionStore(campaignID))

	_, err := client.ListHighlights(context.Background(),
		connect.NewRequest(&managementv1.ListHighlightsRequest{VoiceSessionId: "not-a-uuid"}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unparsable session id: want NotFound, got %v", err)
	}
}
