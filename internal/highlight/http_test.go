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
	imageKey   string // empty = no image yet (#311)
	imageCT    string
}

func (f *fakeClipStore) GetHighlight(_ context.Context, tenantID, id uuid.UUID) (storage.Highlight, error) {
	if tenantID != f.tenantID || id != f.id {
		return storage.Highlight{}, storage.ErrNotFound
	}
	return storage.Highlight{
		ID:               f.id,
		TenantID:         f.tenantID,
		CampaignID:       f.campaignID,
		Status:           storage.HighlightCandidate,
		ClipKey:          f.clipKey,
		ClipContentType:  "audio/wav",
		ImageKey:         f.imageKey,
		ImageContentType: f.imageCT,
		CreatedAt:        time.Now(),
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

// --- image serve (#311) ---

// imageRequest builds a GET /highlights/{id}/image request with the tenant injected.
func imageRequest(t *testing.T, tenantID uuid.UUID, id, rangeHdr string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/highlights/"+id+"/image", nil)
	req.SetPathValue("id", id)
	if tenantID != uuid.Nil {
		req = req.WithContext(auth.WithTenant(req.Context(), tenantID))
	}
	if rangeHdr != "" {
		req.Header.Set("Range", rangeHdr)
	}
	return req
}

// newImageFixture builds a ClipServer over a highlight that HAS an image, plus a
// resolver that resolves the Active Campaign to its campaign.
func newImageFixture(t *testing.T) (*ClipServer, uuid.UUID, uuid.UUID) {
	t.Helper()
	tenantID, id, campaignID := uuid.New(), uuid.New(), uuid.New()
	clipKey, _ := blob.Key(tenantID, "highlight", id, "clip.wav")
	imgKey, err := blob.Key(tenantID, "highlight", id, "image")
	if err != nil {
		t.Fatalf("build image key: %v", err)
	}
	blobs := newFakeBlobs()
	blobs.data[imgKey] = make([]byte, 300)
	store := &fakeClipStore{tenantID: tenantID, id: id, campaignID: campaignID, clipKey: clipKey, imageKey: imgKey, imageCT: "image/png"}
	resolve := func(context.Context) (uuid.UUID, bool, error) { return campaignID, true, nil }
	return NewClipServer(store, blobs, resolve, testLog()), tenantID, id
}

func TestImage_ServesWithContentType(t *testing.T) {
	srv, tenantID, id := newImageFixture(t)
	rr := httptest.NewRecorder()
	srv.ServeImage(rr, imageRequest(t, tenantID, id.String(), ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("want image/png, got %q", ct)
	}
	if rr.Body.Len() != 300 {
		t.Fatalf("want 300 bytes, got %d", rr.Body.Len())
	}
}

func TestImage_RangeReturnsPartial(t *testing.T) {
	srv, tenantID, id := newImageFixture(t)
	rr := httptest.NewRecorder()
	srv.ServeImage(rr, imageRequest(t, tenantID, id.String(), "bytes=0-149"))
	if rr.Code != http.StatusPartialContent {
		t.Fatalf("want 206, got %d", rr.Code)
	}
	if rr.Body.Len() != 150 {
		t.Fatalf("want 150 bytes, got %d", rr.Body.Len())
	}
}

func TestImage_EmptyImageKey404(t *testing.T) {
	// A highlight with no image yet: image_key == "".
	tenantID, id, campaignID := uuid.New(), uuid.New(), uuid.New()
	clipKey, _ := blob.Key(tenantID, "highlight", id, "clip.wav")
	store := &fakeClipStore{tenantID: tenantID, id: id, campaignID: campaignID, clipKey: clipKey}
	resolve := func(context.Context) (uuid.UUID, bool, error) { return campaignID, true, nil }
	srv := NewClipServer(store, newFakeBlobs(), resolve, testLog())

	rr := httptest.NewRecorder()
	srv.ServeImage(rr, imageRequest(t, tenantID, id.String(), ""))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("no image yet: want 404, got %d", rr.Code)
	}
}

func TestImage_ForeignTenant404(t *testing.T) {
	srv, _, id := newImageFixture(t)
	rr := httptest.NewRecorder()
	srv.ServeImage(rr, imageRequest(t, uuid.New(), id.String(), ""))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rr.Code)
	}
}

