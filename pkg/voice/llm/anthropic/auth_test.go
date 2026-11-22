package anthropic_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/anthropic"
)

// authHeaders runs one completion with key and returns the request's auth
// headers as the server saw them.
func authHeaders(t *testing.T, key string) http.Header {
	t.Helper()
	var seen http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range endTurn() {
			_, _ = w.Write([]byte(ev))
		}
	}))
	defer srv.Close()

	ch, err := anthropic.New(key, anthropic.WithBaseURL(srv.URL)).Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	collect(t, ch)
	return seen
}

// TestComplete_OAuthAccessToken_SendsBearer pins the `ant` CLI login path
// (#702): an OAuth access token ("sk-ant-oat…") is only accepted as
// "Authorization: Bearer"; sent as x-api-key the API answers 401 "invalid
// x-api-key". anthropic-version stays 2023-06-01 and no beta header is needed
// (both verified live).
func TestComplete_OAuthAccessToken_SendsBearer(t *testing.T) {
	h := authHeaders(t, "sk-ant-oat01-test-token")

	if got := h.Get("Authorization"); got != "Bearer sk-ant-oat01-test-token" {
		t.Errorf("Authorization = %q, want the bearer token", got)
	}
	if got := h.Get("x-api-key"); got != "" {
		t.Errorf("x-api-key = %q, want it absent for an OAuth token", got)
	}
	if got := h.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version = %q, want 2023-06-01", got)
	}
}

// TestComplete_APIKey_StillSendsXAPIKey pins that an ordinary API key is
// unchanged by the OAuth path.
func TestComplete_APIKey_StillSendsXAPIKey(t *testing.T) {
	h := authHeaders(t, "sk-ant-api03-test-key")

	if got := h.Get("x-api-key"); got != "sk-ant-api03-test-key" {
		t.Errorf("x-api-key = %q, want the API key", got)
	}
	if got := h.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want it absent for an API key", got)
	}
}
