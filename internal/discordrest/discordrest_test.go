package discordrest_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/discordrest"
)

// TestGetJSON pins the shared Bot GET: the Bot auth + DiscordBot User-Agent
// ride every call, a 200 decodes, and any other status comes back as data with
// no error (the callers map 403/404 to their own sentinels).
func TestGetJSON(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bot tok" || r.Header.Get("User-Agent") != discordrest.UserAgent {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/missing" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"id":"42"}`))
	}))
	t.Cleanup(srv.Close)

	var out struct {
		ID string `json:"id"`
	}
	status, err := discordrest.GetJSON(context.Background(), "tok", srv.URL+"/ok", &out)
	if err != nil || status != http.StatusOK || out.ID != "42" {
		t.Fatalf("GetJSON ok = %d %v %+v", status, err, out)
	}
	status, err = discordrest.GetJSON(context.Background(), "tok", srv.URL+"/missing", &out)
	if err != nil || status != http.StatusNotFound {
		t.Fatalf("GetJSON missing = %d %v, want 404 and no error", status, err)
	}
	status, _ = discordrest.GetJSON(context.Background(), "wrong", srv.URL+"/ok", &out)
	if status != http.StatusUnauthorized {
		t.Fatalf("GetJSON bad token = %d, want 401", status)
	}
}

// TestStatusError pins the classifier contract: the status is reachable as
// data through wrapping.
func TestStatusError(t *testing.T) {
	t.Parallel()
	err := error(&discordrest.StatusError{Op: "discordtag: GET /users/@me", Status: 401})
	if err.Error() != "discordtag: GET /users/@me: HTTP 401" {
		t.Errorf("Error() = %q", err)
	}
	var se *discordrest.StatusError
	if !errors.As(errors.Join(errors.New("ctx"), err), &se) || se.Status != 401 {
		t.Errorf("StatusError not reachable through wrapping")
	}
	if discordrest.BaseURL("") != discordrest.LiveAPIBaseURL || discordrest.BaseURL("http://x") != "http://x" {
		t.Errorf("BaseURL seam broken")
	}
}