func TestImage_CrossCampaign404(t *testing.T) {
	tenantID, id, otherCampaign := uuid.New(), uuid.New(), uuid.New()
	imgKey, _ := blob.Key(tenantID, "highlight", id, "image")
	blobs := newFakeBlobs()
	blobs.data[imgKey] = make([]byte, 100)
	store := &fakeClipStore{tenantID: tenantID, id: id, campaignID: otherCampaign, imageKey: imgKey, imageCT: "image/png"}
	// Active Campaign resolves to a different campaign than the highlight's.
	resolve := func(context.Context) (uuid.UUID, bool, error) { return uuid.New(), true, nil }
	srv := NewClipServer(store, blobs, resolve, testLog())

	rr := httptest.NewRecorder()
	srv.ServeImage(rr, imageRequest(t, tenantID, id.String(), ""))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-campaign image: want 404, got %d", rr.Code)
	}
}

// --- real middleware chain (#408) ---
//
// These drive ServeClip through the ACTUAL auth.RequireSession + auth.RequireTenant
// stack (the production mount composition) with only a session cookie — no
// pre-injected tenant. This is the class of test the unit tests above (which
// pre-inject auth.WithTenant) could not catch: #408 was that the mounts wrapped
// only RequireSession, so TenantID always missed and every clip 401'd.

// chainAuthN is a one-token Authenticator for the middleware chain tests.
type chainAuthN struct {
	token string
	user  storage.User
}

func (a chainAuthN) AuthenticateSession(_ context.Context, token string) (storage.User, error) {
	if token == a.token {
		return a.user, nil
	}
	return storage.User{}, storage.ErrNotFound
}

// chainTenant resolves every operator to a fixed tenant (the thin ADR-0039
// pass-through), server-side — the point being the client never supplies it.
type chainTenant struct{ tenantID uuid.UUID }

func (t chainTenant) TenantForUser(context.Context, uuid.UUID) (uuid.UUID, error) {
	return t.tenantID, nil
}

// TestClip_RealChainServesWithCookieOnly proves that a request carrying ONLY a
// valid session cookie (the tenant is NOT pre-injected) is served 200 with bytes
// through the real RequireSession→RequireTenant→ServeClip chain. Regression guard
// for #408.
func TestClip_RealChainServesWithCookieOnly(t *testing.T) {
	srv, tenantID, id := newClipFixture(t)
	const token = "sess-abc"
	authN := chainAuthN{token: token, user: storage.User{ID: uuid.New(), Role: "operator"}}
	chain := auth.RequireSession(authN, auth.RequireTenant(chainTenant{tenantID: tenantID}, http.HandlerFunc(srv.ServeClip)))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/highlights/"+id.String()+"/clip", nil)
	req.SetPathValue("id", id.String())
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	chain.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("real chain, cookie only: want 200, got %d", rr.Code)
	}
	if rr.Body.Len() != 200 {
		t.Fatalf("want 200 bytes, got %d", rr.Body.Len())
	}
}

// TestClip_RealChainForeignTenant404 proves the chain still enforces tenant
// scoping: when the operator's resolved tenant differs from the highlight's owner,
// the row reads as absent → 404, existence never leaked.
func TestClip_RealChainForeignTenant404(t *testing.T) {
	srv, _, id := newClipFixture(t)
	const token = "sess-abc"
	authN := chainAuthN{token: token, user: storage.User{ID: uuid.New(), Role: "operator"}}
	// A tenant that does NOT own the fixture highlight.
	chain := auth.RequireSession(authN, auth.RequireTenant(chainTenant{tenantID: uuid.New()}, http.HandlerFunc(srv.ServeClip)))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/highlights/"+id.String()+"/clip", nil)
	req.SetPathValue("id", id.String())
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	chain.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("real chain, foreign tenant: want 404, got %d", rr.Code)
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
