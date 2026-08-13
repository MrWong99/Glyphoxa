package nodeportrait

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

// fakePortraitStore answers for a single Node in its owning Campaign.
type fakePortraitStore struct {
	campaignID uuid.UUID
	id         uuid.UUID
	blobKey    string // empty = a Node with no portrait (the ordinary default)
}

func (f *fakePortraitStore) NodePortrait(_ context.Context, campaignID, nodeID uuid.UUID) (string, time.Time, error) {
	if campaignID != f.campaignID || nodeID != f.id {
		return "", time.Time{}, storage.ErrNotFound
	}
	return f.blobKey, time.Unix(1_700_000_000, 0), nil
}

// fakeBlobs mirrors blob.Postgres closely enough for the handler's error
// branches to be real: Get VALIDATES THE KEY FIRST and returns ErrInvalidKey —
// not ErrNotFound — for an empty one, exactly as the Postgres backend does. A
// fake that answered ErrNotFound for "" would make the empty-key test pass with
// no handler change at all, which is the wrong reason (the worldmap suite's
// lesson).
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

// portraitRequest builds a GET with the tenant injected the way the guarded
// mount does (#408): the handler re-reads it, so a request without one is 401.
func portraitRequest(t *testing.T, tenantID uuid.UUID, id string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/knowledge/nodes/"+id+"/portrait", nil)
	req.SetPathValue("id", id)
	if tenantID != uuid.Nil {
		req = req.WithContext(auth.WithTenant(req.Context(), tenantID))
	}
	return req
}

// wantBytes is the picture the served fixture holds.
var wantBytes = []byte("portrait bytes")

// portraitState is how a fixture's Node and blob store are set up to disagree
// (or not).
type portraitState int

const (
	// served: a key, and the bytes it names. The ordinary portrait.
	served portraitState = iota
	// keyedButEmpty: a well-formed key naming bytes that are gone — the
	// reconciliation race the handler guards.
	keyedButEmpty
	// keyless: no key at all — a Node that simply has no portrait, which is
	// most Nodes.
	keyless
)

// newFixture wires a server over one Node in that state. It returns the server,
// the tenant the guarded mount would have injected, and the Node's id.
func newFixture(t *testing.T, state portraitState) (*Server, uuid.UUID, uuid.UUID) {
	t.Helper()
	tenantID, id, campaignID := uuid.New(), uuid.New(), uuid.New()

	var key string
	if state != keyless {
		k, err := blob.Key(tenantID, "node", id, "portrait")
		if err != nil {
			t.Fatalf("blob.Key: %v", err)
		}
		key = k
	}
	blobs := &fakeBlobs{data: map[string][]byte{}}
	if state == served {
		blobs.data[key] = wantBytes
	}

	store := &fakePortraitStore{campaignID: campaignID, id: id, blobKey: key}
	resolve := func(context.Context) (uuid.UUID, bool, error) { return campaignID, true, nil }
	return NewServer(store, blobs, resolve, nil), tenantID, id
}

// TestPortrait_ServesTheBytes is the happy path, and the anti-vacuity guard for
// the 404 tests below: without it a handler that answered 404 unconditionally
// would pass every other test here.
func TestPortrait_ServesTheBytes(t *testing.T) {
	srv, tenantID, id := newFixture(t, served)

	rr := httptest.NewRecorder()
	srv.ServePortrait(rr, portraitRequest(t, tenantID, id.String()))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got := rr.Body.Bytes(); !bytes.Equal(got, wantBytes) {
		t.Errorf("body = %q, want %q", got, wantBytes)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("content type = %q, want image/png", ct)
	}
	if lm := rr.Header().Get("Last-Modified"); lm == "" {
		t.Error("no Last-Modified — updated_at is the cache validator and must be served")
	}
}

// TestPortrait_ANodeWithNoPortraitIs404: most Nodes carry no portrait, and
// blob.Get("") is ErrInvalidKey — NOT ErrNotFound — so falling through to the
// seam would log a bogus internal error and answer 500 for the most ordinary
// state there is. It is a plain 404, like any other absent image.
func TestPortrait_ANodeWithNoPortraitIs404(t *testing.T) {
	srv, tenantID, id := newFixture(t, keyless)

	rr := httptest.NewRecorder()
	srv.ServePortrait(rr, portraitRequest(t, tenantID, id.String()))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (a node with no portrait is missing, not broken)", rr.Code)
	}
}

// TestPortrait_MissingBlobIs404: the row and its bytes should agree, but a
// reconciliation race must not 500.
func TestPortrait_MissingBlobIs404(t *testing.T) {
	srv, tenantID, id := newFixture(t, keyedButEmpty)

	rr := httptest.NewRecorder()
	srv.ServePortrait(rr, portraitRequest(t, tenantID, id.String()))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

// TestPortrait_NoTenantIs401: the mount injects the tenant (#408/#446), so a
// missing one is a miswired mount and rejects fail-closed rather than serving.
func TestPortrait_NoTenantIs401(t *testing.T) {
	srv, _, id := newFixture(t, served)

	rr := httptest.NewRecorder()
	srv.ServePortrait(rr, portraitRequest(t, uuid.Nil, id.String()))

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

// TestPortrait_ANodeOutsideTheActiveCampaignIs404: the row load is
// campaign-scoped, so another campaign's Node is indistinguishable from one
// that does not exist.
func TestPortrait_ANodeOutsideTheActiveCampaignIs404(t *testing.T) {
	srv, tenantID, _ := newFixture(t, served)

	rr := httptest.NewRecorder()
	srv.ServePortrait(rr, portraitRequest(t, tenantID, uuid.New().String()))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}
