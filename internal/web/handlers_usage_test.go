package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHandleGetUsage(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/usage", auth(http.HandlerFunc(srv.handleGetUsage)))

	now := time.Now().UTC()
	ws.usage = []UsageRecord{
		{TenantID: "tenant-1", Period: now, SessionHours: 10, LLMTokens: 5000, TTSChars: 1000, STTSeconds: 300},
		{TenantID: "tenant-1", Period: now.AddDate(0, -1, 0), SessionHours: 5, LLMTokens: 2500, TTSChars: 500, STTSeconds: 150},
		{TenantID: "tenant-2", Period: now, SessionHours: 3, LLMTokens: 1000, TTSChars: 200, STTSeconds: 60},
	}

	req := authReq(t, http.MethodGet, "/api/v1/usage", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var body struct {
		Data []UsageRecord `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 2 {
		t.Errorf("got %d usage records, want 2 (tenant-1 only)", len(body.Data))
	}
}

func TestHandleGetUsage_CustomDateRange(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/usage", auth(http.HandlerFunc(srv.handleGetUsage)))

	ws.usage = []UsageRecord{
		{TenantID: "tenant-1", Period: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), SessionHours: 10},
		{TenantID: "tenant-1", Period: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), SessionHours: 20},
		{TenantID: "tenant-1", Period: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), SessionHours: 30},
	}

	req := authReq(t, http.MethodGet, "/api/v1/usage?from=2026-01-15&to=2026-02-15", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var body struct {
		Data []UsageRecord `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 1 {
		t.Errorf("got %d records, want 1 (only Feb within range)", len(body.Data))
	}
}

func TestHandleGetUsage_Empty(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/usage", auth(http.HandlerFunc(srv.handleGetUsage)))

	req := authReq(t, http.MethodGet, "/api/v1/usage", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var body struct {
		Data []UsageRecord `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data == nil {
		t.Error("data should be empty array, not null")
	}
}

func TestHandleGetUsage_InvalidDateIgnored(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/usage", auth(http.HandlerFunc(srv.handleGetUsage)))

	// Invalid dates should be silently ignored, using defaults.
	req := authReq(t, http.MethodGet, "/api/v1/usage?from=not-a-date&to=also-bad", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (invalid dates should be ignored)", rr.Code, http.StatusOK)
	}
}

func TestHandleGetUsage_Unauthenticated(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/usage", auth(http.HandlerFunc(srv.handleGetUsage)))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}
