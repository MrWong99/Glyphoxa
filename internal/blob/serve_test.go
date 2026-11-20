package blob_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/blob"
)

// serveStore is a one-blob Store that counts Gets and bytes read, so the tests
// can prove what Serve did NOT do (fetch on a 304, copy the whole payload).
type serveStore struct {
	blob.Store // Put/Delete/List are never reached by Serve

	key         string
	data        []byte
	contentType string
	seekable    bool
	getErr      error

	gets      int
	bytesRead int
}

type countingReader struct {
	io.ReadSeeker
	s *serveStore
}

func (c countingReader) Read(p []byte) (int, error) {
	n, err := c.ReadSeeker.Read(p)
	c.s.bytesRead += n
	return n, err
}
func (countingReader) Close() error { return nil }

// readOnly hides Seek, modelling a streaming (non-buffering) backend.
type readOnly struct{ io.Reader }

func (readOnly) Close() error { return nil }

func (s *serveStore) Get(_ context.Context, key string) (io.ReadCloser, blob.Meta, error) {
	s.gets++
	if s.getErr != nil {
		return nil, blob.Meta{}, s.getErr
	}
	if key != s.key {
		return nil, blob.Meta{}, blob.ErrNotFound
	}
	meta := blob.Meta{ContentType: s.contentType, Size: int64(len(s.data))}
	if s.seekable {
		return countingReader{ReadSeeker: bytes.NewReader(s.data), s: s}, meta, nil
	}
	return readOnly{Reader: bytes.NewReader(s.data)}, meta, nil
}

func newServeStore(t *testing.T, size int) *serveStore {
	t.Helper()
	key, err := blob.Key(uuid.New(), "map", uuid.New(), "image")
	if err != nil {
		t.Fatalf("build key: %v", err)
	}
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i)
	}
	return &serveStore{key: key, data: data, contentType: "image/png", seekable: true}
}

var rowTime = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func serveOnce(t *testing.T, s *serveStore, key string, opt blob.ServeOptions, header http.Header) (*httptest.ResponseRecorder, error) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/maps/x/image", nil)
	for k, vs := range header {
		req.Header[k] = vs
	}
	rec := httptest.NewRecorder()
	err := blob.Serve(rec, req, s, key, opt)
	return rec, err
}

// TestServe_HardeningAndCachingHeaders pins what every byte mount now answers
// with (R3): the nosniff/CSP pair (three Highlight routes used to ship without
// it), `Cache-Control: private, no-cache`, and an ETag — none of which the
// mounts sent before.
func TestServe_HardeningAndCachingHeaders(t *testing.T) {
	s := newServeStore(t, 64)
	rec, err := serveOnce(t, s, s.key, blob.ServeOptions{ModTime: rowTime}, nil)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), s.data) {
		t.Fatalf("status=%d, %d body bytes; want 200 and the %d stored bytes", rec.Code, rec.Body.Len(), len(s.data))
	}
	for name, want := range map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"Content-Security-Policy": blob.ServeCSP,
		"Cache-Control":           "private, no-cache",
		"Content-Type":            "image/png",
		"Last-Modified":           "Fri, 20 Nov 2026 12:00:00 GMT",
	} {
		if got := rec.Header().Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	etag := rec.Header().Get("ETag")
	if len(etag) < 4 || etag[0] != '"' || etag[len(etag)-1] != '"' {
		t.Errorf("ETag = %q, want a quoted strong validator", etag)
	}
	if bytes.Contains([]byte(etag), []byte(s.key)) {
		t.Errorf("ETag %q exposes the raw blob key (it embeds the tenant id)", etag)
	}
}

