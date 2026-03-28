package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleTestProvider_SSRFBlocked(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	srv.mux.Handle("POST /api/v1/providers/test", AuthMiddleware(secret)(http.HandlerFunc(srv.handleTestProvider)))

	tests := []struct {
		name    string
		baseURL string
		wantOK  bool
	}{
		{
			name:    "private 10.x blocked",
			baseURL: "http://10.0.0.1",
			wantOK:  false,
		},
		{
			name:    "private 192.168.x blocked",
			baseURL: "http://192.168.1.1",
			wantOK:  false,
		},
		{
			name:    "loopback blocked",
			baseURL: "http://127.0.0.1",
			wantOK:  false,
		},
		{
			name:    "metadata endpoint blocked",
			baseURL: "http://169.254.169.254",
			wantOK:  false,
		},
		{
			name:    "localhost hostname blocked",
			baseURL: "http://localhost",
			wantOK:  false,
		},
		{
			name:    "kubernetes service DNS blocked",
			baseURL: "https://vault.default.svc.cluster.local",
			wantOK:  false,
		},
		{
			name:    "file scheme blocked",
			baseURL: "file:///etc/passwd",
			wantOK:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body, _ := json.Marshal(map[string]string{
				"type":     "llm",
				"provider": "openai",
				"api_key":  "sk-test",
				"base_url": tt.baseURL,
			})
			req := authReq(t, http.MethodPost, "/api/v1/providers/test",
				bytes.NewBuffer(body), secret, "user-1", "tenant-1", "tenant_admin")
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			srv.mux.ServeHTTP(rr, req)

			if tt.wantOK {
				if rr.Code == http.StatusBadRequest {
					t.Errorf("expected request to be allowed, got 400: %s", rr.Body.String())
				}
			} else {
				if rr.Code != http.StatusBadRequest {
					t.Errorf("expected 400 for SSRF attempt, got %d: %s", rr.Code, rr.Body.String())
				}
				// Verify error response structure.
				var resp struct {
					Error struct {
						Code    string `json:"code"`
						Message string `json:"message"`
					} `json:"error"`
				}
				if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				if resp.Error.Code != "invalid_base_url" {
					t.Errorf("error code = %q, want %q", resp.Error.Code, "invalid_base_url")
				}
			}
		})
	}
}

func TestHandleTestProvider_EmptyBaseURLAllowed(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	srv.mux.Handle("POST /api/v1/providers/test", AuthMiddleware(secret)(http.HandlerFunc(srv.handleTestProvider)))

	// An empty base_url should pass validation (uses provider defaults).
	// The request will fail at the provider connection stage since we have
	// no real API key, but it should NOT fail with "invalid_base_url".
	body, _ := json.Marshal(map[string]string{
		"type":     "llm",
		"provider": "openai",
		"api_key":  "sk-test",
	})
	req := authReq(t, http.MethodPost, "/api/v1/providers/test",
		bytes.NewBuffer(body), secret, "user-1", "tenant-1", "tenant_admin")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	// Should not be a 400 with "invalid_base_url".
	if rr.Code == http.StatusBadRequest {
		var resp struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.NewDecoder(rr.Body).Decode(&resp)
		if resp.Error.Code == "invalid_base_url" {
			t.Error("empty base_url should not trigger SSRF validation")
		}
	}
}

func TestHandleTestProvider_MissingFields(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	srv.mux.Handle("POST /api/v1/providers/test", AuthMiddleware(secret)(http.HandlerFunc(srv.handleTestProvider)))

	body, _ := json.Marshal(map[string]string{
		"api_key": "sk-test",
	})
	req := authReq(t, http.MethodPost, "/api/v1/providers/test",
		bytes.NewBuffer(body), secret, "user-1", "tenant-1", "tenant_admin")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}
