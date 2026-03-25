package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	pb "github.com/MrWong99/glyphoxa/gen/glyphoxa/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// mockMgmtClient implements pb.ManagementServiceClient for testing.
type mockMgmtClient struct {
	pb.ManagementServiceClient
	tenants map[string]*pb.TenantInfo
}

func newMockMgmtClient() *mockMgmtClient {
	return &mockMgmtClient{
		tenants: map[string]*pb.TenantInfo{
			"t1": {Id: "t1", LicenseTier: "shared", CreatedAt: timestamppb.Now(), UpdatedAt: timestamppb.Now()},
		},
	}
}

func (m *mockMgmtClient) ListTenants(_ context.Context, _ *pb.ListTenantsRequest, _ ...grpc.CallOption) (*pb.ListTenantsResponse, error) {
	ts := make([]*pb.TenantInfo, 0, len(m.tenants))
	for _, t := range m.tenants {
		ts = append(ts, t)
	}
	return &pb.ListTenantsResponse{Tenants: ts}, nil
}

func (m *mockMgmtClient) GetTenant(_ context.Context, req *pb.GetTenantRequest, _ ...grpc.CallOption) (*pb.TenantResponse, error) {
	t, ok := m.tenants[req.GetId()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "tenant %q not found", req.GetId())
	}
	return &pb.TenantResponse{Tenant: t}, nil
}

func (m *mockMgmtClient) CreateTenant(_ context.Context, req *pb.CreateTenantRequest, _ ...grpc.CallOption) (*pb.TenantResponse, error) {
	if _, exists := m.tenants[req.GetId()]; exists {
		return nil, status.Errorf(codes.AlreadyExists, "tenant %q already exists", req.GetId())
	}
	t := &pb.TenantInfo{
		Id:          req.GetId(),
		LicenseTier: req.GetLicenseTier(),
		GuildIds:    req.GetGuildIds(),
		CreatedAt:   timestamppb.Now(),
		UpdatedAt:   timestamppb.Now(),
	}
	m.tenants[req.GetId()] = t
	return &pb.TenantResponse{Tenant: t}, nil
}

func (m *mockMgmtClient) UpdateTenant(_ context.Context, req *pb.UpdateTenantRequest, _ ...grpc.CallOption) (*pb.TenantResponse, error) {
	t, ok := m.tenants[req.GetId()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "tenant %q not found", req.GetId())
	}
	if req.GetLicenseTier() != "" {
		t.LicenseTier = req.GetLicenseTier()
	}
	return &pb.TenantResponse{Tenant: t}, nil
}

func (m *mockMgmtClient) DeleteTenant(_ context.Context, req *pb.DeleteTenantRequest, _ ...grpc.CallOption) (*pb.DeleteTenantResponse, error) {
	if _, ok := m.tenants[req.GetId()]; !ok {
		return nil, status.Errorf(codes.NotFound, "tenant %q not found", req.GetId())
	}
	delete(m.tenants, req.GetId())
	return &pb.DeleteTenantResponse{}, nil
}

func (m *mockMgmtClient) StartWebSession(_ context.Context, req *pb.StartWebSessionRequest, _ ...grpc.CallOption) (*pb.StartWebSessionResponse, error) {
	return &pb.StartWebSessionResponse{SessionId: "session-" + req.GetChannelId()}, nil
}

func (m *mockMgmtClient) StopWebSession(_ context.Context, req *pb.StopWebSessionRequest, _ ...grpc.CallOption) (*pb.StopWebSessionResponse, error) {
	return &pb.StopWebSessionResponse{}, nil
}

func (m *mockMgmtClient) ListActiveSessions(_ context.Context, req *pb.ListActiveSessionsRequest, _ ...grpc.CallOption) (*pb.ListActiveSessionsResponse, error) {
	return &pb.ListActiveSessionsResponse{
		Sessions: []*pb.ActiveSessionInfo{
			{
				SessionId:  "s1",
				TenantId:   req.GetTenantId(),
				CampaignId: "c1",
				GuildId:    "g1",
				ChannelId:  "ch1",
				State:      pb.SessionState_SESSION_STATE_ACTIVE,
				StartedAt:  timestamppb.Now(),
			},
		},
	}, nil
}

