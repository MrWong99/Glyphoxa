package blob

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ServeCSP is the Content-Security-Policy every blob response carries. Blobs
// are user- or model-supplied bytes served SAME-ORIGIN with the operator
// console, so a blob that turns out to be a scriptable document (an SVG, an
// HTML file stored before an upload allowlist existed, or through any future
// writer) must never execute when opened directly: no scripts, no
// subresources. Inline style is allowed so an SVG that somehow is one still
// renders as a picture.
const ServeCSP = "default-src 'none'; style-src 'unsafe-inline'"

// ServeOptions describes one blob response to [Serve].
type ServeOptions struct {
	// ContentType is the type the OWNING ROW declares for the blob. Empty
	// falls back to the type stored beside the bytes ([Meta.ContentType]).
	ContentType string
	// ModTime is the owning row's version timestamp. It becomes Last-Modified
	// and — together with the key — the ETag, so it must change whenever the
	// bytes behind this URL change. For owners that write a new key per
	// version the key alone already does that; for owners that overwrite one
	// deterministic key in place (a re-requested Highlight sound) ModTime is
	// what distinguishes the versions.
	ModTime time.Time
}

// Serve answers one GET/HEAD for the blob at key: the shared tail of every
// byte mount (Highlight clip/image/sound, Map image, Node portrait). The
// caller has already authorized the request and resolved the owning row; Serve
// owns everything from there, so the mounts cannot drift apart on it again —
// three of them used to ship without the nosniff/CSP pair two others had.
//
//   - Hardening: X-Content-Type-Options: nosniff and [ServeCSP] on every
//     response.
//   - Caching: `Cache-Control: private, no-cache` (operator-only bytes: a
//     browser may keep them, a shared cache may not, and every reuse
//     revalidates because the URL names a row whose image can be replaced) and
//     a strong ETag derived from the blob key and the row version.
//   - Conditional requests are answered BEFORE the bytes are fetched: an
//     If-None-Match (or, absent one, If-Modified-Since) that still matches is
//     a 304 costing no blob read at all. Previously every revalidation loaded
//     the whole blob — up to 32 MiB out of Postgres — just to discard it.
//   - The body streams from the backend's own buffer: a [Store] whose Get
//     returns an io.ReadSeeker (the Postgres backend does) is served with no
//     second copy; a non-seekable backend is buffered once here. Range
//     requests (the <audio> scrubber) work either way.
//
// An absent blob is 404 — the row and its bytes should agree, but a
// reconciliation or purge race must not 500 — and yields a nil error. Any
// other failure is answered 500 and returned for the caller to log; the
// response has been written in every case.
func Serve(w http.ResponseWriter, req *http.Request, store Store, key string, opt ServeOptions) error {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", ServeCSP)
	h.Set("Cache-Control", "private, no-cache")
	etag := serveETag(key, opt.ModTime)
	h.Set("ETag", etag)
	if !opt.ModTime.IsZero() {
		h.Set("Last-Modified", opt.ModTime.UTC().Format(http.TimeFormat))
	}

	if notModified(req, etag, opt.ModTime) {
		w.WriteHeader(http.StatusNotModified)
		return nil
	}

	rc, meta, err := store.Get(req.Context(), key)
	if errors.Is(err, ErrNotFound) {
		clearValidators(h)
		http.NotFound(w, req)
		return nil
	}
	if err != nil {
		clearValidators(h)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return fmt.Errorf("blob: serve %s: %w", key, err)
	}
	defer rc.Close()

	body, ok := rc.(io.ReadSeeker)
	if !ok {
		data, rerr := io.ReadAll(rc)
		if rerr != nil {
			clearValidators(h)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return fmt.Errorf("blob: read %s: %w", key, rerr)
		}
		body = bytes.NewReader(data)
	}

	contentType := opt.ContentType
	if contentType == "" {
		contentType = meta.ContentType
	}
	if contentType == "" {
		// nosniff is set, so the browser will not guess; do not let
		// ServeContent sniff one either.
		contentType = "application/octet-stream"
	}
	h.Set("Content-Type", contentType)
	// ServeContent handles Range and If-Range against the ETag/Last-Modified
	// set above (it re-checks the preconditions, which by now cannot match).
	http.ServeContent(w, req, "", opt.ModTime, body)
	return nil
}

// serveETag is the strong validator for one blob version: a digest of the key
// and the owning row's version time. The key is never exposed raw — it embeds
// the tenant id.
func serveETag(key string, modTime time.Time) string {
	sum := sha256.New()
	sum.Write([]byte(key))
	sum.Write([]byte{0})
	sum.Write([]byte(strconv.FormatInt(modTime.UnixNano(), 10)))
	return `"` + hex.EncodeToString(sum.Sum(nil)[:16]) + `"`
}

// notModified reports whether the request's cache validators still match, per
// RFC 9110 §13.1: If-None-Match takes precedence, and If-Modified-Since is
// consulted only without it. Evaluated on the row's metadata alone, so a hit
// never touches the blob. A Range request is left to http.ServeContent.
func notModified(req *http.Request, etag string, modTime time.Time) bool {
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		return false
	}
	if req.Header.Get("Range") != "" {
		return false
	}
	if inm := req.Header.Get("If-None-Match"); inm != "" {
		return etagListed(inm, etag)
	}
	ims := req.Header.Get("If-Modified-Since")
	if ims == "" || modTime.IsZero() {
		return false
	}
	t, err := http.ParseTime(ims)
	if err != nil {
		return false
	}
	// HTTP dates carry whole seconds.
	return !modTime.Truncate(time.Second).After(t)
}

// etagListed reports whether an If-None-Match value names etag (or is "*").
// If-None-Match uses the weak comparison, so a W/ prefix is ignored.
func etagListed(headerValue, etag string) bool {
	for _, candidate := range strings.Split(headerValue, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}

// clearValidators drops the caching headers before an error response, so a 404
// or 500 is never stored against the blob's validators.
func clearValidators(h http.Header) {
	h.Del("ETag")
	h.Del("Last-Modified")
	h.Set("Cache-Control", "no-store")
}

// Reader is the seekable, in-memory body a buffering backend returns from
// [Store.Get]: the bytes it already holds, readable and seekable in place.
// [Serve] streams from it directly (Range included) instead of copying the
// payload a second time.
type Reader struct {
	*bytes.Reader
}

// NewReader wraps an in-memory payload. The Reader does not copy data and the
// caller must not modify it afterwards.
func NewReader(data []byte) *Reader {
	return &Reader{Reader: bytes.NewReader(data)}
}

// Close implements io.Closer; an in-memory payload holds no resource.
func (*Reader) Close() error { return nil }

var _ io.ReadSeekCloser = (*Reader)(nil)
