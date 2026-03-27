package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRateLimiter_Allow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		rate      int
		burst     int
		requests  int
		wantAllow int // how many should be allowed
	}{
		{
			name:      "all within burst",
			rate:      10,
			burst:     10,
			requests:  5,
			wantAllow: 5,
		},
		{
			name:      "exceeds burst",
			rate:      3,
			burst:     3,
			requests:  5,
			wantAllow: 3,
		},
		{
			name:      "single request always allowed",
			rate:      1,
			burst:     1,
			requests:  1,
			wantAllow: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rl := NewRateLimiter(tt.rate, tt.burst, time.Minute)
			var allowed int
			for i := 0; i < tt.requests; i++ {
				if rl.Allow("test-key") {
					allowed++
				}
			}

			if allowed != tt.wantAllow {
				t.Errorf("allowed = %d, want %d", allowed, tt.wantAllow)
			}
		})
	}
}

func TestRateLimiter_DifferentKeys(t *testing.T) {
	t.Parallel()

	rl := NewRateLimiter(2, 2, time.Minute)

	// Key A should get its own bucket.
	if !rl.Allow("a") {
		t.Error("key a first request should be allowed")
	}
	if !rl.Allow("a") {
		t.Error("key a second request should be allowed")
	}
	if rl.Allow("a") {
		t.Error("key a third request should be denied")
	}

	// Key B has a separate bucket.
	if !rl.Allow("b") {
		t.Error("key b first request should be allowed")
	}
}

func TestRateLimiter_Remaining(t *testing.T) {
	t.Parallel()

	rl := NewRateLimiter(5, 5, time.Minute)

	rem := rl.Remaining("new-key")
	if rem != 5 {
		t.Errorf("remaining for new key = %d, want 5", rem)
	}

	rl.Allow("new-key")
	rem = rl.Remaining("new-key")
	if rem != 4 {
		t.Errorf("remaining after 1 request = %d, want 4", rem)
	}
}

func TestRateLimitMiddleware_Blocks(t *testing.T) {
	t.Parallel()

	readRL := NewRateLimiter(2, 2, time.Minute)
	writeRL := NewRateLimiter(1, 1, time.Minute)

	handler := RateLimitMiddleware(readRL, writeRL)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// First 2 GET requests should pass.
	for i := 0; i < 2; i++ {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.RemoteAddr = "192.168.1.1:1234"
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("request %d: status = %d, want %d", i, rr.Code, http.StatusOK)
		}
	}

	// Third GET should be rate limited.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "192.168.1.1:1234"
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("third request: status = %d, want %d", rr.Code, http.StatusTooManyRequests)
	}
}

func TestClientIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		xff    string
		xri    string
		remote string
		wantIP string
	}{
		{
			name:   "remote addr",
			remote: "10.0.0.1:8080",
			wantIP: "10.0.0.1",
		},
		{
			name:   "xff ignored for rate limiting",
			xff:    "203.0.113.50, 70.41.3.18",
			remote: "10.0.0.1:8080",
			wantIP: "10.0.0.1", // uses RemoteAddr, not XFF
		},
		{
			name:   "x-real-ip ignored for rate limiting",
			xri:    "203.0.113.100",
			remote: "10.0.0.1:8080",
			wantIP: "10.0.0.1", // uses RemoteAddr, not X-Real-IP
		},
		{
			name:   "remote addr without port",
			remote: "10.0.0.1",
			wantIP: "10.0.0.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tt.remote
			if tt.xff != "" {
				req.Header.Set("X-Forwarded-For", tt.xff)
			}
			if tt.xri != "" {
				req.Header.Set("X-Real-IP", tt.xri)
			}

			got := clientIP(req)
			if got != tt.wantIP {
				t.Errorf("clientIP = %q, want %q", got, tt.wantIP)
			}
		})
	}
}
