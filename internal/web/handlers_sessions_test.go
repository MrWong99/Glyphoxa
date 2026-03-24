package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHandleListSessions(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/sessions", auth(http.HandlerFunc(srv.handleListSessions)))

	now := time.Now().UTC()
	ws.sessions = []SessionSummary{
		{ID: "s1", TenantID: "tenant-1", State: "active", CreatedAt: now},
		{ID: "s2", TenantID: "tenant-1", State: "ended", CreatedAt: now.Add(-time.Hour)},
		{ID: "s3", TenantID: "tenant-2", State: "active", CreatedAt: now},
	}

	req := authReq(t, http.MethodGet, "/api/v1/sessions", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var body struct {
		Data []SessionSummary `json:"data"`
		Meta struct {
			Page    int `json:"page"`
			PerPage int `json:"per_page"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 2 {
		t.Errorf("got %d sessions, want 2 (tenant-1 only)", len(body.Data))
	}
	if body.Meta.Page != 1 {
		t.Errorf("page = %d, want 1", body.Meta.Page)
	}
	if body.Meta.PerPage != 25 {
		t.Errorf("per_page = %d, want 25 (default)", body.Meta.PerPage)
	}
}

func TestHandleListSessions_Pagination(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/sessions", auth(http.HandlerFunc(srv.handleListSessions)))

	now := time.Now().UTC()
	for i := range 5 {
		ws.sessions = append(ws.sessions, SessionSummary{
			ID:        "s" + string(rune('1'+i)),
			TenantID:  "tenant-1",
			State:     "ended",
			CreatedAt: now.Add(-time.Duration(i) * time.Hour),
		})
	}

	// Page 1, 2 per page.
	req := authReq(t, http.MethodGet, "/api/v1/sessions?per_page=2&page=1", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var body struct {
		Data []SessionSummary `json:"data"`
		Meta struct {
			Page    int `json:"page"`
			PerPage int `json:"per_page"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 2 {
		t.Errorf("got %d sessions, want 2", len(body.Data))
	}
	if body.Meta.PerPage != 2 {
		t.Errorf("per_page = %d, want 2", body.Meta.PerPage)
	}

	// Page 3, 2 per page — should get 1 result.
	req2 := authReq(t, http.MethodGet, "/api/v1/sessions?per_page=2&page=3", nil, secret, "user-1", "tenant-1", "dm")
	rr2 := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr2, req2)

	if rr2.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr2.Code, http.StatusOK)
	}

	var body2 struct {
		Data []SessionSummary `json:"data"`
	}
	if err := json.NewDecoder(rr2.Body).Decode(&body2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body2.Data) != 1 {
		t.Errorf("got %d sessions on page 3, want 1", len(body2.Data))
	}
}

func TestHandleListSessions_Empty(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/sessions", auth(http.HandlerFunc(srv.handleListSessions)))

	req := authReq(t, http.MethodGet, "/api/v1/sessions", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var body struct {
		Data []SessionSummary `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data == nil {
		t.Error("data should be empty array, not null")
	}
}

func TestHandleListSessions_DefaultPagination(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/sessions", auth(http.HandlerFunc(srv.handleListSessions)))

	// Invalid pagination values should use defaults.
	req := authReq(t, http.MethodGet, "/api/v1/sessions?per_page=-1&page=0", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var body struct {
		Meta struct {
			Page    int `json:"page"`
			PerPage int `json:"per_page"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Meta.Page != 1 {
		t.Errorf("page = %d, want 1 (default)", body.Meta.Page)
	}
	if body.Meta.PerPage != 25 {
		t.Errorf("per_page = %d, want 25 (default)", body.Meta.PerPage)
	}
}

func TestHandleGetTranscript(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/sessions/{id}/transcript", auth(http.HandlerFunc(srv.handleGetTranscript)))

	now := time.Now().UTC()
	ws.transcripts["session-1"] = []TranscriptEntry{
		{ID: 1, Speaker: "Heinrich", Content: "Willkommen!", CreatedAt: now},
		{ID: 2, Speaker: "Player", Content: "Hello there", CreatedAt: now.Add(time.Second)},
	}

	req := authReq(t, http.MethodGet, "/api/v1/sessions/session-1/transcript", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var body struct {
		Data []TranscriptEntry `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 2 {
		t.Errorf("got %d entries, want 2", len(body.Data))
	}
	if body.Data[0].Speaker != "Heinrich" {
		t.Errorf("first speaker = %q, want %q", body.Data[0].Speaker, "Heinrich")
	}
}

func TestHandleGetTranscript_Empty(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/sessions/{id}/transcript", auth(http.HandlerFunc(srv.handleGetTranscript)))

	req := authReq(t, http.MethodGet, "/api/v1/sessions/no-session/transcript", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var body struct {
		Data []TranscriptEntry `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data == nil {
		t.Error("data should be empty array, not null")
	}
}
