package spa

import (
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"
)

// srcHrefRe matches src=/href= attribute values in dist/index.html, quoted with
// either quote style or unquoted — an html-minify pass (removeAttributeQuotes)
// would otherwise emit src=/assets/index-abc.js and slip past a quote-only
// pattern, silently retiring the check below. A regexp rather than an HTML
// parser is deliberate: the input is machine-generated Vite output, and
// golang.org/x/net (the only reasonable parser) is in neither go.mod nor
// go.sum — a whole new module dependency to read two tags is a bad trade.
//
// Accepted residual: the unquoted alternative lets a match start inside another
// attribute's value, so prose containing a literal "src=" (say in a <meta
// content=…>) yields a spurious ref. That errs toward a red test a human then
// reads — the safe direction here — and only real HTML parsing would fix it.
var srcHrefRe = regexp.MustCompile(`(?i)(?:src|href)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+))`)

// schemeRe matches any URL scheme prefix (https:, data:, mailto:, tel:, blob:).
// Matching the grammar rather than listing schemes keeps a new one from being
// mistaken for an embedded path.
var schemeRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)

// TestEmbeddedIndexReferencesResolve is the fresh-clone gate for the embedded
// SPA bundle: every same-origin src/href in dist/index.html must resolve to a
// real file inside the embed.FS.
//
// Why this exists: .gitignore tracks ONLY dist/index.html and never
// dist/assets/**, so the tracked index.html must be the placeholder that
// references no build output. When a real `npm run build` result is committed
// over it (this has reached main twice — 155772c, then 6d6089e), a checkout
// with no node step embeds an index.html pointing at hashed assets that do not
// exist, and the binary silently serves a BLANK PAGE. Nothing else in the suite
// notices: the existing handler tests only assert `<div id="root">`, which the
// real Vite output contains too, and the container-smoke embedded-console gate
// (#114, ADR-0034 amendment) runs against an image that downloads the real
// spa-dist artifact, so it is green either way.
//
// This asserts the invariant ("the embedded index is self-consistent") rather
// than the mechanism, so it keeps working across vite `base`/`assetsDir`
// renames and does not care whether the offending commit came from a human or
// an agent. It runs against the embed.FS directly rather than through
// Handler(): the SPA fallback answers a missing /favicon.png with index.html
// and a 200, so an HTTP-level check would miss every broken reference outside
// assets/.
//
// It passes in BOTH tree shapes. On a node-free checkout the placeholder
// references nothing. After a local `npm run build`, dist/ holds the real
// bundle and every reference resolves. In CI it runs in the `test` AND
// `integration` jobs (the latter's `-tags=integration` run includes untagged
// tests), whose workspaces are exactly the committed tree — do NOT download the
// `spa-dist` artifact into either, or this test stops guarding anything (see
// the note on the `test` job in .github/workflows/ci.yml).
//
// If this fails in CI but not on your machine, your local dist/assets/ is
// hiding the bug: the COMMITTED index.html is Vite build output. Restore the
// placeholder and re-commit that file alone:
//
//	printf '<!doctype html><html><body><div id="root"></div></body></html>\n' > internal/spa/dist/index.html
func TestEmbeddedIndexReferencesResolve(t *testing.T) {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		t.Fatalf("sub dist FS: %v", err)
	}
	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		t.Fatalf("read embedded index.html: %v", err)
	}

	for _, m := range srcHrefRe.FindAllStringSubmatch(string(index), -1) {
		ref := attrValue(m)
		if ref == "" || !isSameOrigin(ref) {
			continue
		}

		// Strip the leading slash (embedded paths are dist-relative) and any
		// query/fragment a future cache-buster might append.
		p := strings.TrimPrefix(ref, "/")
		if i := strings.IndexAny(p, "?#"); i >= 0 {
			p = p[:i]
		}
		if p == "" {
			continue
		}
		// Normalise "./assets/x" — what vite emits under a relative `base` — to
		// the dist-relative form embed.FS expects. io/fs rejects an unclean
		// path outright, which would surface as a missing file and blame the
		// wrong thing on a tree that is actually fine.
		p = path.Clean(p)
		if !fs.ValidPath(p) {
			t.Errorf("embedded index.html references %q, which escapes the embedded dist/ tree", ref)
			continue
		}

		info, err := fs.Stat(sub, p)
		if err != nil {
			t.Errorf("embedded index.html references %q, which is NOT in the embedded dist/ — "+
				"a checkout with no `npm run build` would serve a broken page. "+
				"Is the committed index.html real Vite output instead of the placeholder? "+
				"(see this test's doc comment for the fix): %v", ref, err)
			continue
		}
		if info.IsDir() {
			t.Errorf("embedded index.html references %q, which is a directory, not a file", ref)
		}
	}
}

// attrValue returns the populated capture from a srcHrefRe match: double-quoted,
// single-quoted, or unquoted, in that order.
func attrValue(m []string) string {
	for _, g := range m[1:] {
		if g != "" {
			return g
		}
	}
	return ""
}

// isSameOrigin reports whether ref addresses the embedded bundle itself, as
// opposed to an external resource (the Google Fonts preconnect/stylesheet
// links), an inline data: URI, or a bare fragment. Only same-origin refs are
// ours to resolve.
func isSameOrigin(ref string) bool {
	switch {
	case schemeRe.MatchString(ref): // absolute URL, data:, mailto:, tel:, …
		return false
	case strings.HasPrefix(ref, "//"): // protocol-relative
		return false
	case strings.HasPrefix(ref, "#"): // in-page fragment
		return false
	}
	return true
}
