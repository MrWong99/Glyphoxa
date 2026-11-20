// Package spa serves the built Vite + React single-page app (ADR-0013/0039)
// from an embedded filesystem. The web/ build emits its bundle into dist/ (see
// web/vite.config.ts: build.outDir → ../internal/spa/dist), and this package
// embeds that dist/ tree into the binary (via the embed directive below) so the
// single-binary deployment (ADR-0005) ships the UI with no separate static-file
// step.
//
// A committed placeholder dist/index.html keeps `go build ./...` and this embed
// compiling on a fresh checkout with NO node step (mirroring how gen/ is handled
// — but committed, not a CI artifact). A real `npm run build` overwrites
// index.html and adds the hashed assets/ locally and in CI; only the placeholder
// index.html is tracked (see .gitignore).
package spa

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

// dist is the embedded built SPA bundle. `all:` includes files the default
// embed pattern skips (e.g. dotfiles), so nothing the build emits is dropped.
//
//go:embed all:dist
var dist embed.FS

// ContentSecurityPolicy is the policy every SPA response carries (#662). It is
// written against what the built app actually loads — tighten or widen it
// together with the app, and re-check the browser console when you do:
//
//   - script-src 'self': the Vite bundle is external module scripts only; there
//     is no inline script and nothing evaluates strings.
//   - style-src 'unsafe-inline': React `style` attributes are used throughout,
//     and the toast/select/command-palette libraries inject <style> elements.
//   - img-src data: blob: + the Discord CDN: Vite inlines small assets as data:
//     URIs, upload/generation drafts preview through object URLs, and the
//     signed-in operator's avatar is served by Discord. Everything else (map
//     images, portraits, Highlight images, landing shots) is same-origin.
//   - media-src blob:: TTS previews play from an object URL; Highlight clips
//     and sounds stream same-origin.
//   - font-src 'self' data:: the fonts are self-hosted with the bundle. No
//     third-party font origin is allowed, deliberately — a font CDN sees every
//     visitor's IP address.
//   - connect-src 'self': Connect RPC and the SSE transcript tail are
//     same-origin (/api).
//   - frame-ancestors 'none' (with X-Frame-Options for older browsers): the
//     console is never legitimately framed, so clickjacking is closed outright
//     instead of leaning on SameSite=Lax cookies.
//   - object-src / base-uri / form-action: no plugins, no <base> hijack, no
//     form posting off-origin if markup is ever injected.
const ContentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob: https://cdn.discordapp.com; " +
	"media-src 'self' blob:; " +
	"font-src 'self' data:; " +
	"connect-src 'self'; " +
	"object-src 'none'; " +
	"base-uri 'self'; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'"

const (
	// cacheImmutable is for the content-hashed bundle under /assets/: Vite
	// names every file there after its content, so a URL's bytes never change
	// and a browser may keep them for a year without ever revalidating.
	cacheImmutable = "public, max-age=31536000, immutable"
	// cacheRevalidate is for the shell (index.html) and the other unhashed
	// files copied from web/public: store them, but check before every use, so
	// a deploy's new shell — which names the new hashed bundle — is picked up
	// on the next navigation. The check is a cheap 304 against the ETag below.
	cacheRevalidate = "no-cache"
)

// Handler returns an http.Handler that serves the embedded SPA with client-side
// routing support: a request for an existing embedded asset (e.g. /assets/*.js)
// is served with the correct content-type; any other path that is not an
// embedded file falls back to index.html so client-side deep links (e.g.
// /t/foo/configuration) resolve to the SPA shell. A request that LOOKS like a
// static asset but is genuinely missing (e.g. /assets/nope.js) returns 404
// rather than masking a broken bundle reference behind the HTML fallback.
//
// Every response carries the security headers (#662): [ContentSecurityPolicy],
// X-Content-Type-Options: nosniff, a Referrer-Policy and the anti-framing
// pair. Caching is explicit: the hashed /assets/ tree is immutable, everything
// else revalidates.
//
// It panics at construction if the embedded dist/ tree is malformed (the
// fs.Sub / index.html reads), so a botched embed fails the process at startup
// rather than per-request.
func Handler() http.Handler {
	// Strip the dist/ prefix so embedded paths map onto request paths
	// (dist/index.html → /index.html, dist/assets/x → /assets/x).
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic("spa: sub dist FS: " + err.Error())
	}
	return newHandler(sub)
}

// newHandler is [Handler] over an arbitrary bundle tree; tests hand it a
// synthetic one with hashed assets, which the committed placeholder lacks.
func newHandler(sub fs.FS) http.Handler {
	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		panic("spa: read embedded index.html: " + err.Error())
	}
	// An embed.FS carries no modification times, so net/http can derive neither
	// Last-Modified nor an ETag for it and a browser has NOTHING to revalidate
	// against — every "check" re-downloads the file. The shell is the one file
	// fetched on every navigation, so it gets a content-derived strong ETag;
	// http.ServeContent then answers If-None-Match with 304 by itself.
	indexSum := sha256.Sum256(index)
	indexETag := `"` + hex.EncodeToString(indexSum[:16]) + `"`

	fileServer := http.FileServerFS(sub)

	serveIndex := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", cacheRevalidate)
		w.Header().Set("ETag", indexETag)
		http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(index))
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w.Header())

		// Only GET/HEAD make sense for a static SPA; let the file server reject
		// the rest with its own 405.
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			fileServer.ServeHTTP(w, r)
			return
		}

		upath := strings.TrimPrefix(r.URL.Path, "/")
		if upath == "" || upath == "index.html" {
			serveIndex(w, r)
			return
		}

		// An existing embedded asset is served by the file server (correct
		// content-type, range support — but no validators, see newHandler). A
		// non-existent path is the SPA fallback — UNLESS it addresses the
		// assets/ tree, where a miss is a genuine 404 (a broken bundle
		// reference must not be masked as HTML).
		hashed := strings.HasPrefix(upath, "assets/")
		if assetExists(sub, upath) {
			if hashed {
				w.Header().Set("Cache-Control", cacheImmutable)
			} else {
				w.Header().Set("Cache-Control", cacheRevalidate)
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		if hashed {
			http.NotFound(w, r)
			return
		}

		// SPA fallback: serve index.html for client-side routes.
		serveIndex(w, r)
	})
}

// setSecurityHeaders stamps the headers every SPA response carries (#662).
func setSecurityHeaders(h http.Header) {
	h.Set("Content-Security-Policy", ContentSecurityPolicy)
	// Never let a browser second-guess a declared content type.
	h.Set("X-Content-Type-Options", "nosniff")
	// Campaign and session ids live in the SPA's URLs; do not hand the full
	// path to the external sites the app links to (Discord, provider consoles).
	h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	// The pre-CSP spelling of frame-ancestors 'none'.
	h.Set("X-Frame-Options", "DENY")
}

// assetExists reports whether name resolves to a regular file in the embedded
// SPA FS. Directories are not assets (the file server would 301 to a listing),
// so they fall through to the SPA fallback.
func assetExists(fsys fs.FS, name string) bool {
	f, err := fsys.Open(name)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return !info.IsDir()
}