func TestTenantHandlers_NoGateway(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"list tenants", http.MethodGet, "/api/v1/tenants", ""},
		{"get tenant", http.MethodGet, "/api/v1/tenants/t1", ""},
		{"create tenant", http.MethodPost, "/api/v1/tenants", `{"id":"t1"}`},
		{"update tenant", http.MethodPut, "/api/v1/tenants/t1", `{"display_name":"New"}`},
		{"delete tenant", http.MethodDelete, "/api/v1/tenants/t1", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, _, _, secret := testServerWithStores(t)
			// No gwClient configured — should return 503.
			auth := AuthMiddleware(secret)

			srv.mux.Handle("GET /api/v1/tenants", auth(RequireRole("super_admin")(http.HandlerFunc(srv.handleListTenants))))
			srv.mux.Handle("GET /api/v1/tenants/{id}", auth(RequireRole("tenant_admin")(http.HandlerFunc(srv.handleGetTenant))))
			srv.mux.Handle("POST /api/v1/tenants", auth(RequireRole("super_admin")(http.HandlerFunc(srv.handleCreateTenant))))
			srv.mux.Handle("PUT /api/v1/tenants/{id}", auth(RequireRole("tenant_admin")(http.HandlerFunc(srv.handleUpdateTenant))))
			srv.mux.Handle("DELETE /api/v1/tenants/{id}", auth(RequireRole("super_admin")(http.HandlerFunc(srv.handleDeleteTenant))))

			var body io.Reader
			if tt.body != "" {
				body = bytes.NewBufferString(tt.body)
			}
			req := httptest.NewRequest(tt.method, tt.path, body)
			token := signTestToken(t, secret, "user-1", "t1", "super_admin")
			req.Header.Set("Authorization", "Bearer "+token)

			rr := httptest.NewRecorder()
			srv.mux.ServeHTTP(rr, req)

			if rr.Code != http.StatusServiceUnavailable {
				t.Errorf("status = %d, want %d (no gateway configured)", rr.Code, http.StatusServiceUnavailable)
			}
		})
	}
}

func TestTenantHandlers_GRPCClient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		method   string
		path     string
		body     string
		wantCode int
	}{
		{"list tenants", http.MethodGet, "/api/v1/tenants", "", http.StatusOK},
		{"get tenant", http.MethodGet, "/api/v1/tenants/t1", "", http.StatusOK},
		{"create tenant", http.MethodPost, "/api/v1/tenants", `{"id":"new-t","license_tier":"shared"}`, http.StatusCreated},
		{"update tenant", http.MethodPut, "/api/v1/tenants/t1", `{"license_tier":"dedicated"}`, http.StatusOK},
		{"delete tenant", http.MethodDelete, "/api/v1/tenants/t1", "", http.StatusNoContent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, _, _, secret := testServerWithStores(t)
			srv.gwClient = newMockMgmtClient()

			auth := AuthMiddleware(secret)
			srv.mux.Handle("GET /api/v1/tenants", auth(RequireRole("super_admin")(http.HandlerFunc(srv.handleListTenants))))
			srv.mux.Handle("GET /api/v1/tenants/{id}", auth(RequireRole("tenant_admin")(http.HandlerFunc(srv.handleGetTenant))))
			srv.mux.Handle("POST /api/v1/tenants", auth(RequireRole("super_admin")(http.HandlerFunc(srv.handleCreateTenant))))
			srv.mux.Handle("PUT /api/v1/tenants/{id}", auth(RequireRole("tenant_admin")(http.HandlerFunc(srv.handleUpdateTenant))))
			srv.mux.Handle("DELETE /api/v1/tenants/{id}", auth(RequireRole("super_admin")(http.HandlerFunc(srv.handleDeleteTenant))))

			var body io.Reader
			if tt.body != "" {
				body = bytes.NewBufferString(tt.body)
			}
			req := httptest.NewRequest(tt.method, tt.path, body)
			token := signTestToken(t, secret, "user-1", "t1", "super_admin")
			req.Header.Set("Authorization", "Bearer "+token)

			rr := httptest.NewRecorder()
			srv.mux.ServeHTTP(rr, req)

			if rr.Code != tt.wantCode {
				t.Errorf("status = %d, want %d; body: %s", rr.Code, tt.wantCode, rr.Body.String())
			}
		})
	}
}

