package anthropic_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/providererr"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/anthropic"
)

// ListModels walks GET /v1/models to the last page and returns every id in the
// API's own order, authenticated like a completion.
func TestListModels_PagesThroughCatalog(t *testing.T) {
	var gotPaths []string
	var gotKey, gotVersion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path+"?"+r.URL.RawQuery)
		gotKey, gotVersion = r.Header.Get("x-api-key"), r.Header.Get("anthropic-version")
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("after_id") == "" {
			_, _ = w.Write([]byte(`{"data":[{"id":"claude-haiku-5-5","type":"model"},{"id":"claude-opus-4-8","type":"model"}],"has_more":true,"last_id":"claude-opus-4-8"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-haiku-4-5","type":"model"}],"has_more":false,"last_id":"claude-haiku-4-5"}`))
	}))
	defer srv.Close()

	got, err := anthropic.New("sk-test", anthropic.WithBaseURL(srv.URL)).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	want := []string{"claude-haiku-5-5", "claude-opus-4-8", "claude-haiku-4-5"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ids = %v, want %v", got, want)
	}
	if gotKey != "sk-test" || gotVersion != "2023-06-01" {
		t.Errorf("headers: x-api-key=%q anthropic-version=%q", gotKey, gotVersion)
	}
	if len(gotPaths) != 2 || !strings.HasPrefix(gotPaths[0], "/v1/models?") || !strings.Contains(gotPaths[1], "after_id=claude-opus-4-8") {
		t.Errorf("requests = %v, want two pages of /v1/models, the second after the first's last_id", gotPaths)
	}
}

// A rejected key surfaces as a typed HTTP error (so health classifies it), and
// a missing key never reaches the network.
func TestListModels_Errors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := anthropic.New("sk-bad", anthropic.WithBaseURL(srv.URL)).ListModels(context.Background())
	var httpErr *providererr.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("err = %v, want a providererr.HTTPError with status 401", err)
	}

	t.Setenv(anthropic.APIKeyEnv, "")
	if _, err := anthropic.New("", anthropic.WithBaseURL(srv.URL)).ListModels(context.Background()); err == nil || !strings.Contains(err.Error(), "missing API key") {
		t.Errorf("keyless err = %v, want a missing-API-key error", err)
	}
}

// A catalog that never stops paging is cut off rather than followed forever.
func TestListModels_BoundsPagination(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"data":[{"id":"m"}],"has_more":true,"last_id":"m"}`))
	}))
	defer srv.Close()
	if _, err := anthropic.New("k", anthropic.WithBaseURL(srv.URL)).ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if calls > 20 {
		t.Errorf("followed %d pages, want a bound", calls)
	}
}
