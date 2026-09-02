package worldmap

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/blob"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// fakeMapStore returns a single Map for its owning Campaign.
type fakeMapStore struct {
	campaignID uuid.UUID
	id         uuid.UUID
	blobKey    string // empty = a Map with no picture (an imageless bundle import)
}

func (f *fakeMapStore) GetMap(_ context.Context, campaignID, id uuid.UUID) (storage.CampaignMap, error) {
	if campaignID != f.campaignID || id != f.id {
		return storage.CampaignMap{}, storage.ErrNotFound
	}
	return storage.CampaignMap{
		ID: f.id, CampaignID: f.campaignID, Name: "Saltmarsh", BlobKey: f.blobKey,
		WidthPx: 800, HeightPx: 600, UpdatedAt: time.Unix(1_700_000_000, 0),
	}, nil
}

// fakeBlobs mirrors blob.Postgres closely enough for the handler's error
// branches to be real. In particular Get VALIDATES THE KEY FIRST and returns
// ErrInvalidKey — not ErrNotFound — for an empty one, exactly as the Postgres
// backend does. A fake that answered ErrNotFound for "" would make the
// empty-key test pass with no handler change at all, which is the wrong reason.
type fakeBlobs struct{ data map[string][]byte }

func (f *fakeBlobs) Put(_ context.Context, key, _ string, r io.Reader, _ int64) error {
	if _, err := blob.ValidateKey(key); err != nil {
		return err
	}
	b, _ := io.ReadAll(r)
	f.data[key] = b
	return nil
}

func (f *fakeBlobs) Get(_ context.Context, key string) (io.ReadCloser, blob.Meta, error) {
	if _, err := blob.ValidateKey(key); err != nil {
		return nil, blob.Meta{}, err
	}
	b, ok := f.data[key]
	if !ok {
		return nil, blob.Meta{}, blob.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), blob.Meta{ContentType: "image/png", Size: int64(len(b))}, nil
}

func (f *fakeBlobs) Delete(_ context.Context, key string) error {
	if _, err := blob.ValidateKey(key); err != nil {
		return err
	}
	delete(f.data, key)
	return nil
}

func (f *fakeBlobs) List(_ context.Context, _ string) ([]string, error) { return nil, nil }

// imageRequest builds a GET with the tenant injected the way the guarded mount
// does (#408): the handler re-reads it, so a request without one is 401.
func imageRequest(t *testing.T, tenantID uuid.UUID, id string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/maps/"+id+"/image", nil)
	req.SetPathValue("id", id)
	if tenantID != uuid.Nil {
		req = req.WithContext(auth.WithTenant(req.Context(), tenantID))
	}
	return req
}

// wantBytes is the picture the served fixture holds.
var wantBytes = []byte("map bytes")

// mapState is how a fixture's Map and blob store are set up to disagree (or not).
type mapState int

const (
	// served: a key, and the bytes it names. The ordinary Map.
	served mapState = iota
	// keyedButEmpty: a well-formed key naming bytes that are gone — the
	// reconciliation race the handler already guards.
	keyedButEmpty
	// keyless: no key at all, which is what an imageless bundle import leaves.
	keyless
)

// newImageFixture wires a server over one Map in that state. It returns the
// server, the tenant the guarded mount would have injected, and the Map's id.
func newImageFixture(t *testing.T, state mapState) (*ImageServer, uuid.UUID, uuid.UUID) {
	t.Helper()
	tenantID, id, campaignID := uuid.New(), uuid.New(), uuid.New()

	var key string
	if state != keyless {
		k, err := blob.Key(tenantID, "map", id, "image")
		if err != nil {
			t.Fatalf("blob.Key: %v", err)
		}
		key = k
	}
	blobs := &fakeBlobs{data: map[string][]byte{}}
	if state == served {
		blobs.data[key] = wantBytes
	}

	store := &fakeMapStore{campaignID: campaignID, id: id, blobKey: key}
	resolve := func(context.Context) (uuid.UUID, bool, error) { return campaignID, true, nil }
	return NewImageServer(store, blobs, resolve, nil), tenantID, id
}

// TestImage_ServesTheBytes is the happy path, and the anti-vacuity guard for the
// 404 tests below: without it a handler that answered 404 unconditionally would
// pass every other test here.
func TestImage_ServesTheBytes(t *testing.T) {
	srv, tenantID, id := newImageFixture(t, served)

	rr := httptest.NewRecorder()
	srv.ServeImage(rr, imageRequest(t, tenantID, id.String()))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got := rr.Body.Bytes(); !bytes.Equal(got, wantBytes) {
		t.Errorf("body = %q, want %q", got, wantBytes)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("content type = %q, want image/png", ct)
	}
	// The mount echoes a stored Content-Type same-origin, so it must also carry the
	// headers that keep a scriptable blob from ever executing (#591 posture).
	if v := rr.Header().Get("X-Content-Type-Options"); v != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", v)
	}
	if v := rr.Header().Get("Content-Security-Policy"); v != "default-src 'none'; style-src 'unsafe-inline'" {
		t.Errorf("Content-Security-Policy = %q", v)
	}
}

// TestImage_AMapWithNoPictureIs404 is the regression for the imageless-import
// state. A bundle exported without -include-images restores maps with an EMPTY
// blob_key, and blob.Get("") is ErrInvalidKey — NOT ErrNotFound — so falling
// through to the seam logged a bogus internal error and answered 500 for an
// ordinary, expected state. It is a plain 404, like any other absent image.
func TestImage_AMapWithNoPictureIs404(t *testing.T) {
	srv, tenantID, id := newImageFixture(t, keyless)

	rr := httptest.NewRecorder()
	srv.ServeImage(rr, imageRequest(t, tenantID, id.String()))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (a map with no image is missing, not broken)", rr.Code)
	}
}

// TestImage_MissingBlobIs404: the row and its bytes should agree, but a
// reconciliation race must not 500.
func TestImage_MissingBlobIs404(t *testing.T) {
	srv, tenantID, id := newImageFixture(t, keyedButEmpty)

	rr := httptest.NewRecorder()
	srv.ServeImage(rr, imageRequest(t, tenantID, id.String()))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

// TestImage_NoTenantIs401: the mount injects the tenant (#408/#446), so a missing
// one is a miswired mount and rejects fail-closed rather than serving.
func TestImage_NoTenantIs401(t *testing.T) {
	srv, _, id := newImageFixture(t, served)

	rr := httptest.NewRecorder()
	srv.ServeImage(rr, imageRequest(t, uuid.Nil, id.String()))

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

// TestImage_AMapOutsideTheActiveCampaignIs404: the row load is campaign-scoped, so
// another campaign's Map is indistinguishable from one that does not exist.
func TestImage_AMapOutsideTheActiveCampaignIs404(t *testing.T) {
	srv, tenantID, _ := newImageFixture(t, served)

	rr := httptest.NewRecorder()
	srv.ServeImage(rr, imageRequest(t, tenantID, uuid.New().String()))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}
