package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleMe_Authenticated(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/auth/me", auth(http.HandlerFunc(srv.handleMe)))

	// Seed user.
	ws.users["user-1"] = &User{
		ID:          "user-1",
		TenantID:    "tenant-1",
		DiscordID:   strPtr("discord-1"),
		DisplayName: "TestUser",
		Role:        "dm",
	}

	req := authReq(t, http.MethodGet, "/api/v1/auth/me", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var body struct {
		Data User `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.ID != "user-1" {
		t.Errorf("user ID = %q, want %q", body.Data.ID, "user-1")
	}
	if body.Data.DisplayName != "TestUser" {
		t.Errorf("display_name = %q, want %q", body.Data.DisplayName, "TestUser")
	}
}

func TestHandleMe_UserNotFound(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/auth/me", auth(http.HandlerFunc(srv.handleMe)))

	// No user seeded — token references a nonexistent user.
	req := authReq(t, http.MethodGet, "/api/v1/auth/me", nil, secret, "nonexistent", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestHandleRefresh_Authenticated(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("POST /api/v1/auth/refresh", auth(http.HandlerFunc(srv.handleRefresh)))

	ws.users["user-1"] = &User{
		ID:       "user-1",
		TenantID: "tenant-1",
		Role:     "tenant_admin",
	}

	req := authReq(t, http.MethodPost, "/api/v1/auth/refresh", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var body struct {
		Data struct {
			AccessToken string `json:"access_token"`
			TokenType   string `json:"token_type"`
			ExpiresIn   int    `json:"expires_in"`
			User        User   `json:"user"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.AccessToken == "" {
		t.Error("expected non-empty access_token")
	}
	if body.Data.TokenType != "Bearer" {
		t.Errorf("token_type = %q, want %q", body.Data.TokenType, "Bearer")
	}
	if body.Data.ExpiresIn != 86400 {
		t.Errorf("expires_in = %d, want %d", body.Data.ExpiresIn, 86400)
	}
	// The refreshed token should use current DB role (tenant_admin), not the old role (dm).
	if body.Data.User.Role != "tenant_admin" {
		t.Errorf("refreshed user role = %q, want %q", body.Data.User.Role, "tenant_admin")
	}
}

func TestHandleRefresh_UserNotFound(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("POST /api/v1/auth/refresh", auth(http.HandlerFunc(srv.handleRefresh)))

	// No user in store.
	req := authReq(t, http.MethodPost, "/api/v1/auth/refresh", nil, secret, "gone-user", "t1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestHandleRefresh_Unauthenticated(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("POST /api/v1/auth/refresh", auth(http.HandlerFunc(srv.handleRefresh)))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestHandleDiscordCallback_MissingState(t *testing.T) {
	t.Parallel()

	srv, _ := testServer(t)
	srv.mux.HandleFunc("GET /api/v1/auth/discord/callback", srv.handleDiscordCallback)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/discord/callback?code=test&state=abc", nil)
	// No state cookie set.
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestHandleDiscordCallback_StateMismatch(t *testing.T) {
	t.Parallel()

	srv, _ := testServer(t)
	srv.mux.HandleFunc("GET /api/v1/auth/discord/callback", srv.handleDiscordCallback)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/discord/callback?code=test&state=abc", nil)
	req.AddCookie(&http.Cookie{Name: "glyphoxa_oauth_state", Value: "xyz"})
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestHandleDiscordCallback_MissingCode(t *testing.T) {
	t.Parallel()

	srv, _ := testServer(t)
	srv.mux.HandleFunc("GET /api/v1/auth/discord/callback", srv.handleDiscordCallback)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/discord/callback?state=abc", nil)
	req.AddCookie(&http.Cookie{Name: "glyphoxa_oauth_state", Value: "abc"})
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestHandleDiscordCallback_DiscordError(t *testing.T) {
	t.Parallel()

	srv, _ := testServer(t)
	srv.mux.HandleFunc("GET /api/v1/auth/discord/callback", srv.handleDiscordCallback)

	// The handler checks code before error, so include a code param to reach the error branch.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/discord/callback?state=abc&code=test&error=access_denied&error_description=user+denied", nil)
	req.AddCookie(&http.Cookie{Name: "glyphoxa_oauth_state", Value: "abc"})
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}

	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != "discord_error" {
		t.Errorf("error code = %q, want %q", body.Error.Code, "discord_error")
	}
}

func TestHandleAPIKeyLogin_Success(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.cfg.AdminAPIKey = "test-admin-key-12345"
	srv.mux.HandleFunc("POST /api/v1/auth/apikey", srv.handleAPIKeyLogin)

	body := bytes.NewBufferString(`{"api_key":"test-admin-key-12345"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/apikey", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var resp struct {
		Data struct {
			AccessToken string `json:"access_token"`
			TokenType   string `json:"token_type"`
			ExpiresIn   int    `json:"expires_in"`
			User        User   `json:"user"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.AccessToken == "" {
		t.Error("expected non-empty access_token")
	}
	if resp.Data.TokenType != "Bearer" {
		t.Errorf("token_type = %q, want %q", resp.Data.TokenType, "Bearer")
	}
	if resp.Data.User.Role != "super_admin" {
		t.Errorf("role = %q, want %q", resp.Data.User.Role, "super_admin")
	}
	if resp.Data.User.ID != adminUserID {
		t.Errorf("user ID = %q, want %q", resp.Data.User.ID, adminUserID)
	}
}

func TestHandleAPIKeyLogin_InvalidKey(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.cfg.AdminAPIKey = "correct-key"
	srv.mux.HandleFunc("POST /api/v1/auth/apikey", srv.handleAPIKeyLogin)

	body := bytes.NewBufferString(`{"api_key":"wrong-key"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/apikey", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestHandleAPIKeyLogin_NotConfigured(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	// AdminAPIKey left empty.
	srv.mux.HandleFunc("POST /api/v1/auth/apikey", srv.handleAPIKeyLogin)

	body := bytes.NewBufferString(`{"api_key":"anything"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/apikey", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestHandleAPIKeyLogin_MissingKey(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.cfg.AdminAPIKey = "some-key"
	srv.mux.HandleFunc("POST /api/v1/auth/apikey", srv.handleAPIKeyLogin)

	body := bytes.NewBufferString(`{}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/apikey", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestHandleDiscordLogin_WithInvite(t *testing.T) {
	t.Parallel()

	srv, _ := testServer(t)
	srv.mux.HandleFunc("GET /api/v1/auth/discord", srv.handleDiscordLogin)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/discord?invite=test-invite-token", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusFound)
	}

	// Should set both state and invite cookies.
	cookies := rr.Result().Cookies()
	var stateCookie, inviteCookie *http.Cookie
	for _, c := range cookies {
		switch c.Name {
		case "glyphoxa_oauth_state":
			stateCookie = c
		case "glyphoxa_invite":
			inviteCookie = c
		}
	}
	if stateCookie == nil {
		t.Error("missing glyphoxa_oauth_state cookie")
	}
	if inviteCookie == nil {
		t.Fatal("missing glyphoxa_invite cookie")
	}
	if inviteCookie.Value != "test-invite-token" {
		t.Errorf("invite cookie = %q, want %q", inviteCookie.Value, "test-invite-token")
	}
}

func TestHandleMe_Unauthenticated_Explicit(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/auth/me", auth(http.HandlerFunc(srv.handleMe)))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestHandleAPIKeyLogin_InvalidJSON(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.cfg.AdminAPIKey = "some-key"
	srv.mux.HandleFunc("POST /api/v1/auth/apikey", srv.handleAPIKeyLogin)

	body := bytes.NewBufferString(`not json`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/apikey", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}
