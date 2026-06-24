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
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// dist is the embedded built SPA bundle. `all:` includes files the default
// embed pattern skips (e.g. dotfiles), so nothing the build emits is dropped.
//
//go:embed all:dist
var dist embed.FS

// Handler returns an http.Handler that serves the embedded SPA with client-side
// routing support: a request for an existing embedded asset (e.g. /assets/*.js)
// is served with the correct content-type; any other path that is not an
// embedded file falls back to index.html so client-side deep links (e.g.
// /t/foo/configuration) resolve to the SPA shell. A request that LOOKS like a
// static asset but is genuinely missing (e.g. /assets/nope.js) returns 404
// rather than masking a broken bundle reference behind the HTML fallback.
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

	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		panic("spa: read embedded index.html: " + err.Error())
	}

	fileServer := http.FileServerFS(sub)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only GET/HEAD make sense for a static SPA; let the file server reject
		// the rest with its own 405.
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			fileServer.ServeHTTP(w, r)
			return
		}

		upath := strings.TrimPrefix(r.URL.Path, "/")
		if upath == "" {
			upath = "index.html"
		}

		// An existing embedded asset is served by the file server (correct
		// content-type, range support, ETag). A non-existent path is the SPA
		// fallback — UNLESS it addresses the assets/ tree, where a miss is a
		// genuine 404 (a broken bundle reference must not be masked as HTML).
		if assetExists(sub, upath) {
			fileServer.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(upath, "assets/") {
			http.NotFound(w, r)
			return
		}

		// SPA fallback: serve index.html for client-side routes.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
	})
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
