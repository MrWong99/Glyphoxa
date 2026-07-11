package highlight

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/blob"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// fakeClipStore returns a single highlight for its owning tenant.
type fakeClipStore struct {
	tenantID   uuid.UUID
	id         uuid.UUID
	campaignID uuid.UUID
	clipKey    string
}

func (f *fakeClipStore) GetHighlight(_ context.Context, tenantID, id uuid.UUID) (storage.Highlight, error) {
	if tenantID != f.tenantID || id != f.id {
		return storage.Highlight{}, storage.ErrNotFound
	}
	return storage.Highlight{
		ID:              f.id,
		TenantID:        f.tenantID,
		CampaignID:      f.campaignID,
		Status:          storage.HighlightCandidate,
		ClipKey:         f.clipKey,
		ClipContentType: "audio/wav",
		CreatedAt:       time.Now(),
	}, nil
}

func clipRequest(t *testing.T, tenantID uuid.UUID, id string, rangeHdr string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/highlights/"+id+"/clip", nil)
	req.SetPathValue("id", id)
	if tenantID != uuid.Nil {
		req = req.WithContext(auth.WithTenant(req.Context(), tenantID))
	}
	if rangeHdr != "" {
		req.Header.Set("Range", rangeHdr)
	}
	return req
}

func newClipFixture(t *testing.T) (*ClipServer, uuid.UUID, uuid.UUID) {
	srv, tenantID, id, _ := newClipFixtureCampaign(t)
	return srv, tenantID, id
}

// newClipFixtureCampaign is newClipFixture plus the highlight's campaign id and a
// resolver that resolves the Active Campaign to it, so the campaign-scoping check
// passes on the happy path (#308).
func newClipFixtureCampaign(t *testing.T) (*ClipServer, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	tenantID, id, campaignID := uuid.New(), uuid.New(), uuid.New()
	key, err := blob.Key(tenantID, "highlight", id, "clip.wav")
	if err != nil {
		t.Fatalf("build key: %v", err)
	}
	blobs := newFakeBlobs()
	// A 200-byte "WAV" so Range math is meaningful.
	blobs.data[key] = make([]byte, 200)
	store := &fakeClipStore{tenantID: tenantID, id: id, campaignID: campaignID, clipKey: key}
	resolve := func(context.Context) (uuid.UUID, bool, error) { return campaignID, true, nil }
	return NewClipServer(store, blobs, resolve, testLog()), tenantID, id, campaignID
}

func TestClip_ServesWithContentType(t *testing.T) {
	srv, tenantID, id := newClipFixture(t)
	rr := httptest.NewRecorder()
	srv.ServeClip(rr, clipRequest(t, tenantID, id.String(), ""))

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "audio/wav" {
		t.Fatalf("want audio/wav, got %q", ct)
	}
	if rr.Body.Len() != 200 {
		t.Fatalf("want 200 bytes, got %d", rr.Body.Len())
	}
}

func TestClip_RangeReturnsPartial(t *testing.T) {
	srv, tenantID, id := newClipFixture(t)
	rr := httptest.NewRecorder()
	srv.ServeClip(rr, clipRequest(t, tenantID, id.String(), "bytes=0-99"))

	if rr.Code != http.StatusPartialContent {
		t.Fatalf("want 206 Partial Content, got %d", rr.Code)
	}
	if rr.Body.Len() != 100 {
		t.Fatalf("want 100 bytes, got %d", rr.Body.Len())
	}
}

func TestClip_NoTenant401(t *testing.T) {
	srv, _, id := newClipFixture(t)
	rr := httptest.NewRecorder()
	srv.ServeClip(rr, clipRequest(t, uuid.Nil, id.String(), ""))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rr.Code)
	}
}

func TestClip_ForeignTenant404(t *testing.T) {
	srv, _, id := newClipFixture(t)
	rr := httptest.NewRecorder()
	srv.ServeClip(rr, clipRequest(t, uuid.New(), id.String(), ""))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rr.Code)
	}
}

func TestClip_UnparsableID404(t *testing.T) {
	srv, tenantID, _ := newClipFixture(t)
	rr := httptest.NewRecorder()
	srv.ServeClip(rr, clipRequest(t, tenantID, "not-a-uuid", ""))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rr.Code)
	}
}

// TestClip_CrossCampaign404: a clip whose highlight belongs to ANOTHER campaign than
// the resolved Active Campaign is 404 — the same campaign-scoping posture the RPCs
// adopt (#308), existence never leaked.
func TestClip_CrossCampaign404(t *testing.T) {
	tenantID, id, otherCampaign := uuid.New(), uuid.New(), uuid.New()
	key, err := blob.Key(tenantID, "highlight", id, "clip.wav")
	if err != nil {
		t.Fatalf("build key: %v", err)
	}
	blobs := newFakeBlobs()
	blobs.data[key] = make([]byte, 200)
	store := &fakeClipStore{tenantID: tenantID, id: id, campaignID: otherCampaign, clipKey: key}
	// The Active Campaign resolves to a DIFFERENT campaign than the highlight's.
	resolve := func(context.Context) (uuid.UUID, bool, error) { return uuid.New(), true, nil }
	srv := NewClipServer(store, blobs, resolve, testLog())

	rr := httptest.NewRecorder()
	srv.ServeClip(rr, clipRequest(t, tenantID, id.String(), ""))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-campaign clip: want 404, got %d", rr.Code)
	}
}

// TestClip_MissingBlob404: the row exists but its clip blob is gone (a purge race) —
// the handler must 404, not 500 (#308, finding #6).
func TestClip_MissingBlob404(t *testing.T) {
	srv, tenantID, id, _ := newClipFixtureCampaign(t)
	// Drop the blob out from under the row (the purge deleted the clip first).
	key, _ := blob.Key(tenantID, "highlight", id, "clip.wav")
	if err := srv.blobs.Delete(context.Background(), key); err != nil {
		t.Fatalf("delete blob: %v", err)
	}
	rr := httptest.NewRecorder()
	srv.ServeClip(rr, clipRequest(t, tenantID, id.String(), ""))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("missing blob: want 404, got %d", rr.Code)
	}
}
