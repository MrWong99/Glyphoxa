package bundle

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/auth"
)

// gzipBomb is a VALID bundle prefix whose one map image is 250 MiB of a single
// repeated byte — about 250 KiB compressed, the shape both audits measured
// pinning 0.6–1.1 GiB of RSS through the old ReadAll + double Unmarshal.
func gzipBomb(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = zw.Write([]byte(`{"format_version":3,"campaign":{"name":"x","system":"y","language":"en","agents":[],"maps":[{"id":"m","name":"m","image_base64":"`))
	chunk := bytes.Repeat([]byte("A"), 1<<20)
	for range 250 {
		_, _ = zw.Write(chunk)
	}
	_, _ = zw.Write([]byte(`"}]}}`))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// allocDuring reports the bytes allocated while fn runs.
func allocDuring(fn func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// bombAllocBudget is the TOTAL allocation a bomb may cost, below even the
// 256 MiB decode cap: the bomb must be refused once it passes the 32 MiB inflate
// floor (~128 MiB allocated, the json.Decoder's buffer doubling up to it), not
// after inflating to the cap. The old ReadAll + double-Unmarshal path allocated
// several times the cap.
const bombAllocBudget = 192 << 20

func TestDecodeRefusesGzipBombEarly(t *testing.T) {
	bomb := gzipBomb(t)
	if len(bomb) > 1<<20 {
		t.Fatalf("bomb is %d bytes compressed; the test wants a tiny upload", len(bomb))
	}
	var err error
	alloc := allocDuring(func() { _, err = Decode(bytes.NewReader(bomb)) })
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
	t.Logf("bomb: %d KiB compressed, %d MiB allocated", len(bomb)>>10, alloc>>20)
	if alloc > bombAllocBudget {
		t.Fatalf("decoding a %d KiB bomb allocated %d MiB; want < %d MiB", len(bomb)>>10, alloc>>20, bombAllocBudget>>20)
	}
}

// imageBundle gzips a bundle whose one map carries img as its base64 image.
func imageBundle(t *testing.T, img string) []byte {
	t.Helper()
	var buf bytes.Buffer
	err := Encode(&buf, &Bundle{
		FormatVersion: FormatVersion,
		Campaign: Campaign{
			Name: "Big", System: "dnd5e", Language: "en",
			Maps: []Map{{ID: "m1", Name: "World", ContentType: "image/png", ImageBase64: img}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDecodeAcceptsHonestLargeBundles(t *testing.T) {
	// Both decode well past the 32 MiB inflate floor, so the ratio guard is what
	// is being exercised, not the floor.
	raw := make([]byte, 30<<20)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	// A low-entropy "image": random over a two-letter alphabet, which gzip
	// shrinks several-fold — the compressible end of an honest bundle.
	lowEntropy := make([]byte, 40<<20)
	for i := range lowEntropy {
		lowEntropy[i] = 'A' + raw[i%len(raw)]&1
	}
	cases := map[string]string{
		"random image, ~1.3x": base64.StdEncoding.EncodeToString(raw),
		"compressible, ~6x":   string(lowEntropy),
	}
	for name, img := range cases {
		t.Run(name, func(t *testing.T) {
			gz := imageBundle(t, img)
			t.Logf("decoded image %d MiB from %d MiB compressed", len(img)>>20, len(gz)>>20)
			b, err := Decode(bytes.NewReader(gz))
			if err != nil {
				t.Fatalf("an honest large bundle must decode: %v", err)
			}
			if got := b.Campaign.Maps[0].ImageBase64; len(got) != len(img) {
				t.Fatalf("image is %d bytes after decode, want %d", len(got), len(img))
			}
		})
	}
}

func TestDecodeRatioGuardSparesPlainJSON(t *testing.T) {
	// The ratio guard measures gzip; a plain-JSON bundle (the seed path accepts
	// those) has no compressed side and is bounded by the decode cap alone.
	img := strings.Repeat("A", 40<<20)
	raw, err := json.Marshal(&Bundle{
		FormatVersion: FormatVersion,
		Campaign: Campaign{Name: "x", System: "y", Language: "en",
			Maps: []Map{{ID: "m1", Name: "m", ImageBase64: img}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(bytes.NewReader(raw)); err != nil {
		t.Fatalf("plain JSON under the decode cap must decode: %v", err)
	}
}

func TestDecodeMissingVersionUnsupported(t *testing.T) {
	raw := []byte(`{"exported_at":"2026-07-10T20:00:00Z","campaign":{"name":"x","system":"y","language":"de","agents":[]}}`)
	_, err := Decode(bytes.NewReader(raw))
	if !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("want ErrUnsupportedFormat, got %v", err)
	}
}

func TestDecodeRejectsNonObject(t *testing.T) {
	for _, raw := range []string{`[]`, `"x"`, ``, `{"format_version":3`} {
		if _, err := Decode(strings.NewReader(raw)); err == nil {
			t.Errorf("Decode(%q) succeeded, want an error", raw)
		}
	}
}

// multipartUpload wraps raw as the "bundle" file field of a POST import request.
func multipartUpload(t *testing.T, field string, raw []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	// A plain field ahead of the file: the streaming reader must skip it.
	if err := mw.WriteField("note", "ignored"); err != nil {
		t.Fatal(err)
	}
	fw, err := mw.CreateFormFile(field, "campaign.glyphoxa.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/campaigns/import", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

func TestServeImportRefusesGzipBomb(t *testing.T) {
	bomb := gzipBomb(t)
	h := &Handler{}
	rec := httptest.NewRecorder()
	req := multipartUpload(t, "bundle", bomb)
	alloc := allocDuring(func() { h.ServeImport(rec, req) })
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("code=%d body=%q, want 413", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "decoded size") {
		t.Errorf("body=%q, want the decoded-size refusal named", rec.Body.String())
	}
	t.Logf("bomb: %d KiB compressed, %d MiB allocated", len(bomb)>>10, alloc>>20)
	if alloc > bombAllocBudget {
		t.Fatalf("a %d KiB bomb upload allocated %d MiB; want < %d MiB", len(bomb)>>10, alloc>>20, bombAllocBudget>>20)
	}
}

func TestServeImportUploadShape(t *testing.T) {
	good := imageBundle(t, "")

	t.Run("missing bundle field is 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		(&Handler{}).ServeImport(rec, multipartUpload(t, "other", good))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d, want 400", rec.Code)
		}
	})

	t.Run("non-multipart body is 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/campaigns/import", bytes.NewReader(good))
		req.Header.Set("Content-Type", "application/gzip")
		rec := httptest.NewRecorder()
		(&Handler{}).ServeImport(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d, want 400", rec.Code)
		}
	})

	t.Run("garbage in the bundle part is 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		(&Handler{}).ServeImport(rec, multipartUpload(t, "bundle", []byte("not a bundle")))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d, want 400", rec.Code)
		}
	})

	for name, declared := range map[string]bool{
		"upload over the cap with a declared length is 413 before reading": true,
		// No Content-Length (a chunked upload): the cap fires while Decode reads
		// the part, and the MaxBytesError must still map to 413 from there.
		"upload over the cap is 413 although the part is read inside Decode": false,
	} {
		t.Run(name, func(t *testing.T) {
			defer SetImportLimitForTest(64 << 10)()
			raw := make([]byte, 256<<10)
			_, _ = rand.Read(raw)
			img := base64.StdEncoding.EncodeToString(raw)
			req := multipartUpload(t, "bundle", imageBundle(t, img))
			if !declared {
				req.ContentLength = -1
			}
			rec := httptest.NewRecorder()
			(&Handler{}).ServeImport(rec, req)
			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("code=%d body=%q, want 413", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "maximum upload size") {
				t.Errorf("body=%q, want the upload-size refusal", rec.Body.String())
			}
		})
	}
}

func TestServeExportSharesTheBundleSlot(t *testing.T) {
	release := HoldImportSlotsForTest()
	defer release()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/campaigns/x/export", nil)
	req.SetPathValue("id", uuid.NewString())
	req = req.WithContext(auth.WithTenant(req.Context(), uuid.New()))
	rec := httptest.NewRecorder()
	(&Handler{}).ServeExport(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d, want 503 while an import holds the bundle slot", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("503 without Retry-After")
	}
}
