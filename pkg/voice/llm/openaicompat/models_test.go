package openaicompat_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/openaicompat"
)

// modelsServer replies to GET /models with the given raw JSON body and records
// the request method + path so a test can assert the OpenAI-compatible list call
// was made — no live endpoint, the cassette-replay posture (ADR-0021).
func modelsServer(t *testing.T, method, path *atomic.Value, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if method != nil {
			method.Store(r.Method)
		}
		if path != nil {
			path.Store(r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

// TestListModels_ParsesOpenAIListShape pins the happy path: the adapter GETs the
// OpenAI-compatible /models list and returns every id in the "data" array,
// UNFILTERED and in the provider's own order (curation is the caller's concern).
func TestListModels_ParsesOpenAIListShape(t *testing.T) {
	var method, path atomic.Value
	srv := modelsServer(t, &method, &path, http.StatusOK,
		`{"object":"list","data":[{"id":"llama-3.3-70b-versatile","object":"model"},{"id":"meta-llama/llama-4-scout-17b-16e-instruct","object":"model"},{"id":"whisper-large-v3","object":"model"}]}`)
	defer srv.Close()

	c := newClient(srv.URL)
	got, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}

	want := []string{"llama-3.3-70b-versatile", "meta-llama/llama-4-scout-17b-16e-instruct", "whisper-large-v3"}
	if len(got) != len(want) {
		t.Fatalf("models = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("models[%d] = %q, want %q (order/unfiltered)", i, got[i], want[i])
		}
	}
	if m, _ := method.Load().(string); m != http.MethodGet {
		t.Errorf("method = %q, want GET", m)
	}
	if p, _ := path.Load().(string); p != "/models" {
		t.Errorf("path = %q, want /models", p)
	}
}

// TestListModels_Non2xxErrors pins that a non-2xx catalog response is an error,
// not a silent empty list — the RPC layer decides how to degrade.
func TestListModels_Non2xxErrors(t *testing.T) {
	srv := modelsServer(t, nil, nil, http.StatusUnauthorized, `{"error":{"message":"invalid api key"}}`)
	defer srv.Close()

	if _, err := newClient(srv.URL).ListModels(context.Background()); err == nil {
		t.Fatal("ListModels against a 401 endpoint returned nil error")
	}
}

// TestListModels_MissingKey pins the same request-time missing-key posture as
// Complete: an empty key never reaches the endpoint.
func TestListModels_MissingKey(t *testing.T) {
	c := openaicompat.New(
		openaicompat.WithProviderName("test"),
		openaicompat.WithAPIKey(""),
		openaicompat.WithBaseURL("http://127.0.0.1:0"),
	)
	_, err := c.ListModels(context.Background())
	if err == nil {
		t.Fatal("ListModels with no key returned nil error")
	}
	if !strings.Contains(err.Error(), "missing API key") {
		t.Errorf("error %q missing %q", err, "missing API key")
	}
}