// TestServe_NotModifiedSkipsTheBlobFetch is the R3 regression: a revalidation
// whose validator still matches must be a 304 WITHOUT loading the blob. Before
// the fix every conditional request read the whole payload out of the store
// (up to 32 MiB), only for http.ServeContent to discard it.
func TestServe_NotModifiedSkipsTheBlobFetch(t *testing.T) {
	s := newServeStore(t, 64)
	first, _ := serveOnce(t, s, s.key, blob.ServeOptions{ModTime: rowTime}, nil)
	etag := first.Header().Get("ETag")
	s.gets = 0

	for name, header := range map[string]http.Header{
		"If-None-Match":           {"If-None-Match": {etag}},
		"If-None-Match weak/list": {"If-None-Match": {`"other", W/` + etag}},
		"If-Modified-Since":       {"If-Modified-Since": {"Fri, 20 Nov 2026 12:00:00 GMT"}},
	} {
		rec, err := serveOnce(t, s, s.key, blob.ServeOptions{ModTime: rowTime}, header)
		if err != nil {
			t.Fatalf("%s: Serve: %v", name, err)
		}
		if rec.Code != http.StatusNotModified {
			t.Errorf("%s: status = %d, want 304", name, rec.Code)
		}
		if rec.Body.Len() != 0 {
			t.Errorf("%s: 304 carried a %d-byte body", name, rec.Body.Len())
		}
		if rec.Header().Get("ETag") != etag {
			t.Errorf("%s: 304 ETag = %q, want %q", name, rec.Header().Get("ETag"), etag)
		}
	}
	if s.gets != 0 {
		t.Errorf("the blob was fetched %d times to answer 304s, want 0", s.gets)
	}
}

// TestServe_StaleValidatorRefetches proves the validators actually version the
// bytes: a new row version (the same deterministic key overwritten in place — a
// re-requested Highlight sound) or a new key (a replaced Map image) changes the
// ETag, so the old one no longer short-circuits.
func TestServe_StaleValidatorRefetches(t *testing.T) {
	s := newServeStore(t, 64)
	first, _ := serveOnce(t, s, s.key, blob.ServeOptions{ModTime: rowTime}, nil)
	etag := first.Header().Get("ETag")

	later := rowTime.Add(time.Minute)
	rec, _ := serveOnce(t, s, s.key, blob.ServeOptions{ModTime: later}, http.Header{"If-None-Match": {etag}})
	if rec.Code != http.StatusOK {
		t.Errorf("same key, newer row version: status = %d, want 200 (the bytes changed)", rec.Code)
	}
	if rec.Header().Get("ETag") == etag {
		t.Error("a newer row version produced the same ETag")
	}
	// If-Modified-Since from before the new version must not match either.
	rec, _ = serveOnce(t, s, s.key, blob.ServeOptions{ModTime: later}, http.Header{"If-Modified-Since": {"Fri, 20 Nov 2026 12:00:00 GMT"}})
	if rec.Code != http.StatusOK {
		t.Errorf("If-Modified-Since older than the row: status = %d, want 200", rec.Code)
	}

	other := newServeStore(t, 64)
	rec, _ = serveOnce(t, other, other.key, blob.ServeOptions{ModTime: rowTime}, nil)
	if rec.Header().Get("ETag") == etag {
		t.Error("a different blob key produced the same ETag")
	}
}

// TestServe_SeekableBodyIsNotCopied is the other half of R3: when the backend
// hands back a seekable body, a small Range request reads only that range.
// The old mounts io.ReadAll'd the body into a second buffer first, so every
// request — a 1-byte probe included — cost two full copies of the blob.
func TestServe_SeekableBodyIsNotCopied(t *testing.T) {
	s := newServeStore(t, 1<<20)
	rec, err := serveOnce(t, s, s.key, blob.ServeOptions{ModTime: rowTime}, http.Header{"Range": {"bytes=100-199"}})
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if rec.Code != http.StatusPartialContent || !bytes.Equal(rec.Body.Bytes(), s.data[100:200]) {
		t.Fatalf("status=%d with %d bytes, want 206 and bytes 100-199", rec.Code, rec.Body.Len())
	}
	if s.bytesRead > 64<<10 {
		t.Errorf("a 100-byte Range read %d bytes of a 1 MiB blob — the payload was copied instead of streamed", s.bytesRead)
	}
}

