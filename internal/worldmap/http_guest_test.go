package worldmap

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/blob"
)

// A Campaign Guest on the Map image mount (ADR-0065): a gm_private Map does
// not exist for a guest without wiki.secrets, and a Map of another Campaign of
// the host Tenant does not exist for any guest.
func TestImage_GuestAndPrivateMaps(t *testing.T) {
	tenantID, id, campaignID := uuid.New(), uuid.New(), uuid.New()
	key, err := blob.Key(tenantID, "map", id, "image")
	if err != nil {
		t.Fatalf("blob.Key: %v", err)
	}
	mk := func(private bool) *ImageServer {
		blobs := &fakeBlobs{data: map[string][]byte{key: wantBytes}}
		store := &fakeMapStore{campaignID: campaignID, id: id, blobKey: key, gmPrivate: private}
		resolve := func(ctx context.Context) (uuid.UUID, bool, error) {
			// The production resolver pins a guest to their Campaign.
			if g, ok := auth.Guest(ctx); ok {
				return g.CampaignID, true, nil
			}
			return campaignID, true, nil
		}
		return NewImageServer(store, blobs, resolve, nil)
	}
	request := func(guestCampaign uuid.UUID, rights ...auth.GuestRight) *http.Request {
		req := imageRequest(t, tenantID, id.String())
		if guestCampaign == uuid.Nil {
			return req
		}
		return req.WithContext(auth.WithGuest(req.Context(),
			auth.GuestScope{CampaignID: guestCampaign, Rights: auth.NewRightSet(rights...)}))
	}
	serve := func(srv *ImageServer, req *http.Request) int {
		rr := httptest.NewRecorder()
		srv.ServeImage(rr, req)
		return rr.Code
	}

	cases := []struct {
		name    string
		private bool
		req     *http.Request
		want    int
	}{
		{"guest, public map", false, request(campaignID, auth.RightMapsRead), http.StatusOK},
		{"guest without secrets, private map", true, request(campaignID, auth.RightMapsRead), http.StatusNotFound},
		{"guest with maps.edit only, private map", true, request(campaignID, auth.RightMapsEdit), http.StatusNotFound},
		{"guest with secrets, private map", true, request(campaignID, auth.RightMapsRead, auth.RightWikiSecrets), http.StatusOK},
		{"the GM, private map", true, request(uuid.Nil), http.StatusOK},
		{"guest of another campaign of the host tenant", false, request(uuid.New(), auth.RightMapsRead, auth.RightWikiSecrets), http.StatusNotFound},
	}
	for _, tc := range cases {
		if got := serve(mk(tc.private), tc.req); got != tc.want {
			t.Errorf("%s: status = %d, want %d", tc.name, got, tc.want)
		}
	}
}
