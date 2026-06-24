package spa_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/goleak"

	"github.com/MrWong99/Glyphoxa/internal/spa"
)

// TestMain runs goleak to match the repo style (internal/web/server_test.go);
// these tests use httptest.Server, so a leaked goroutine from a botched handler
// fails the package rather than hanging around (ADR-0033 keyless gate).
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// newServer spins the SPA handler on an httptest.Server and returns its base URL
// plus a GET helper that returns status + body. The handler serves the EMBEDDED
// bundle — the committed placeholder dist/index.html on a node-free checkout, or
// the real Vite bundle after `npm run build` — so the test is keyless and needs
// no node step.
func newServer(t *testing.T) (string, func()) {
	t.Helper()
	srv := httptest.NewServer(spa.Handler())
	return srv.URL, srv.Close
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, string(body)
}

// TestServesIndexAtRoot is the issue-#66 acceptance: embed.FS serves index.html
// at / with the SPA mount point (#66: "A Go test asserts embed.FS serves
// index.html at /").
func TestServesIndexAtRoot(t *testing.T) {
	base, stop := newServer(t)
	defer stop()

	status, body := get(t, base+"/")
	if status != http.StatusOK {
		t.Fatalf("GET /: status = %d, want 200", status)
	}
	if !strings.Contains(body, `<div id="root">`) {
		t.Errorf("GET / body missing SPA mount point %q; got:\n%s", `<div id="root">`, body)
	}
}

// TestDeepLinkFallsBackToIndex proves the SPA fallback: a client-side deep link
// that is not an embedded file returns index.html (200) so the browser router
// can resolve it, instead of a 404.
func TestDeepLinkFallsBackToIndex(t *testing.T) {
	base, stop := newServer(t)
	defer stop()

	status, body := get(t, base+"/t/foo/configuration")
	if status != http.StatusOK {
		t.Fatalf("deep link: status = %d, want 200", status)
	}
	if !strings.Contains(body, `<div id="root">`) {
		t.Errorf("deep link did not fall back to index.html; got:\n%s", body)
	}
}

// TestMissingAssetIs404 proves a genuinely-missing asset path 404s rather than
// being masked by the HTML fallback — a broken bundle reference must surface.
func TestMissingAssetIs404(t *testing.T) {
	base, stop := newServer(t)
	defer stop()

	status, _ := get(t, base+"/assets/nope.js")
	if status != http.StatusNotFound {
		t.Fatalf("GET /assets/nope.js: status = %d, want 404", status)
	}
}
