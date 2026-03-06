package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MrWong99/glyphoxa/internal/health"
)

func TestDiagnosticsEndpoints(t *testing.T) {
	t.Parallel()

	// Build a mux identical to what main.go wires — health + metrics.
	mux := http.NewServeMux()
	h := health.New() // no checkers → always ready
	h.Register(mux)

	t.Run("healthz returns 200", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("GET /healthz status = %d, want 200", rec.Code)
		}
		var body map[string]any
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if body["status"] != "ok" {
			t.Errorf("status = %v, want ok", body["status"])
		}
	})

	t.Run("readyz returns 200 with no checkers", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("GET /readyz status = %d, want 200", rec.Code)
		}
	})

	t.Run("readyz returns 503 with failing checker", func(t *testing.T) {
		t.Parallel()
		failMux := http.NewServeMux()
		fh := health.New(health.Checker{
			Name:  "fail",
			Check: func(_ context.Context) error { return fmt.Errorf("broken") },
		})
		fh.Register(failMux)

		req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		rec := httptest.NewRecorder()
		failMux.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("GET /readyz status = %d, want 503", rec.Code)
		}
	})
}