// TestServe_NonSeekableBackendStillRanges keeps the seam honest for a future
// streaming backend: a body that cannot seek is buffered once and Range works.
func TestServe_NonSeekableBackendStillRanges(t *testing.T) {
	s := newServeStore(t, 200)
	s.seekable = false
	rec, err := serveOnce(t, s, s.key, blob.ServeOptions{ModTime: rowTime}, http.Header{"Range": {"bytes=0-99"}})
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if rec.Code != http.StatusPartialContent || !bytes.Equal(rec.Body.Bytes(), s.data[:100]) {
		t.Fatalf("status=%d with %d bytes, want 206 and the first 100 bytes", rec.Code, rec.Body.Len())
	}
}

// TestServe_ContentType pins the precedence: the owning row's declared type,
// else the type stored beside the bytes, else a non-sniffed octet-stream.
func TestServe_ContentType(t *testing.T) {
	s := newServeStore(t, 16)
	rec, _ := serveOnce(t, s, s.key, blob.ServeOptions{ContentType: "audio/wav", ModTime: rowTime}, nil)
	if ct := rec.Header().Get("Content-Type"); ct != "audio/wav" {
		t.Errorf("row-declared type: Content-Type = %q, want audio/wav", ct)
	}
	s.contentType = ""
	s.data = []byte("<html><script>alert(1)</script></html>")
	rec, _ = serveOnce(t, s, s.key, blob.ServeOptions{ModTime: rowTime}, nil)
	if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("untyped blob: Content-Type = %q, want application/octet-stream (never sniffed to text/html)", ct)
	}
}

// TestServe_MissingAndFailingBlob: an absent blob is a quiet 404 (a purge race
// must not 500 or log), a backend failure is a 500 returned to the caller for
// logging, and neither response keeps the blob's cache validators.
func TestServe_MissingAndFailingBlob(t *testing.T) {
	s := newServeStore(t, 16)
	missing, _ := blob.Key(uuid.New(), "map", uuid.New(), "image")
	rec, err := serveOnce(t, s, missing, blob.ServeOptions{ModTime: rowTime}, nil)
	if err != nil {
		t.Errorf("missing blob: err = %v, want nil", err)
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing blob: status = %d, want 404", rec.Code)
	}

	s.getErr = errors.New("connection reset")
	failed, err := serveOnce(t, s, s.key, blob.ServeOptions{ModTime: rowTime}, nil)
	if err == nil || !errors.Is(err, s.getErr) {
		t.Errorf("backend failure: err = %v, want it to wrap the store error", err)
	}
	if failed.Code != http.StatusInternalServerError {
		t.Errorf("backend failure: status = %d, want 500", failed.Code)
	}
	for name, r := range map[string]*httptest.ResponseRecorder{"404": rec, "500": failed} {
		if r.Header().Get("ETag") != "" || r.Header().Get("Last-Modified") != "" {
			t.Errorf("%s response still carries the blob's validators", name)
		}
		if cc := r.Header().Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s response Cache-Control = %q, want no-store", name, cc)
		}
		if r.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s response lost nosniff", name)
		}
	}
}

// TestReader_IsSeekable pins the body type a buffering backend returns.
func TestReader_IsSeekable(t *testing.T) {
	var body io.ReadCloser = blob.NewReader([]byte("abcdef"))
	rs, ok := body.(io.ReadSeeker)
	if !ok {
		t.Fatal("blob.Reader is not an io.ReadSeeker")
	}
	if _, err := rs.Seek(3, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	rest, _ := io.ReadAll(rs)
	if string(rest) != "def" {
		t.Errorf("after Seek(3): read %q, want %q", rest, "def")
	}
	if err := body.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}
