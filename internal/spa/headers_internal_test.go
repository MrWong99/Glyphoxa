package spa

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// builtBundle is a synthetic Vite build: the shell, one content-hashed chunk
// under assets/, and one unhashed file copied from web/public. The committed
// placeholder dist/ carries only index.html, so the caching split cannot be
// exercised against the real embed on a node-free checkout.
func builtBundle() fstest.MapFS {
	return fstest.MapFS{
		"index.html":               {Data: []byte(`<!doctype html><html><body><div id="root"></div><script type="module" src="/assets/index-C0ffee12.js"></script></body></html>`)},
		"assets/index-C0ffee12.js": {Data: []byte(`console.log("glyphoxa")`)},
		"favicon.png":              {Data: []byte("\x89PNG\r\n\x1a\n")},
	}
}

func serve(t *testing.T, h http.Handler, method, path string, header http.Header) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for k, vs := range header {
		req.Header[k] = vs
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

// TestSecurityHeadersOnEveryResponse is the #662 regression: the shell, a deep
// link, a hashed asset, an unhashed file and a 404 all carry the CSP, nosniff,
// a Referrer-Policy and the anti-framing pair. Before the fix the SPA sent
// none of them.
func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	h := newHandler(builtBundle())
	for _, path := range []string{"/", "/t/foo/configuration", "/assets/index-C0ffee12.js", "/favicon.png", "/assets/nope.js"} {
		resp := serve(t, h, http.MethodGet, path, nil)
		for name, want := range map[string]string{
			"Content-Security-Policy": ContentSecurityPolicy,
			"X-Content-Type-Options":  "nosniff",
			"Referrer-Policy":         "strict-origin-when-cross-origin",
			"X-Frame-Options":         "DENY",
		} {
			if got := resp.Header.Get(name); got != want {
				t.Errorf("GET %s: %s = %q, want %q", path, name, got, want)
			}
		}
	}
}

// TestContentSecurityPolicyShape pins the directives that carry the policy's
// weight, so a well-meant loosening has to be made on purpose: scripts are
// same-origin only (no inline, no eval), the page cannot be framed, and no
// third-party font origin is allowed (the fonts are self-hosted).
func TestContentSecurityPolicyShape(t *testing.T) {
	directives := map[string]string{}
	for _, d := range strings.Split(ContentSecurityPolicy, ";") {
		name, value, _ := strings.Cut(strings.TrimSpace(d), " ")
		directives[name] = value
	}
	for name, want := range map[string]string{
		"default-src":     "'self'",
		"script-src":      "'self'",
		"connect-src":     "'self'",
		"object-src":      "'none'",
		"frame-ancestors": "'none'",
		"base-uri":        "'self'",
	} {
		if got := directives[name]; got != want {
			t.Errorf("CSP %s = %q, want %q", name, got, want)
		}
	}
	for _, forbidden := range []string{"fonts.googleapis.com", "fonts.gstatic.com", "'unsafe-eval'", "*"} {
		if strings.Contains(ContentSecurityPolicy, forbidden) {
			t.Errorf("CSP must not contain %q: %s", forbidden, ContentSecurityPolicy)
		}
	}
	// The app needs these to render at all (see the ContentSecurityPolicy doc).
	for name, needs := range map[string][]string{
		"style-src": {"'self'", "'unsafe-inline'"},
		"img-src":   {"'self'", "data:", "blob:"},
		"media-src": {"'self'", "blob:"},
		"font-src":  {"'self'"},
	} {
		for _, n := range needs {
			if !strings.Contains(" "+directives[name]+" ", " "+n+" ") {
				t.Errorf("CSP %s = %q, missing %s — the built app would break", name, directives[name], n)
			}
		}
	}
}

// TestCacheControl pins the caching split (#662): a content-hashed file under
// /assets/ never changes at its URL and is immutable for a year; the shell —
// at /, at /index.html and on every client-side deep link — and the unhashed
// public files must revalidate, or a deploy would keep serving a stale shell
// that names a bundle the new binary no longer embeds.
func TestCacheControl(t *testing.T) {
	h := newHandler(builtBundle())
	for path, want := range map[string]string{
		"/assets/index-C0ffee12.js": "public, max-age=31536000, immutable",
		"/":                         "no-cache",
		"/index.html":               "no-cache",
		"/t/foo/configuration":      "no-cache",
		"/favicon.png":              "no-cache",
	} {
		resp := serve(t, h, http.MethodGet, path, nil)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: status = %d, want 200", path, resp.StatusCode)
		}
		if got := resp.Header.Get("Cache-Control"); got != want {
			t.Errorf("GET %s: Cache-Control = %q, want %q", path, got, want)
		}
	}
	// A missing asset is a 404 that must not be cached as immutable.
	resp := serve(t, h, http.MethodGet, "/assets/nope.js", nil)
	if cc := resp.Header.Get("Cache-Control"); strings.Contains(cc, "immutable") {
		t.Errorf("404 under /assets/ carries Cache-Control %q", cc)
	}
}

// TestShellRevalidatesWith304 proves `no-cache` on the shell is cheap: the
// shell carries a content-derived ETag (an embed.FS has no mtimes, so net/http
// supplies no validator of its own) and a matching If-None-Match is answered
// 304 with no body. A different bundle yields a different ETag.
func TestShellRevalidatesWith304(t *testing.T) {
	h := newHandler(builtBundle())
	first := serve(t, h, http.MethodGet, "/", nil)
	etag := first.Header.Get("ETag")
	if etag == "" {
		t.Fatal("shell response carries no ETag — a browser cannot revalidate it")
	}
	if deep := serve(t, h, http.MethodGet, "/t/foo", nil).Header.Get("ETag"); deep != etag {
		t.Errorf("deep-link shell ETag = %q, want the same %q", deep, etag)
	}

	again := serve(t, h, http.MethodGet, "/", http.Header{"If-None-Match": {etag}})
	if again.StatusCode != http.StatusNotModified {
		t.Fatalf("If-None-Match %s: status = %d, want 304", etag, again.StatusCode)
	}
	if again.ContentLength > 0 {
		t.Errorf("304 carried a %d-byte body", again.ContentLength)
	}

	other := builtBundle()
	other["index.html"] = &fstest.MapFile{Data: []byte(`<!doctype html><html><body><div id="root"></div><script type="module" src="/assets/index-Deadbeef.js"></script></body></html>`)}
	if got := serve(t, newHandler(other), http.MethodGet, "/", nil).Header.Get("ETag"); got == etag {
		t.Error("a different shell produced the same ETag")
	}
}
