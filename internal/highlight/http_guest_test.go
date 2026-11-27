package highlight

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/blob"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// A Campaign Guest on the Highlight byte mounts (ADR-0065). The row is loaded
// by the request's Tenant, which for a guest is the HOST Tenant — so the
// campaign check and the promoted-only rule are all that stand between a guest
// and every asset the host owns.

// guestAssetFixture is a server over one highlight with all three assets, in
// the given status, resolving the Active Campaign to resolved.
func guestAssetFixture(t *testing.T, status string) (srv *ClipServer, store *fakeClipStore, tenantID, id uuid.UUID) {
	t.Helper()
	tenantID, id = uuid.New(), uuid.New()
	campaignID := uuid.New()
	blobs := newFakeBlobs()
	keys := map[string]string{}
	for _, kind := range []string{"clip.wav", "image.png", "sound.mp3"} {
		k, err := blob.Key(tenantID, "highlight", id, kind)
		if err != nil {
			t.Fatalf("blob.Key: %v", err)
		}
		blobs.data[k] = []byte(kind)
		keys[kind] = k
	}
	store = &fakeClipStore{
		tenantID: tenantID, id: id, campaignID: campaignID, status: status,
		clipKey: keys["clip.wav"], imageKey: keys["image.png"], imageCT: "image/png",
		soundKey: keys["sound.mp3"], soundCT: "audio/mpeg",
	}
	resolve := func(ctx context.Context) (uuid.UUID, bool, error) {
		// The production resolver pins a guest to their Campaign.
		if g, ok := auth.Guest(ctx); ok {
			return g.CampaignID, true, nil
		}
		return campaignID, true, nil
	}
	return NewClipServer(store, blobs, resolve, testLog()), store, tenantID, id
}

func guestAssetRequest(tenantID, id, guestCampaign uuid.UUID, kind string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/highlights/"+id.String()+"/"+kind, nil)
	req.SetPathValue("id", id.String())
	ctx := auth.WithTenant(req.Context(), tenantID)
	if guestCampaign != uuid.Nil {
		ctx = auth.WithGuest(ctx, auth.GuestScope{CampaignID: guestCampaign, Rights: auth.NewRightSet(auth.RightHighlightsRead)})
	}
	return req.WithContext(ctx)
}

func TestHighlightAssets_GuestGetsPromotedOnlyInTheirCampaign(t *testing.T) {
	routes := map[string]func(*ClipServer) http.HandlerFunc{
		"clip":  func(s *ClipServer) http.HandlerFunc { return s.ServeClip },
		"image": func(s *ClipServer) http.HandlerFunc { return s.ServeImage },
		"sound": func(s *ClipServer) http.HandlerFunc { return s.ServeSound },
	}
	for kind, route := range routes {
		serve := func(srv *ClipServer, req *http.Request) int {
			rr := httptest.NewRecorder()
			route(srv)(rr, req)
			return rr.Code
		}

		// A promoted highlight of the guest's own campaign is served.
		srv, store, tenantID, id := guestAssetFixture(t, storage.HighlightPromoted)
		if code := serve(srv, guestAssetRequest(tenantID, id, store.campaignID, kind)); code != http.StatusOK {
			t.Errorf("%s: guest on a promoted highlight of their campaign = %d, want 200", kind, code)
		}
		// The same highlight for a guest of ANOTHER campaign of the same host
		// tenant: the tenant-scoped row load finds it, the campaign check 404s.
		if code := serve(srv, guestAssetRequest(tenantID, id, uuid.New(), kind)); code != http.StatusNotFound {
			t.Errorf("%s: guest of another campaign of the host tenant = %d, want 404", kind, code)
		}

		// A candidate does not exist for a guest, even in their own campaign…
		srv, store, tenantID, id = guestAssetFixture(t, storage.HighlightCandidate)
		if code := serve(srv, guestAssetRequest(tenantID, id, store.campaignID, kind)); code != http.StatusNotFound {
			t.Errorf("%s: guest on a CANDIDATE = %d, want 404", kind, code)
		}
		// …while the GM still gets it.
		if code := serve(srv, guestAssetRequest(tenantID, id, uuid.Nil, kind)); code != http.StatusOK {
			t.Errorf("%s: the GM on a candidate = %d, want 200", kind, code)
		}

		// With no campaign resolver a GM is scoped by tenant alone; a guest,
		// whose tenant is the host's, must be served nothing.
		_, store, tenantID, id = guestAssetFixture(t, storage.HighlightPromoted)
		blobs := newFakeBlobs()
		blobs.data[store.clipKey], blobs.data[store.imageKey], blobs.data[store.soundKey] = []byte("c"), []byte("i"), []byte("s")
		unscoped := NewClipServer(store, blobs, nil, testLog())
		if code := serve(unscoped, guestAssetRequest(tenantID, id, store.campaignID, kind)); code != http.StatusNotFound {
			t.Errorf("%s: guest on a server with no campaign resolver = %d, want 404 (fail closed)", kind, code)
		}
		if code := serve(unscoped, guestAssetRequest(tenantID, id, uuid.Nil, kind)); code != http.StatusOK {
			t.Errorf("%s: the GM on a server with no campaign resolver = %d, want 200", kind, code)
		}
	}
}
