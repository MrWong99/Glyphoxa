//go:build integration

package bundle_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/bundle"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/wirenpc"
)

// fakeAuthN authenticates exactly one token, so the mount's RequireSession gate
// can be exercised without a real session row.
type fakeAuthN struct{ token string }

func (f fakeAuthN) AuthenticateSession(_ context.Context, token string) (storage.User, error) {
	if token == f.token {
		return storage.User{ID: uuid.New(), Name: "op", Role: "operator"}, nil
	}
	return storage.User{}, storage.ErrNotFound
}

// exportRoute mounts ServeExport exactly as cmd/glyphoxa/main.go does — behind
// auth.RequireSession on GET /api/v1/campaigns/{id}/export — so the test drives
// the guarded route through a real ServeMux.
func exportRoute(st *storage.Store, token string) http.Handler {
	h := &bundle.Handler{Store: st}
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/campaigns/{id}/export",
		auth.RequireSession(fakeAuthN{token: token}, http.HandlerFunc(h.ServeExport)))
	return mux
}

func TestServeExport(t *testing.T) {
	ctx := context.Background()
	st, cid, _ := seededCampaign(t)
	const token = "valid-session-token"
	route := exportRoute(st, token)

	authed := func(method, target string) *http.Request {
		req := httptest.NewRequest(method, target, nil)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
		return req
	}

	t.Run("no cookie is 401", func(t *testing.T) {
		rec := httptest.NewRecorder()
		route.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/campaigns/"+cid.String()+"/export", nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("code=%d, want 401", rec.Code)
		}
	})

	t.Run("bad uuid is 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		route.ServeHTTP(rec, authed(http.MethodGet, "/api/v1/campaigns/not-a-uuid/export"))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d, want 400", rec.Code)
		}
	})

	t.Run("unknown id is 404", func(t *testing.T) {
		rec := httptest.NewRecorder()
		route.ServeHTTP(rec, authed(http.MethodGet, "/api/v1/campaigns/"+uuid.New().String()+"/export"))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("code=%d, want 404", rec.Code)
		}
	})

	t.Run("200 gzip with filename", func(t *testing.T) {
		rec := httptest.NewRecorder()
		route.ServeHTTP(rec, authed(http.MethodGet, "/api/v1/campaigns/"+cid.String()+"/export"))
		if rec.Code != http.StatusOK {
			t.Fatalf("code=%d, want 200", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/gzip" {
			t.Errorf("Content-Type=%q, want application/gzip", ct)
		}
		wantCD := `attachment; filename="` + bundle.Filename(wirenpc.SeedCampaignName) + `"`
		if cd := rec.Header().Get("Content-Disposition"); cd != wantCD {
			t.Errorf("Content-Disposition=%q, want %q", cd, wantCD)
		}
		b := decodeBody(t, rec)
		if b.Campaign.History != nil {
			t.Errorf("default export nested History")
		}
	})

	t.Run("include_history honored", func(t *testing.T) {
		// Add transcript rows so history is non-empty.
		vs, err := st.CreateVoiceSession(ctx, cid)
		if err != nil {
			t.Fatalf("CreateVoiceSession: %v", err)
		}
		if err := st.UpsertTranscriptLine(ctx, storage.TranscriptLine{
			VoiceSessionID: vs.ID, CampaignID: cid, LineID: "l1", Seq: 1,
			Who: "Frodo", Kind: "human", TS: time.Now().UTC(), Text: "hi",
		}); err != nil {
			t.Fatalf("UpsertTranscriptLine: %v", err)
		}
		rec := httptest.NewRecorder()
		route.ServeHTTP(rec, authed(http.MethodGet, "/api/v1/campaigns/"+cid.String()+"/export?include_history=true"))
		if rec.Code != http.StatusOK {
			t.Fatalf("code=%d, want 200", rec.Code)
		}
		b := decodeBody(t, rec)
		if b.Campaign.History == nil || len(b.Campaign.History.Sessions) == 0 {
			t.Errorf("include_history=true did not nest sessions")
		}
	})
}

// TestServeExportArchivedCampaign proves an archived campaign is still
// exportable (backup path, ADR-0053 §7).
func TestServeExportArchivedCampaign(t *testing.T) {
	ctx := context.Background()
	st, cid, _ := seededCampaign(t)
	if _, err := st.ArchiveCampaign(ctx, cid); err != nil {
		t.Fatalf("ArchiveCampaign: %v", err)
	}
	const token = "valid-session-token"
	route := exportRoute(st, token)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/campaigns/"+cid.String()+"/export", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	route.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("archived export code=%d, want 200", rec.Code)
	}
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) *bundle.Bundle {
	t.Helper()
	// Decode sniffs the gzip magic and inflates transparently.
	b, err := bundle.Decode(rec.Body)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	return b
}
