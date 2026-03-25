//go:build integration

package health_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MrWong99/glyphoxa/internal/health"
)

// TestIntegration_HealthEndpoints tests the /healthz and /readyz endpoints
// with various checker configurations.
func TestIntegration_HealthEndpoints(t *testing.T) {
	t.Parallel()

	t.Run("healthz always returns 200", func(t *testing.T) {
		t.Parallel()

		h := health.New() // No checkers.
		mux := http.NewServeMux()
		h.Register(mux)

		req := httptest.NewRequest("GET", "/healthz", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rec.Code)
		}

		var result map[string]any
		if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if result["status"] != "ok" {
			t.Errorf("status = %v, want ok", result["status"])
		}
	})

	t.Run("readyz with all healthy checkers returns 200", func(t *testing.T) {
		t.Parallel()

		h := health.New(
			health.Checker{
				Name: "database",
				Check: func(ctx context.Context) error {
					return nil // Healthy.
				},
			},
			health.Checker{
				Name: "cache",
				Check: func(ctx context.Context) error {
					return nil // Healthy.
				},
			},
		)
		mux := http.NewServeMux()
		h.Register(mux)

		req := httptest.NewRequest("GET", "/readyz", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rec.Code)
		}

		var result map[string]any
		if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if result["status"] != "ok" {
			t.Errorf("status = %v, want ok", result["status"])
		}

		checks, ok := result["checks"].(map[string]any)
		if !ok {
			t.Fatal("missing checks in response")
		}
		if checks["database"] != "ok" {
			t.Errorf("database = %v, want ok", checks["database"])
		}
		if checks["cache"] != "ok" {
			t.Errorf("cache = %v, want ok", checks["cache"])
		}
	})

	t.Run("readyz with failing checker returns 503", func(t *testing.T) {
		t.Parallel()

		h := health.New(
			health.Checker{
				Name: "database",
				Check: func(ctx context.Context) error {
					return nil
				},
			},
			health.Checker{
				Name: "redis",
				Check: func(ctx context.Context) error {
					return errors.New("connection refused")
				},
			},
		)
		mux := http.NewServeMux()
		h.Register(mux)

		req := httptest.NewRequest("GET", "/readyz", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503", rec.Code)
		}

		var result map[string]any
		if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if result["status"] != "fail" {
			t.Errorf("status = %v, want fail", result["status"])
		}

		checks, ok := result["checks"].(map[string]any)
		if !ok {
			t.Fatal("missing checks")
		}
		if checks["database"] != "ok" {
			t.Errorf("database = %v, want ok", checks["database"])
		}

		redis, ok := checks["redis"].(string)
		if !ok {
			t.Fatal("redis check not a string")
		}
		if redis != "fail: connection refused" {
			t.Errorf("redis = %q, want 'fail: connection refused'", redis)
		}
	})

	t.Run("readyz with no checkers returns 200", func(t *testing.T) {
		t.Parallel()

		h := health.New()
		mux := http.NewServeMux()
		h.Register(mux)

		req := httptest.NewRequest("GET", "/readyz", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rec.Code)
		}
	})

	t.Run("readyz checks respect context cancellation", func(t *testing.T) {
		t.Parallel()

		h := health.New(
			health.Checker{
				Name: "slow",
				Check: func(ctx context.Context) error {
					<-ctx.Done()
					return ctx.Err()
				},
			},
		)
		mux := http.NewServeMux()
		h.Register(mux)

		// The handler uses a 5s timeout. We use a request with a shorter
		// context to verify the checker respects cancellation.
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately.

		req := httptest.NewRequest("GET", "/readyz", nil).WithContext(ctx)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		// The checker will fail because the context was cancelled.
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503", rec.Code)
		}
	})

	t.Run("response has correct content type", func(t *testing.T) {
		t.Parallel()

		h := health.New()
		mux := http.NewServeMux()
		h.Register(mux)

		req := httptest.NewRequest("GET", "/healthz", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		ct := rec.Header().Get("Content-Type")
		if ct != "application/json; charset=utf-8" {
			t.Errorf("Content-Type = %q, want application/json; charset=utf-8", ct)
		}
	})
}

// TestIntegration_HealthWithRealDatabaseChecker simulates what happens in
// production: the health handler includes a database ping checker.
func TestIntegration_HealthWithRealDatabaseChecker(t *testing.T) {
	t.Parallel()

	// This test simulates a database checker using a mock function.
	// The real database integration is covered in the web integration test.
	dbHealthy := true

	h := health.New(
		health.Checker{
			Name: "database",
			Check: func(ctx context.Context) error {
				if !dbHealthy {
					return errors.New("database unreachable")
				}
				return nil
			},
		},
		health.Checker{
			Name: "providers",
			Check: func(ctx context.Context) error {
				return nil
			},
		},
	)
	mux := http.NewServeMux()
	h.Register(mux)

	t.Run("all healthy", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest("GET", "/readyz", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rec.Code)
		}
	})
}