func TestHandleGetTenant_TenantIsolation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		userTID  string
		role     string
		targetID string
		wantCode int
	}{
		{"tenant_admin own tenant", "t1", "tenant_admin", "t1", http.StatusOK},
		{"tenant_admin other tenant", "t1", "tenant_admin", "t2", http.StatusForbidden},
		{"super_admin any tenant", "t1", "super_admin", "t2", http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, _, _, secret := testServerWithStores(t)
			srv.gwClient = newMockMgmtClient()

			auth := AuthMiddleware(secret)
			srv.mux.Handle("GET /api/v1/tenants/{id}", auth(RequireRole("tenant_admin")(http.HandlerFunc(srv.handleGetTenant))))

			req := authReq(t, http.MethodGet, "/api/v1/tenants/"+tt.targetID, nil, secret, "user-1", tt.userTID, tt.role)
			rr := httptest.NewRecorder()
			srv.mux.ServeHTTP(rr, req)

			if rr.Code != tt.wantCode {
				t.Errorf("status = %d, want %d", rr.Code, tt.wantCode)
			}
		})
	}
}

func TestHandleUpdateTenant_ClearFields(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	srv.gwClient = newMockMgmtClient()

	auth := AuthMiddleware(secret)
	srv.mux.Handle("PUT /api/v1/tenants/{id}", auth(RequireRole("tenant_admin")(http.HandlerFunc(srv.handleUpdateTenant))))

	// Clear dm_role_id and campaign_id by sending empty strings.
	body := `{"dm_role_id":"","campaign_id":""}`
	req := authReq(t, http.MethodPut, "/api/v1/tenants/t1",
		bytes.NewBufferString(body), secret, "user-1", "t1", "tenant_admin")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d; body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
}

func TestHandleUpdateTenant_SetFields(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	srv.gwClient = newMockMgmtClient()

	auth := AuthMiddleware(secret)
	srv.mux.Handle("PUT /api/v1/tenants/{id}", auth(RequireRole("tenant_admin")(http.HandlerFunc(srv.handleUpdateTenant))))

	body := `{"dm_role_id":"role-123","campaign_id":"camp-456","guild_ids":["g1","g2"],"bot_token":"tok"}`
	req := authReq(t, http.MethodPut, "/api/v1/tenants/t1",
		bytes.NewBufferString(body), secret, "user-1", "t1", "tenant_admin")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d; body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
}

func TestHandleUpdateTenant_TenantIsolation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		userTID  string
		role     string
		targetID string
		wantCode int
	}{
		{"tenant_admin own tenant", "t1", "tenant_admin", "t1", http.StatusOK},
		{"tenant_admin other tenant", "t1", "tenant_admin", "t2", http.StatusForbidden},
		{"super_admin any tenant", "t1", "super_admin", "t2", http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, _, _, secret := testServerWithStores(t)
			srv.gwClient = newMockMgmtClient()

			auth := AuthMiddleware(secret)
			srv.mux.Handle("PUT /api/v1/tenants/{id}", auth(RequireRole("tenant_admin")(http.HandlerFunc(srv.handleUpdateTenant))))

			req := authReq(t, http.MethodPut, "/api/v1/tenants/"+tt.targetID,
				bytes.NewBufferString(`{"license_tier":"dedicated"}`), secret, "user-1", tt.userTID, tt.role)
			rr := httptest.NewRecorder()
			srv.mux.ServeHTTP(rr, req)

			if rr.Code != tt.wantCode {
				t.Errorf("status = %d, want %d", rr.Code, tt.wantCode)
			}
		})
	}
}

