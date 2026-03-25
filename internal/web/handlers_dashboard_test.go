package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHandleDashboardStats(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		role       string
		tenantID   string
		seedData   func(*mockWebStore)
		wantStatus int
		wantStats  *DashboardStats
	}{
		{
			name:       "empty tenant",
			role:       "dm",
			tenantID:   "t1",
			seedData:   func(_ *mockWebStore) {},
			wantStatus: http.StatusOK,
			wantStats: &DashboardStats{
				CampaignCount:      0,
				ActiveSessionCount: 0,
				HoursUsed:          0,
				HoursLimit:         0,
			},
		},
		{
			name:     "with campaigns and sessions",
			role:     "tenant_admin",
			tenantID: "t1",
			seedData: func(ws *mockWebStore) {
				ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "t1", Name: "Camp A"}
				ws.campaigns["c2"] = &Campaign{ID: "c2", TenantID: "t1", Name: "Camp B"}
				ws.campaigns["c3"] = &Campaign{ID: "c3", TenantID: "other", Name: "Other"}
				ws.sessions = []SessionSummary{
					{ID: "s1", TenantID: "t1", State: "running", StartedAt: time.Now()},
					{ID: "s2", TenantID: "t1", State: "ended", StartedAt: time.Now()},
					{ID: "s3", TenantID: "other", State: "running", StartedAt: time.Now()},
				}
				now := time.Now().UTC()
				ws.usage = []UsageRecord{
					{TenantID: "t1", Period: time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC), SessionHours: 3.5},
				}
			},
			wantStatus: http.StatusOK,
			wantStats: &DashboardStats{
				CampaignCount:      2,
				ActiveSessionCount: 1,
				HoursUsed:          3.5,
				HoursLimit:         0,
			},
		},
		{
			name:     "tenant isolation",
			role:     "viewer",
			tenantID: "t2",
			seedData: func(ws *mockWebStore) {
				ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "t1", Name: "Camp A"}
				ws.sessions = []SessionSummary{
					{ID: "s1", TenantID: "t1", State: "running", StartedAt: time.Now()},
				}
			},
			wantStatus: http.StatusOK,
			wantStats: &DashboardStats{
				CampaignCount:      0,
				ActiveSessionCount: 0,
				HoursUsed:          0,
				HoursLimit:         0,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, ws, _, secret := testServerWithStores(t)
			tt.seedData(ws)

			auth := AuthMiddleware(secret)
			srv.mux.Handle("GET /api/v1/dashboard/stats", auth(http.HandlerFunc(srv.handleDashboardStats)))

			req := authReq(t, http.MethodGet, "/api/v1/dashboard/stats", nil, secret, "user-1", tt.tenantID, tt.role)
			rr := httptest.NewRecorder()
			srv.mux.ServeHTTP(rr, req)

			if rr.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rr.Code, tt.wantStatus, rr.Body.String())
			}

			if tt.wantStats != nil {
				var body struct {
					Data DashboardStats `json:"data"`
				}
				if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				if body.Data.CampaignCount != tt.wantStats.CampaignCount {
					t.Errorf("campaign_count = %d, want %d", body.Data.CampaignCount, tt.wantStats.CampaignCount)
				}
				if body.Data.ActiveSessionCount != tt.wantStats.ActiveSessionCount {
					t.Errorf("active_session_count = %d, want %d", body.Data.ActiveSessionCount, tt.wantStats.ActiveSessionCount)
				}
				if body.Data.HoursUsed != tt.wantStats.HoursUsed {
					t.Errorf("hours_used = %f, want %f", body.Data.HoursUsed, tt.wantStats.HoursUsed)
				}
			}
		})
	}
}

