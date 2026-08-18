package highlight

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/blob"
)

// --- GET /api/v1/highlights/{id}/sound (#312) ---

func soundRequest(t *testing.T, tenantID uuid.UUID, id string, rangeHdr string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/highlights/"+id+"/sound", nil)
	req.SetPathValue("id", id)
	if tenantID != uuid.Nil {
		req = req.WithContext(auth.WithTenant(req.Context(), tenantID))
	}
	if rangeHdr != "" {
		req.Header.Set("Range", rangeHdr)
	}
	return req
}

// newSoundFixture mirrors newClipFixtureCampaign with a landed sound blob.
func newSoundFixture(t *testing.T) (*ClipServer, uuid.UUID, uuid.UUID) {
	t.Helper()
	tenantID, id, campaignID := uuid.New(), uuid.New(), uuid.New()
	key, err := blob.Key(tenantID, "highlight", id, soundBlobName)
	if err != nil {
		t.Fatalf("build key: %v", err)
	}
	blobs := newFakeBlobs()
	blobs.data[key] = make([]byte, 150)
	store := &fakeClipStore{tenantID: tenantID, id: id, campaignID: campaignID, soundKey: key, soundCT: "audio/mpeg"}
	resolve := func(context.Context) (uuid.UUID, bool, error) { return campaignID, true, nil }
	return NewClipServer(store, blobs, resolve, testLog()), tenantID, id
}

func TestSound_ServesWithContentType(t *testing.T) {
	srv, tenantID, id := newSoundFixture(t)
	rr := httptest.NewRecorder()
	srv.ServeSound(rr, soundRequest(t, tenantID, id.String(), ""))

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "audio/mpeg" {
		t.Fatalf("want audio/mpeg, got %q", ct)
	}
	if rr.Body.Len() != 150 {
		t.Fatalf("want 150 bytes, got %d", rr.Body.Len())
	}
}

func TestSound_RangeReturnsPartial(t *testing.T) {
	srv, tenantID, id := newSoundFixture(t)
	rr := httptest.NewRecorder()
	srv.ServeSound(rr, soundRequest(t, tenantID, id.String(), "bytes=0-49"))

	if rr.Code != http.StatusPartialContent {
		t.Fatalf("want 206 Partial Content, got %d", rr.Code)
	}
	if rr.Body.Len() != 50 {
		t.Fatalf("want 50 bytes, got %d", rr.Body.Len())
	}
}

// TestSound_EmptySoundKey404 pins the not-landed posture: requested-but-pending
// (or never requested) serves 404, indistinguishable from a foreign id.
func TestSound_EmptySoundKey404(t *testing.T) {
	tenantID, id, campaignID := uuid.New(), uuid.New(), uuid.New()
	store := &fakeClipStore{tenantID: tenantID, id: id, campaignID: campaignID}
	resolve := func(context.Context) (uuid.UUID, bool, error) { return campaignID, true, nil }
	srv := NewClipServer(store, newFakeBlobs(), resolve, testLog())

	rr := httptest.NewRecorder()
	srv.ServeSound(rr, soundRequest(t, tenantID, id.String(), ""))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rr.Code)
	}
}

func TestSound_NoTenant401(t *testing.T) {
	srv, _, id := newSoundFixture(t)
	rr := httptest.NewRecorder()
	srv.ServeSound(rr, soundRequest(t, uuid.Nil, id.String(), ""))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rr.Code)
	}
}

func TestSound_ForeignTenant404(t *testing.T) {
	srv, _, id := newSoundFixture(t)
	rr := httptest.NewRecorder()
	srv.ServeSound(rr, soundRequest(t, uuid.New(), id.String(), ""))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rr.Code)
	}
}

// TestSound_CrossCampaign404 pins the Active-Campaign scoping: a sound whose
// highlight belongs to another campaign is 404 (existence never leaked).
func TestSound_CrossCampaign404(t *testing.T) {
	tenantID, id := uuid.New(), uuid.New()
	key, err := blob.Key(tenantID, "highlight", id, soundBlobName)
	if err != nil {
		t.Fatalf("build key: %v", err)
	}
	blobs := newFakeBlobs()
	blobs.data[key] = make([]byte, 10)
	store := &fakeClipStore{tenantID: tenantID, id: id, campaignID: uuid.New(), soundKey: key, soundCT: "audio/mpeg"}
	resolve := func(context.Context) (uuid.UUID, bool, error) { return uuid.New(), true, nil } // a DIFFERENT campaign
	srv := NewClipServer(store, blobs, resolve, testLog())

	rr := httptest.NewRecorder()
	srv.ServeSound(rr, soundRequest(t, tenantID, id.String(), ""))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rr.Code)
	}
}
