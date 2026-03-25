package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleGoogleLogin_NotConfigured(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("GET /api/v1/auth/google", srv.handleGoogleLogin)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/google", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d (Google not configured)", rr.Code, http.StatusNotFound)
	}
}

func TestHandleGoogleLogin_Configured(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.cfg.GoogleClientID = "google-test-id"
	srv.cfg.GoogleClientSecret = "google-test-secret"
	srv.cfg.GoogleRedirectURI = "http://localhost/callback/google"
	srv.mux.HandleFunc("GET /api/v1/auth/google", srv.handleGoogleLogin)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/google", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusFound)
	}

	loc := rr.Header().Get("Location")
	if loc == "" {
		t.Fatal("missing Location header")
	}
	if want := "accounts.google.com"; !contains(loc, want) {
		t.Errorf("Location = %q, should contain %q", loc, want)
	}
	if want := "client_id=google-test-id"; !contains(loc, want) {
		t.Errorf("Location = %q, should contain %q", loc, want)
	}

	// Should set state cookie.
	var stateCookie *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == "glyphoxa_oauth_state" {
			stateCookie = c
			break
		}
	}
	if stateCookie == nil {
		t.Fatal("missing glyphoxa_oauth_state cookie")
	}
	if stateCookie.Value == "" {
		t.Fatal("empty state cookie")
	}
}

func TestHandleGitHubLogin_NotConfigured(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("GET /api/v1/auth/github", srv.handleGitHubLogin)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/github", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d (GitHub not configured)", rr.Code, http.StatusNotFound)
	}
}

func TestHandleGitHubLogin_Configured(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.cfg.GitHubClientID = "github-test-id"
	srv.cfg.GitHubClientSecret = "github-test-secret"
	srv.cfg.GitHubRedirectURI = "http://localhost/callback/github"
	srv.mux.HandleFunc("GET /api/v1/auth/github", srv.handleGitHubLogin)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/github", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusFound)
	}

	loc := rr.Header().Get("Location")
	if loc == "" {
		t.Fatal("missing Location header")
	}
	if want := "github.com/login/oauth/authorize"; !contains(loc, want) {
		t.Errorf("Location = %q, should contain %q", loc, want)
	}
	if want := "client_id=github-test-id"; !contains(loc, want) {
		t.Errorf("Location = %q, should contain %q", loc, want)
	}
}

func TestHandleGoogleCallback_MissingState(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.cfg.GoogleClientID = "google-test-id"
	srv.cfg.GoogleClientSecret = "google-test-secret"
	srv.cfg.GoogleRedirectURI = "http://localhost/callback/google"
	srv.mux.HandleFunc("GET /api/v1/auth/google/callback", srv.handleGoogleCallback)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/google/callback?code=test-code&state=abc", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (missing state cookie)", rr.Code, http.StatusBadRequest)
	}
}

func TestHandleGitHubCallback_MissingState(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.cfg.GitHubClientID = "github-test-id"
	srv.cfg.GitHubClientSecret = "github-test-secret"
	srv.cfg.GitHubRedirectURI = "http://localhost/callback/github"
	srv.mux.HandleFunc("GET /api/v1/auth/github/callback", srv.handleGitHubCallback)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/github/callback?code=test-code&state=abc", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (missing state cookie)", rr.Code, http.StatusBadRequest)
	}
}

func TestProcessInvite_NilInvite(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	user := &User{ID: "u1", TenantID: "t1", Role: "dm"}

	// Should not panic with nil invite.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	srv.processInvite(req, user, nil)

	if user.TenantID != "t1" {
		t.Errorf("tenant should be unchanged, got %q", user.TenantID)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsImpl(s, substr))
}

func containsImpl(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
