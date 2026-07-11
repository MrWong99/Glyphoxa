package imagegen_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	// generationConfig.responseModalities == ["IMAGE"]
	gc, _ := gotBody["generationConfig"].(map[string]any)
	mods, _ := gc["responseModalities"].([]any)
	if len(mods) != 1 || mods[0] != "IMAGE" {
		t.Errorf("responseModalities = %v", mods)
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

func TestGemini_Generate_Non2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"quota"}}`)
	}))
	defer srv.Close()

	gen := imagegen.NewGemini("k", imagegen.WithBaseURL(srv.URL))
	if _, err := gen.Generate(context.Background(), "x"); err == nil {
		t.Fatal("want error on 429, got nil")
	} else if !strings.Contains(err.Error(), "429") {
		t.Errorf("error should carry the status: %v", err)
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

func TestGemini_Generate_EmptyKey(t *testing.T) {
	t.Setenv(imagegen.APIKeyEnv, "")
	gen := imagegen.NewGemini("") // no arg, no env → missing key
	if _, err := gen.Generate(context.Background(), "x"); err == nil {
		t.Fatal("want missing-key error, got nil")
	}
}
