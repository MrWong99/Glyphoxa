package web

import (
	"bytes"
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
		{ID: "s1", TenantID: "tenant-1", State: "active", StartedAt: now},
		{ID: "s2", TenantID: "tenant-1", State: "ended", StartedAt: now.Add(-time.Hour)},
		{ID: "s3", TenantID: "tenant-2", State: "active", StartedAt: now},
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
			StartedAt: now.Add(-time.Duration(i) * time.Hour),
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
	ws.sessions = []SessionSummary{
		{ID: "session-1", TenantID: "tenant-1", State: "ended", StartedAt: now},
	}
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

func TestHandleGetTranscript_NotFound(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/sessions/{id}/transcript", auth(http.HandlerFunc(srv.handleGetTranscript)))

	req := authReq(t, http.MethodGet, "/api/v1/sessions/no-session/transcript", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestHandleGetTranscript_EmptyReturnsArray(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/sessions/{id}/transcript", auth(http.HandlerFunc(srv.handleGetTranscript)))

	ws.sessions = []SessionSummary{
		{ID: "session-empty", TenantID: "tenant-1", State: "ended", StartedAt: time.Now()},
	}
	// No transcript entries seeded.

	req := authReq(t, http.MethodGet, "/api/v1/sessions/session-empty/transcript", nil, secret, "user-1", "tenant-1", "dm")
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

func TestHandleGetTranscript_Unauthenticated(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/sessions/{id}/transcript", auth(http.HandlerFunc(srv.handleGetTranscript)))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/s1/transcript", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestHandleStartSession_NoGateway(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("POST /api/v1/sessions/start", auth(RequireRole("dm")(http.HandlerFunc(srv.handleStartSession))))

	body := `{"campaign_id":"c1","guild_id":"g1","channel_id":"ch1"}`
	req := authReq(t, http.MethodPost, "/api/v1/sessions/start",
		bytes.NewBufferString(body), secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}
}

func TestHandleStartSession_InvalidJSON(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	srv.gwClient = newMockMgmtClient()
	auth := AuthMiddleware(secret)
	srv.mux.Handle("POST /api/v1/sessions/start", auth(RequireRole("dm")(http.HandlerFunc(srv.handleStartSession))))

	req := authReq(t, http.MethodPost, "/api/v1/sessions/start",
		bytes.NewBufferString(`{bad`), secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestHandleStartSession_MissingFields(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	srv.gwClient = newMockMgmtClient()
	auth := AuthMiddleware(secret)
	srv.mux.Handle("POST /api/v1/sessions/start", auth(RequireRole("dm")(http.HandlerFunc(srv.handleStartSession))))

	tests := []struct {
		name string
		body string
	}{
		{"missing guild_id", `{"channel_id":"ch1"}`},
		{"missing channel_id", `{"guild_id":"g1"}`},
		{"both missing", `{"campaign_id":"c1"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := authReq(t, http.MethodPost, "/api/v1/sessions/start",
				bytes.NewBufferString(tt.body), secret, "user-1", "tenant-1", "dm")
			rr := httptest.NewRecorder()
			srv.mux.ServeHTTP(rr, req)

			if rr.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d; body: %s", rr.Code, http.StatusBadRequest, rr.Body.String())
			}
		})
	}
}

func TestHandleStartSession_Unauthenticated(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("POST /api/v1/sessions/start", auth(RequireRole("dm")(http.HandlerFunc(srv.handleStartSession))))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/start",
		bytes.NewBufferString(`{"guild_id":"g1","channel_id":"ch1"}`))
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestHandleStopSession_NoGateway(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("POST /api/v1/sessions/{id}/stop", auth(RequireRole("dm")(http.HandlerFunc(srv.handleStopSession))))

	req := authReq(t, http.MethodPost, "/api/v1/sessions/s1/stop", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}
}

func TestHandleStopSession_Unauthenticated(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("POST /api/v1/sessions/{id}/stop", auth(RequireRole("dm")(http.HandlerFunc(srv.handleStopSession))))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/s1/stop", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestHandleListActiveSessions_NoGateway(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/sessions/active", auth(http.HandlerFunc(srv.handleListActiveSessions)))

	req := authReq(t, http.MethodGet, "/api/v1/sessions/active", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}
}

func TestHandleListActiveSessions_Unauthenticated(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/sessions/active", auth(http.HandlerFunc(srv.handleListActiveSessions)))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/active", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestHandleListSessions_Unauthenticated(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/sessions", auth(http.HandlerFunc(srv.handleListSessions)))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestHandleStartSession_WithGateway(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	srv.gwClient = newMockMgmtClient()
	auth := AuthMiddleware(secret)
	srv.mux.Handle("POST /api/v1/sessions/start", auth(RequireRole("dm")(http.HandlerFunc(srv.handleStartSession))))

	body := `{"campaign_id":"c1","guild_id":"g1","channel_id":"ch1"}`
	req := authReq(t, http.MethodPost, "/api/v1/sessions/start",
		bytes.NewBufferString(body), secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusCreated, rr.Body.String())
	}

	var resp struct {
		Data struct {
			SessionID string `json:"session_id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.SessionID == "" {
		t.Error("expected non-empty session_id")
	}
}

func TestHandleStopSession_WithGateway(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	srv.gwClient = newMockMgmtClient()
	auth := AuthMiddleware(secret)
	srv.mux.Handle("POST /api/v1/sessions/{id}/stop", auth(RequireRole("dm")(http.HandlerFunc(srv.handleStopSession))))

	// Seed session belonging to tenant-1.
	ws.sessions = []SessionSummary{
		{ID: "s1", TenantID: "tenant-1", State: "active", StartedAt: time.Now()},
	}

	req := authReq(t, http.MethodPost, "/api/v1/sessions/s1/stop", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d; body: %s", rr.Code, http.StatusNoContent, rr.Body.String())
	}
}

func TestHandleStopSession_CrossTenantBlocked(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	srv.gwClient = newMockMgmtClient()
	auth := AuthMiddleware(secret)
	srv.mux.Handle("POST /api/v1/sessions/{id}/stop", auth(RequireRole("dm")(http.HandlerFunc(srv.handleStopSession))))

	// Session belongs to tenant-1.
	ws.sessions = []SessionSummary{
		{ID: "session-t1", TenantID: "tenant-1", State: "active", StartedAt: time.Now()},
	}

	// Authenticate as tenant-2 — should be blocked.
	req := authReq(t, http.MethodPost, "/api/v1/sessions/session-t1/stop", nil, secret, "user-2", "tenant-2", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d (cross-tenant stop should be blocked)", rr.Code, http.StatusNotFound)
	}
}

func TestHandleListActiveSessions_WithGateway(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	srv.gwClient = newMockMgmtClient()
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/sessions/active", auth(http.HandlerFunc(srv.handleListActiveSessions)))

	req := authReq(t, http.MethodGet, "/api/v1/sessions/active", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var resp struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Errorf("got %d active sessions, want 1", len(resp.Data))
	}
	if resp.Data[0]["session_id"] != "s1" {
		t.Errorf("session_id = %v, want s1", resp.Data[0]["session_id"])
	}
}

func TestHandleGetTranscript_WrongTenant(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/sessions/{id}/transcript", auth(http.HandlerFunc(srv.handleGetTranscript)))

	// Session belongs to tenant-1.
	ws.sessions = []SessionSummary{
		{ID: "session-1", TenantID: "tenant-1", State: "ended", StartedAt: time.Now()},
	}

	// Authenticate as tenant-2.
	req := authReq(t, http.MethodGet, "/api/v1/sessions/session-1/transcript", nil, secret, "user-2", "tenant-2", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d (wrong tenant)", rr.Code, http.StatusNotFound)
	}
}
