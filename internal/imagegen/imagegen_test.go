package imagegen_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/blob"
	"github.com/MrWong99/Glyphoxa/internal/imagegen"
)

// wantPNG is the decoded image bytes the golden fixture base64-encodes.
var wantPNG = []byte("\x89PNG\r\n\x1a\n-fake-image-bytes")

// goldenResponse is a captured-shape generateContent success: one candidate with
// an inline PNG, plus usageMetadata token counts.
func goldenResponse(t *testing.T) string {
	t.Helper()
	body := map[string]any{
		"candidates": []any{
			map[string]any{
				"content": map[string]any{
					"parts": []any{
						// A leading text part the model sometimes emits — the adapter
						// must skip it and find the inlineData part.
						map[string]any{"text": "Here is your scene."},
						map[string]any{
							"inlineData": map[string]any{
								"mimeType": "image/png",
								"data":     base64.StdEncoding.EncodeToString(wantPNG),
							},
						},
					},
				},
			},
		},
		"usageMetadata": map[string]any{
			"promptTokenCount":     37,
			"candidatesTokenCount": 1290,
		},
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal golden: %v", err)
	}
	return string(b)
}

func TestGemini_Generate_RequestShapeAndParse(t *testing.T) {
	var gotPath, gotKey, gotCT string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-goog-api-key")
		gotCT = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, goldenResponse(t))
	}))
	defer srv.Close()

	gen := imagegen.NewGemini("secret-key",
		imagegen.WithBaseURL(srv.URL),
		imagegen.WithModel("gemini-2.5-flash-image"))

	res, err := gen.Generate(context.Background(), "a dragon burns the tavern")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	// Request shape (the shared contract #309/#310 consume).
	if gotPath != "/v1beta/models/gemini-2.5-flash-image:generateContent" {
		t.Errorf("path = %q", gotPath)
	}
	if gotKey != "secret-key" {
		t.Errorf("api key header = %q", gotKey)
	}
	if !strings.HasPrefix(gotCT, "application/json") {
		t.Errorf("content-type = %q", gotCT)
	}
	// generationConfig.responseModalities == ["TEXT","IMAGE"] (canonical shape;
	// image-only 400'd on prior models). The parser skips the text part.
	gc, _ := gotBody["generationConfig"].(map[string]any)
	mods, _ := gc["responseModalities"].([]any)
	if len(mods) != 2 || mods[0] != "TEXT" || mods[1] != "IMAGE" {
		t.Errorf("responseModalities = %v, want [TEXT IMAGE]", mods)
	}
	// contents[0].parts[0].text == prompt
	contents, _ := gotBody["contents"].([]any)
	if len(contents) != 1 {
		t.Fatalf("contents = %v", contents)
	}
	c0, _ := contents[0].(map[string]any)
	parts, _ := c0["parts"].([]any)
	p0, _ := parts[0].(map[string]any)
	if p0["text"] != "a dragon burns the tavern" {
		t.Errorf("prompt text = %v", p0["text"])
	}

	// Parse: decoded bytes, MIME, and usage counts.
	if string(res.Data) != string(wantPNG) {
		t.Errorf("image bytes mismatch: %q", res.Data)
	}
	if res.ContentType != "image/png" {
		t.Errorf("content type = %q", res.ContentType)
	}
	if res.PromptTokens != 37 || res.OutputTokens != 1290 {
		t.Errorf("tokens = %d/%d, want 37/1290", res.PromptTokens, res.OutputTokens)
	}
}

// statusServer replies with one status + body, the shape every non-2xx test needs.
func statusServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGemini_Generate_Non2xx(t *testing.T) {
	srv := statusServer(t, http.StatusTooManyRequests, `{"error":{"message":"quota"}}`)

	gen := imagegen.NewGemini("k", imagegen.WithBaseURL(srv.URL))
	if _, err := gen.Generate(context.Background(), "x"); err == nil {
		t.Fatal("want error on 429, got nil")
	} else if !strings.Contains(err.Error(), "429") {
		t.Errorf("error should carry the status: %v", err)
	}
}

