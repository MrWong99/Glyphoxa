package spa

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

// TestRobotsTxtIsServedAsText pins that /robots.txt reaches a crawler as the
// plain-text file web/public ships, not as the SPA shell. Vite copies
// web/public verbatim into the bundle, so the file sits beside index.html in
// dist/; without it the SPA fallback would answer /robots.txt with HTML and a
// 200 — which a crawler reads as "no rules", and the app, the OAuth endpoints
// and the API would all be fair game.
//
// The bundle here is the synthetic one plus the REAL web/public/robots.txt, so
// the rules below are the ones that ship.
func TestRobotsTxtIsServedAsText(t *testing.T) {
	robots, err := os.ReadFile("../../web/public/robots.txt")
	if err != nil {
		t.Fatalf("read web/public/robots.txt: %v", err)
	}
	bundle := builtBundle()
	bundle["robots.txt"] = &fstest.MapFile{Data: robots}

	resp := serve(t, newHandler(bundle), http.MethodGet, "/robots.txt", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /robots.txt: status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("GET /robots.txt: Content-Type = %q, want text/plain", ct)
	}
	// An unhashed public file: revalidated, so a changed rule takes effect on
	// the next deploy.
	if cc := resp.Header.Get("Cache-Control"); cc != cacheRevalidate {
		t.Errorf("GET /robots.txt: Cache-Control = %q, want %q", cc, cacheRevalidate)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	got := string(body)
	if strings.Contains(got, "<div id=\"root\">") {
		t.Fatal("GET /robots.txt answered with the SPA shell")
	}
	// The paths a crawler must stay out of: the app, the OAuth carve-out, the
	// onboarding step and the API namespace (internal/web mounts Connect, the
	// SSE stream and the media endpoints under /api/).
	for _, rule := range []string{"User-agent: *", "Disallow: /t/", "Disallow: /auth/", "Disallow: /onboarding/", "Disallow: /api/"} {
		if !strings.Contains(got, rule) {
			t.Errorf("robots.txt lacks %q", rule)
		}
	}
}
