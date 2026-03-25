package web

import (
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// RateLimiter provides in-memory per-key rate limiting using a token bucket.
// Safe for concurrent use.
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
	rate    int           // tokens added per interval
	burst   int           // max tokens
	window  time.Duration // refill interval
}

type tokenBucket struct {
	tokens    float64
	lastCheck time.Time
}

// NewRateLimiter creates a rate limiter that allows rate requests per window
// with a burst capacity of burst.
func NewRateLimiter(rate, burst int, window time.Duration) *RateLimiter {
	rl := &RateLimiter{
		buckets: make(map[string]*tokenBucket),
		rate:    rate,
		burst:   burst,
		window:  window,
	}
	// Start background cleanup goroutine.
	go rl.cleanup()
	return rl
}

// Allow checks if a request with the given key is allowed.
// Returns true if the request is within limits.
func (rl *RateLimiter) Allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, ok := rl.buckets[key]
	if !ok {
		b = &tokenBucket{
			tokens:    float64(rl.burst) - 1,
			lastCheck: now,
		}
		rl.buckets[key] = b
		return true
	}

	// Refill tokens based on elapsed time.
	elapsed := now.Sub(b.lastCheck)
	tokensToAdd := elapsed.Seconds() / rl.window.Seconds() * float64(rl.rate)
	b.tokens += tokensToAdd
	if b.tokens > float64(rl.burst) {
		b.tokens = float64(rl.burst)
	}
	b.lastCheck = now

	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// Remaining returns the number of tokens remaining for the key.
func (rl *RateLimiter) Remaining(key string) int {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	b, ok := rl.buckets[key]
	if !ok {
		return rl.burst
	}

	now := time.Now()
	elapsed := now.Sub(b.lastCheck)
	tokensToAdd := elapsed.Seconds() / rl.window.Seconds() * float64(rl.rate)
	tokens := b.tokens + tokensToAdd
	if tokens > float64(rl.burst) {
		tokens = float64(rl.burst)
	}
	return int(tokens)
}

func (rl *RateLimiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		rl.mu.Lock()
		cutoff := time.Now().Add(-10 * time.Minute)
		for key, b := range rl.buckets {
			if b.lastCheck.Before(cutoff) {
				delete(rl.buckets, key)
			}
		}
		rl.mu.Unlock()
	}
}

// rateLimitTiers defines rate limits per role tier.
var rateLimitTiers = map[string]struct{ read, write int }{
	"viewer":       {60, 30},
	"dm":           {60, 30},
	"tenant_admin": {120, 60},
	"super_admin":  {300, 120},
}

// RateLimitMiddleware applies per-user rate limiting based on JWT claims.
// Uses the user ID as the rate limit key for authenticated requests,
// and the client IP for unauthenticated requests.
func RateLimitMiddleware(readLimiter, writeLimiter *RateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var key string
			var limiter *RateLimiter

			claims := ClaimsFromContext(r.Context())
			if claims != nil {
				key = "user:" + claims.Sub
			} else {
				key = "ip:" + clientIP(r)
			}

			// Select limiter based on method.
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				limiter = readLimiter
			default:
				limiter = writeLimiter
			}

			if !limiter.Allow(key) {
				remaining := limiter.Remaining(key)
				w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
				w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10))
				w.Header().Set("Retry-After", "60")
				writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
				return
			}

			remaining := limiter.Remaining(key)
			w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))

			next.ServeHTTP(w, r)
		})
	}
}

// clientIP extracts the client IP from the request, respecting X-Forwarded-For.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Take the first IP in the chain.
		if i := 0; i < len(xff) {
			for j, c := range xff {
				if c == ',' {
					return xff[:j]
				}
				_ = j
			}
			return xff
		}
	}
	if xff := r.Header.Get("X-Real-IP"); xff != "" {
		return xff
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
