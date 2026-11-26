package nodeportrait

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/blob"
)

// A Campaign Guest on the Node portrait mount (ADR-0065): a gm_private entry's
// picture does not exist for a guest without wiki.secrets, and an entry of
// another Campaign of the host Tenant does not exist for any guest.
func TestPortrait_GuestAndPrivateEntries(t *testing.T) {
	tenantID, id, campaignID := uuid.New(), uuid.New(), uuid.New()
	key, err := blob.Key(tenantID, "node", id, "portrait")
	if err != nil {
		t.Fatalf("blob.Key: %v", err)
	}
	mk := func(private bool) *Server {
		blobs := &fakeBlobs{data: map[string][]byte{key: wantBytes}}
		store := &fakePortraitStore{campaignID: campaignID, id: id, blobKey: key, gmPrivate: private}
		resolve := func(ctx context.Context) (uuid.UUID, bool, error) {
			// The production resolver pins a guest to their Campaign.
			if g, ok := auth.Guest(ctx); ok {
				return g.CampaignID, true, nil
			}
			return campaignID, true, nil
		}
		return NewServer(store, blobs, resolve, nil)
	}
	request := func(guestCampaign uuid.UUID, rights ...auth.GuestRight) *http.Request {
		req := portraitRequest(t, tenantID, id.String())
		if guestCampaign == uuid.Nil {
			return req
		}
		return req.WithContext(auth.WithGuest(req.Context(),
			auth.GuestScope{CampaignID: guestCampaign, Rights: auth.NewRightSet(rights...)}))
	}
	serve := func(srv *Server, req *http.Request) int {
		rr := httptest.NewRecorder()
		srv.ServePortrait(rr, req)
		return rr.Code
	}

	cases := []struct {
		name    string
		private bool
		req     *http.Request
		want    int
	}{
		{"guest, public entry", false, request(campaignID, auth.RightWikiRead), http.StatusOK},
		{"guest without secrets, private entry", true, request(campaignID, auth.RightWikiRead), http.StatusNotFound},
		{"guest with secrets, private entry", true, request(campaignID, auth.RightWikiRead, auth.RightWikiSecrets), http.StatusOK},
		{"guest with wiki.edit, private entry", true, request(campaignID, auth.RightWikiEdit), http.StatusOK},
		{"the GM, private entry", true, request(uuid.Nil), http.StatusOK},
		{"guest of another campaign of the host tenant", false, request(uuid.New(), auth.RightWikiEdit), http.StatusNotFound},
	}
	for _, tc := range cases {
		if got := serve(mk(tc.private), tc.req); got != tc.want {
			t.Errorf("%s: status = %d, want %d", tc.name, got, tc.want)
		}
	}
}