// TestGemini_Generate_StatusIsTyped is the whole point of [imagegen.StatusError]:
// the status must survive as DATA, not only as text in the message, so a caller
// can tell "you are out of quota" from "your key was rejected" without grepping.
func TestGemini_Generate_StatusIsTyped(t *testing.T) {
	srv := statusServer(t, http.StatusTooManyRequests,
		`{"error":{"code":429,"message":"You exceeded your current quota"}}`)

	gen := imagegen.NewGemini("k", imagegen.WithBaseURL(srv.URL))
	_, err := gen.Generate(context.Background(), "x")

	var se *imagegen.StatusError
	if !errors.As(err, &se) {
		t.Fatalf("want a *StatusError, got %T: %v", err, err)
	}
	if se.StatusCode != http.StatusTooManyRequests {
		t.Errorf("StatusCode = %d, want 429", se.StatusCode)
	}
	if !strings.Contains(se.Body, "exceeded your current quota") {
		t.Errorf("the provider's own explanation was dropped: %q", se.Body)
	}
	// The message is unchanged from the untyped version, so logs and dead-letter
	// payloads read exactly as they did.
	if got, want := se.Error(), "imagegen: HTTP 429: "+se.Body; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// TestGemini_Generate_StatusPredicates pins the classification each caller
// branches on. The 429 row is the reported bug: a VALID key that ran out of
// quota must never be reported as an auth or configuration problem.
func TestGemini_Generate_StatusPredicates(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		quota, auth bool
	}{
		{"rate limited", http.StatusTooManyRequests, true, false},
		{"key rejected", http.StatusUnauthorized, false, true},
		{"key forbidden", http.StatusForbidden, false, true},
		{"provider down", http.StatusInternalServerError, false, false},
		{"malformed request", http.StatusBadRequest, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := statusServer(t, tc.status, `{"error":{"message":"nope"}}`)
			gen := imagegen.NewGemini("k", imagegen.WithBaseURL(srv.URL))
			_, err := gen.Generate(context.Background(), "x")
			if err == nil {
				t.Fatalf("want an error on HTTP %d", tc.status)
			}
			if got := imagegen.IsQuotaExceeded(err); got != tc.quota {
				t.Errorf("IsQuotaExceeded = %v, want %v", got, tc.quota)
			}
			if got := imagegen.IsAuthFailure(err); got != tc.auth {
				t.Errorf("IsAuthFailure = %v, want %v", got, tc.auth)
			}
			// A 5xx and a 400 are neither: they must fall through to the caller's
			// own handling rather than being reported as a bill or a bad key.
		})
	}
}

// TestStatusPredicates_IgnoreOtherErrors: every predicate must say "no" to an
// error that is not an upstream status at all — an oversize image, a transport
// failure, a nil error — so no caller silently reclassifies them.
func TestStatusPredicates_IgnoreOtherErrors(t *testing.T) {
	for _, err := range []error{nil, imagegen.ErrImageTooLarge, imagegen.ErrNotConfigured, errors.New("dial tcp: i/o timeout")} {
		if imagegen.IsQuotaExceeded(err) || imagegen.IsAuthFailure(err) {
			t.Errorf("%v was classified as an upstream status failure", err)
		}
	}
}

// TestStatusError_WrapsThroughFmt: callers wrap with %w on the way up (mapgen,
// the enrichment job), so the predicates must see through those layers.
func TestStatusError_WrapsThroughFmt(t *testing.T) {
	err := fmt.Errorf("highlight enrich: generate image: %w",
		&imagegen.StatusError{StatusCode: http.StatusTooManyRequests, Body: "quota"})
	if !imagegen.IsQuotaExceeded(err) {
		t.Error("a wrapped 429 stopped being a quota failure")
	}
}

func TestGemini_Generate_NoInlineData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// A text-only candidate (safety refusal / model returned prose, no image).
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"I can't do that"}]}}]}`)
	}))
	defer srv.Close()

	gen := imagegen.NewGemini("k", imagegen.WithBaseURL(srv.URL))
	if _, err := gen.Generate(context.Background(), "x"); err == nil {
		t.Fatal("want error when no inline image data, got nil")
	}
}

func TestGemini_Generate_OversizeIsPermanent(t *testing.T) {
	// An image decoding past the blob cap must return ErrImageTooLarge (permanent),
	// so the handler skips it instead of re-billing on every retry.
	big := make([]byte, blob.MaxSize+1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := json.Marshal(map[string]any{
			"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{
				map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(big)}},
			}}}},
		})
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	gen := imagegen.NewGemini("k", imagegen.WithBaseURL(srv.URL))
	_, err := gen.Generate(context.Background(), "x")
	if !errors.Is(err, imagegen.ErrImageTooLarge) {
		t.Fatalf("want ErrImageTooLarge, got %v", err)
	}
}

func TestGemini_Generate_EmptyKey(t *testing.T) {
	t.Setenv(imagegen.APIKeyEnv, "")
	gen := imagegen.NewGemini("") // no arg, no env → missing key
	if _, err := gen.Generate(context.Background(), "x"); err == nil {
		t.Fatal("want missing-key error, got nil")
	}
}