func TestHandleCreateTenantSelfService(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		body     string
		wantCode int
	}{
		{
			name:     "valid creation",
			body:     `{"id":"new-tenant","display_name":"New Tenant"}`,
			wantCode: http.StatusCreated,
		},
		{
			name:     "missing id",
			body:     `{"display_name":"NoID"}`,
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "invalid json",
			body:     `{bad`,
			wantCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, _, _, secret := testServerWithStores(t)
			// No gwClient — self-service should still work, just skip the gateway call.
			auth := AuthMiddleware(secret)
			srv.mux.Handle("POST /api/v1/tenants/self-service", auth(http.HandlerFunc(srv.handleCreateTenantSelfService)))

			req := authReq(t, http.MethodPost, "/api/v1/tenants/self-service",
				bytes.NewBufferString(tt.body), secret, "user-1", "default", "dm")
			rr := httptest.NewRecorder()
			srv.mux.ServeHTTP(rr, req)

			if rr.Code != tt.wantCode {
				t.Errorf("status = %d, want %d; body: %s", rr.Code, tt.wantCode, rr.Body.String())
			}
		})
	}
}

func TestHandleCreateTenantSelfService_WithGateway(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	srv.gwClient = newMockMgmtClient()

	auth := AuthMiddleware(secret)
	srv.mux.Handle("POST /api/v1/tenants/self-service", auth(http.HandlerFunc(srv.handleCreateTenantSelfService)))

	req := authReq(t, http.MethodPost, "/api/v1/tenants/self-service",
		bytes.NewBufferString(`{"id":"gw-tenant","display_name":"Gateway Tenant"}`), secret, "user-1", "default", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusCreated)
	}

	var body struct {
		Data struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.ID != "gw-tenant" {
		t.Errorf("id = %q, want %q", body.Data.ID, "gw-tenant")
	}
}

func TestGRPCStatusToHTTP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		code     codes.Code
		wantHTTP int
	}{
		{"OK", codes.OK, 200},
		{"InvalidArgument", codes.InvalidArgument, 400},
		{"NotFound", codes.NotFound, 404},
		{"AlreadyExists", codes.AlreadyExists, 409},
		{"PermissionDenied", codes.PermissionDenied, 403},
		{"Unauthenticated", codes.Unauthenticated, 401},
		{"FailedPrecondition", codes.FailedPrecondition, 412},
		{"Unavailable", codes.Unavailable, 503},
		{"Internal falls to default", codes.Internal, 502},
		{"Unknown falls to default", codes.Unknown, 502},
		{"DeadlineExceeded falls to default", codes.DeadlineExceeded, 502},
		{"Unimplemented falls to default", codes.Unimplemented, 502},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := grpcStatusToHTTP(tt.code)
			if got != tt.wantHTTP {
				t.Errorf("grpcStatusToHTTP(%v) = %d, want %d", tt.code, got, tt.wantHTTP)
			}
		})
	}
}

func TestTenantFromPB_Nil(t *testing.T) {
	t.Parallel()

	result := tenantFromPB(nil)
	if result != nil {
		t.Errorf("tenantFromPB(nil) = %v, want nil", result)
	}
}

