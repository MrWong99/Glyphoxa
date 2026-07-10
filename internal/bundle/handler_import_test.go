//go:build integration

package bundle_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/bundle"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// fixedAuthN authenticates one token to one specific operator, so the import
// mount's tenant resolution (TenantForUser) has a real user to key off.
type fixedAuthN struct {
	token string
	user  storage.User
}

func (f fixedAuthN) AuthenticateSession(_ context.Context, token string) (storage.User, error) {
	if token == f.token {
		return f.user, nil
	}
	return storage.User{}, storage.ErrNotFound
}

// importRoute mounts ServeImport exactly as cmd/glyphoxa/main.go does — behind
// auth.RequireSession THEN auth.RequireCSRF on POST /api/v1/campaigns/import.
func importRoute(st *storage.Store, authN auth.Authenticator) http.Handler {
	h := &bundle.Handler{Store: st}
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/campaigns/import",
		auth.RequireSession(authN, auth.RequireCSRF(http.HandlerFunc(h.ServeImport))))
	return mux
}

// operatorStore migrates a fresh DB, creates an operator user + bound tenant, and
// returns the store, the user, and the tenant id.
func operatorStore(t *testing.T) (*storage.Store, storage.User, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	st := storage.New(migratedPool(t))
	user, err := st.UpsertUser(ctx, storage.UpsertUserParams{DiscordUserID: "op-1", Name: "Operator"})
	if err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}
	tenant, err := st.ResolveOperatorTenant(ctx, user.ID)
	if err != nil {
		t.Fatalf("ResolveOperatorTenant: %v", err)
	}
	return st, user, tenant.ID
}

// multipartBundle builds a POST body carrying raw as the "bundle" file field and
// returns the body plus the multipart Content-Type.
func multipartBundle(t *testing.T, raw []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("bundle", "campaign.glyphoxa.json.gz")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := fw.Write(raw); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return &buf, w.FormDataContentType()
}

// encodeBundle gzip-encodes a bundle to bytes for upload.
func encodeBundle(t *testing.T, b *bundle.Bundle) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := bundle.Encode(&buf, b); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return buf.Bytes()
}

const (
	importToken = "valid-session-token"
	importCSRF  = "csrf-token-123"
)

// authedImport builds a POST /api/v1/campaigns/import request with the session
// cookie, the CSRF cookie, and the matching X-CSRF-Token header.
func authedImport(t *testing.T, body io.Reader, contentType string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/campaigns/import", body)
	req.Header.Set("Content-Type", contentType)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: importToken})
	req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: importCSRF})
	req.Header.Set("X-CSRF-Token", importCSRF)
	return req
}

func TestServeImport(t *testing.T) {
	ctx := context.Background()
	st, user, tenantID := operatorStore(t)
	route := importRoute(st, fixedAuthN{token: importToken, user: user})

	good := encodeBundle(t, &bundle.Bundle{
		FormatVersion: bundle.FormatVersion,
		Campaign:      bundle.Campaign{Name: "Uploaded Campaign", System: "dnd5e", Language: "en"},
	})

	t.Run("no cookie is 401", func(t *testing.T) {
		body, ct := multipartBundle(t, good)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/campaigns/import", body)
		req.Header.Set("Content-Type", ct)
		rec := httptest.NewRecorder()
		route.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("code=%d, want 401", rec.Code)
		}
	})

	t.Run("CSRF mismatch is 403", func(t *testing.T) {
		body, ct := multipartBundle(t, good)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/campaigns/import", body)
		req.Header.Set("Content-Type", ct)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: importToken})
		req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: importCSRF})
		req.Header.Set("X-CSRF-Token", "wrong")
		rec := httptest.NewRecorder()
		route.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("code=%d, want 403", rec.Code)
		}
	})

	t.Run("oversized upload is 413", func(t *testing.T) {
		huge := make([]byte, (32<<20)+4096) // > blob.MaxSize
		body, ct := multipartBundle(t, huge)
		rec := httptest.NewRecorder()
		route.ServeHTTP(rec, authedImport(t, body, ct))
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("code=%d, want 413", rec.Code)
		}
	})

	t.Run("newer version is 400 naming both versions", func(t *testing.T) {
		newer := encodeBundle(t, &bundle.Bundle{
			FormatVersion: bundle.FormatVersion + 1,
			Campaign:      bundle.Campaign{Name: "Future", System: "dnd5e", Language: "en"},
		})
		body, ct := multipartBundle(t, newer)
		rec := httptest.NewRecorder()
		route.ServeHTTP(rec, authedImport(t, body, ct))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d, want 400", rec.Code)
		}
		var payload struct {
			Error string `json:"error"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
			t.Fatalf("decode error body: %v", err)
		}
		want := strconv.Itoa(bundle.FormatVersion + 1)
		if !strings.Contains(payload.Error, want) || !strings.Contains(payload.Error, strconv.Itoa(bundle.FormatVersion)) {
			t.Errorf("error %q does not name both versions", payload.Error)
		}
	})

	t.Run("valid upload is 200 with counts and resolves tenant", func(t *testing.T) {
		body, ct := multipartBundle(t, good)
		rec := httptest.NewRecorder()
		route.ServeHTTP(rec, authedImport(t, body, ct))
		if rec.Code != http.StatusOK {
			t.Fatalf("code=%d body=%q, want 200", rec.Code, rec.Body.String())
		}
		var payload struct {
			CampaignID string `json:"campaign_id"`
			Name       string `json:"name"`
			Sessions   int    `json:"sessions"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
			t.Fatalf("decode 200 body: %v", err)
		}
		if payload.Name != "Uploaded Campaign" || payload.CampaignID == "" {
			t.Fatalf("payload = %+v", payload)
		}
		if payload.Sessions != 0 {
			t.Errorf("part-1 reported sessions=%d, want 0", payload.Sessions)
		}
		// The campaign landed under the operator's tenant (tenant resolved from the
		// session user, not a client header).
		if _, err := st.FindCampaignByName(ctx, tenantID, "Uploaded Campaign"); err != nil {
			t.Fatalf("imported campaign not found under operator tenant: %v", err)
		}
	})
}
