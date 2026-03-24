package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAuthMiddleware_ValidToken(t *testing.T) {
	t.Parallel()

	secret := "test-secret"
	token, err := SignJWT(secret, Claims{
		Sub:      "user-1",
		TenantID: "tenant-1",
		Role:     "dm",
		Expires:  time.Now().Add(1 * time.Hour).Unix(),
	})
	if err != nil {
		t.Fatalf("SignJWT: %v", err)
	}

	var gotClaims *Claims
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotClaims = ClaimsFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	handler := AuthMiddleware(secret)(inner)
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if gotClaims == nil {
		t.Fatal("claims not set in context")
	}
	if gotClaims.Sub != "user-1" {
		t.Errorf("Sub = %q, want %q", gotClaims.Sub, "user-1")
	}
}

func TestAuthMiddleware_MissingHeader(t *testing.T) {
	t.Parallel()

	handler := AuthMiddleware("secret")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("inner handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestAuthMiddleware_InvalidToken(t *testing.T) {
	t.Parallel()

	handler := AuthMiddleware("secret")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("inner handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer invalid.token.here")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestRequireRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		userRole string
		minRole  string
		wantCode int
	}{
		{"super_admin passes super_admin", "super_admin", "super_admin", http.StatusOK},
		{"super_admin passes dm", "super_admin", "dm", http.StatusOK},
		{"dm passes dm", "dm", "dm", http.StatusOK},
		{"dm passes viewer", "dm", "viewer", http.StatusOK},
		{"viewer fails dm", "viewer", "dm", http.StatusForbidden},
		{"viewer fails super_admin", "viewer", "super_admin", http.StatusForbidden},
		{"dm fails tenant_admin", "dm", "tenant_admin", http.StatusForbidden},
		{"tenant_admin passes dm", "tenant_admin", "dm", http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			secret := "test-secret"
			token, err := SignJWT(secret, Claims{
				Sub:      "user-1",
				TenantID: "tenant-1",
				Role:     tt.userRole,
				Expires:  time.Now().Add(1 * time.Hour).Unix(),
			})
			if err != nil {
				t.Fatalf("SignJWT: %v", err)
			}

			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})

			handler := AuthMiddleware(secret)(RequireRole(tt.minRole)(inner))
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if rr.Code != tt.wantCode {
				t.Errorf("status = %d, want %d", rr.Code, tt.wantCode)
			}
		})
	}
}

func TestCORSMiddleware_Preflight(t *testing.T) {
	t.Parallel()

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("inner handler should not be called for OPTIONS")
	})

	handler := CORSMiddleware(nil)(inner) // nil = allow all
	req := httptest.NewRequest(http.MethodOptions, "/test", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNoContent)
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("CORS origin = %q, want %q", got, "*")
	}
}

func TestCORSMiddleware_RegularRequest(t *testing.T) {
	t.Parallel()

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := CORSMiddleware(nil)(inner) // nil = allow all
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("CORS origin = %q, want %q", got, "*")
	}
}

func TestCORSMiddleware_RestrictedOrigin(t *testing.T) {
	t.Parallel()

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := CORSMiddleware([]string{"https://app.example.com"})(inner)

	// Allowed origin.
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("CORS origin = %q, want %q", got, "https://app.example.com")
	}
	if got := rr.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("Allow-Credentials = %q, want %q", got, "true")
	}

	// Disallowed origin.
	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req2.Header.Set("Origin", "https://evil.example.com")
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req2)

	if got := rr2.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("CORS origin for disallowed = %q, want empty", got)
	}
}