func TestHandleCreateTenant_InvalidJSON(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	srv.gwClient = newMockMgmtClient()

	auth := AuthMiddleware(secret)
	srv.mux.Handle("POST /api/v1/tenants", auth(RequireRole("super_admin")(http.HandlerFunc(srv.handleCreateTenant))))

	req := authReq(t, http.MethodPost, "/api/v1/tenants",
		bytes.NewBufferString(`{bad json`), secret, "user-1", "t1", "super_admin")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestHandleUpdateTenant_InvalidJSON(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	srv.gwClient = newMockMgmtClient()

	auth := AuthMiddleware(secret)
	srv.mux.Handle("PUT /api/v1/tenants/{id}", auth(RequireRole("tenant_admin")(http.HandlerFunc(srv.handleUpdateTenant))))

	req := authReq(t, http.MethodPut, "/api/v1/tenants/t1",
		bytes.NewBufferString(`{bad json`), secret, "user-1", "t1", "tenant_admin")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestHandleCreateTenantSelfService_Unauthenticated(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("POST /api/v1/tenants/self-service", auth(http.HandlerFunc(srv.handleCreateTenantSelfService)))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/self-service",
		bytes.NewBufferString(`{"id":"test"}`))
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestHandleCreateTenantSelfService_GatewayConflict(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	srv.gwClient = newMockMgmtClient() // t1 already exists

	auth := AuthMiddleware(secret)
	srv.mux.Handle("POST /api/v1/tenants/self-service", auth(http.HandlerFunc(srv.handleCreateTenantSelfService)))

	// Try to create t1 which already exists.
	req := authReq(t, http.MethodPost, "/api/v1/tenants/self-service",
		bytes.NewBufferString(`{"id":"t1"}`), secret, "user-1", "default", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d (already exists)", rr.Code, http.StatusConflict)
	}
}

func TestHandleDeleteTenant_GRPCNotFound(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	srv.gwClient = newMockMgmtClient()

	auth := AuthMiddleware(secret)
	srv.mux.Handle("DELETE /api/v1/tenants/{id}", auth(RequireRole("super_admin")(http.HandlerFunc(srv.handleDeleteTenant))))

	req := authReq(t, http.MethodDelete, "/api/v1/tenants/nonexistent", nil, secret, "user-1", "t1", "super_admin")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestHandleCreateTenant_AlreadyExists(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	srv.gwClient = newMockMgmtClient() // t1 exists

	auth := AuthMiddleware(secret)
	srv.mux.Handle("POST /api/v1/tenants", auth(RequireRole("super_admin")(http.HandlerFunc(srv.handleCreateTenant))))

	req := authReq(t, http.MethodPost, "/api/v1/tenants",
		bytes.NewBufferString(`{"id":"t1","license_tier":"shared"}`), secret, "user-1", "t1", "super_admin")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusConflict)
	}
}

func TestTenantHandlers_InsufficientRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		method string
		path   string
		role   string
	}{
		{"list requires super_admin", http.MethodGet, "/api/v1/tenants", "tenant_admin"},
		{"create requires super_admin", http.MethodPost, "/api/v1/tenants", "tenant_admin"},
		{"delete requires super_admin", http.MethodDelete, "/api/v1/tenants/t1", "tenant_admin"},
		{"get requires tenant_admin", http.MethodGet, "/api/v1/tenants/t1", "dm"},
		{"update requires tenant_admin", http.MethodPut, "/api/v1/tenants/t1", "dm"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, _, _, secret := testServerWithStores(t)
			srv.gwClient = newMockMgmtClient()

			auth := AuthMiddleware(secret)
			srv.mux.Handle("GET /api/v1/tenants", auth(RequireRole("super_admin")(http.HandlerFunc(srv.handleListTenants))))
			srv.mux.Handle("GET /api/v1/tenants/{id}", auth(RequireRole("tenant_admin")(http.HandlerFunc(srv.handleGetTenant))))
			srv.mux.Handle("POST /api/v1/tenants", auth(RequireRole("super_admin")(http.HandlerFunc(srv.handleCreateTenant))))
			srv.mux.Handle("PUT /api/v1/tenants/{id}", auth(RequireRole("tenant_admin")(http.HandlerFunc(srv.handleUpdateTenant))))
			srv.mux.Handle("DELETE /api/v1/tenants/{id}", auth(RequireRole("super_admin")(http.HandlerFunc(srv.handleDeleteTenant))))

			req := httptest.NewRequest(tt.method, tt.path, nil)
			token := signTestToken(t, secret, "user-1", "t1", tt.role)
			req.Header.Set("Authorization", "Bearer "+token)

			rr := httptest.NewRecorder()
			srv.mux.ServeHTTP(rr, req)

			if rr.Code != http.StatusForbidden {
				t.Errorf("status = %d, want %d", rr.Code, http.StatusForbidden)
			}
		})
	}
}