func TestHandleDashboardStats_Unauthenticated(t *testing.T) {
	t.Parallel()

	srv, secret := testServer(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/dashboard/stats", auth(http.HandlerFunc(srv.handleDashboardStats)))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/stats", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestHandleDashboardActivity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		tenantID   string
		seedData   func(*mockWebStore)
		wantStatus int
		wantCount  int
	}{
		{
			name:       "empty tenant returns empty array",
			tenantID:   "t1",
			seedData:   func(_ *mockWebStore) {},
			wantStatus: http.StatusOK,
			wantCount:  0,
		},
		{
			name:     "returns activity items",
			tenantID: "t1",
			seedData: func(ws *mockWebStore) {
				now := time.Now().UTC()
				ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "t1", Name: "Camp A", CreatedAt: now}
				ended := now.Add(-1 * time.Hour)
				ws.sessions = []SessionSummary{
					{ID: "s1", TenantID: "t1", State: "running", StartedAt: now},
					{ID: "s2", TenantID: "t1", State: "ended", StartedAt: now.Add(-2 * time.Hour), EndedAt: &ended},
				}
			},
			wantStatus: http.StatusOK,
			wantCount:  3,
		},
		{
			name:     "tenant isolation",
			tenantID: "t2",
			seedData: func(ws *mockWebStore) {
				ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "t1", Name: "Camp A", CreatedAt: time.Now()}
			},
			wantStatus: http.StatusOK,
			wantCount:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, ws, _, secret := testServerWithStores(t)
			tt.seedData(ws)

			auth := AuthMiddleware(secret)
			srv.mux.Handle("GET /api/v1/dashboard/activity", auth(http.HandlerFunc(srv.handleDashboardActivity)))

			req := authReq(t, http.MethodGet, "/api/v1/dashboard/activity", nil, secret, "user-1", tt.tenantID, "dm")
			rr := httptest.NewRecorder()
			srv.mux.ServeHTTP(rr, req)

			raw := rr.Body.String()
			if rr.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rr.Code, tt.wantStatus, raw)
			}

			var body struct {
				Data []ActivityItem `json:"data"`
			}
			if err := json.Unmarshal([]byte(raw), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if len(body.Data) != tt.wantCount {
				t.Errorf("activity count = %d, want %d", len(body.Data), tt.wantCount)
			}

			// Verify empty array, not null.
			if tt.wantCount == 0 {
				var rawBody map[string]json.RawMessage
				if err := json.Unmarshal([]byte(raw), &rawBody); err == nil {
					if string(rawBody["data"]) == "null" {
						t.Error("data should be [], not null")
					}
				}
			}
		})
	}
}

func TestHandleDashboardActivity_Unauthenticated(t *testing.T) {
	t.Parallel()

	srv, secret := testServer(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/dashboard/activity", auth(http.HandlerFunc(srv.handleDashboardActivity)))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/activity", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestHandleDashboardActiveSessions_NoGateway(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/dashboard/active-sessions", auth(http.HandlerFunc(srv.handleDashboardActiveSessions)))

	req := authReq(t, http.MethodGet, "/api/v1/dashboard/active-sessions", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	// gwClient is nil in the test server, so handleListActiveSessions returns 503.
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d; body: %s", rr.Code, http.StatusServiceUnavailable, rr.Body.String())
	}
}

func TestHandleDashboardStats_WithGateway(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	srv.gwClient = newMockMgmtClient()

	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "t1", Name: "Camp A"}

	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/dashboard/stats", auth(http.HandlerFunc(srv.handleDashboardStats)))

	req := authReq(t, http.MethodGet, "/api/v1/dashboard/stats", nil, secret, "user-1", "t1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var body struct {
		Data DashboardStats `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.CampaignCount != 1 {
		t.Errorf("campaign_count = %d, want 1", body.Data.CampaignCount)
	}
}

func TestHandleDashboardActiveSessions_WithGateway(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	srv.gwClient = newMockMgmtClient()

	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/dashboard/active-sessions", auth(http.HandlerFunc(srv.handleDashboardActiveSessions)))

	req := authReq(t, http.MethodGet, "/api/v1/dashboard/active-sessions", nil, secret, "user-1", "tenant-1", "dm")
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
}

func TestHandleDashboardActiveSessions_Unauthenticated(t *testing.T) {
	t.Parallel()

	srv, secret := testServer(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/dashboard/active-sessions", auth(http.HandlerFunc(srv.handleDashboardActiveSessions)))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/active-sessions", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}
