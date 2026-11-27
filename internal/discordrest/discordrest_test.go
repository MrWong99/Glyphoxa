package discordrest_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"testing"
	"time"

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

// TestClientsDoNotShareDefaultTransport pins the private transport (#602):
// every httptest.Server.Close calls CloseIdleConnections on
// http.DefaultTransport, so clients riding it lose their keep-alive
// connections whenever an unrelated parallel test finishes, and a request
// caught reusing one dies with "http: CloseIdleConnections called".
func TestClientsDoNotShareDefaultTransport(t *testing.T) {
	t.Parallel()
	if tr := discordrest.Client.Transport; tr == nil || tr == http.DefaultTransport {
		t.Fatalf("Client.Transport = %v, want a private transport", tr)
	}
	if discordrest.UploadClient.Transport != discordrest.Client.Transport {
		t.Fatalf("UploadClient must share Client's transport (one pool to Discord)")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"42"}`))
	}))
	t.Cleanup(srv.Close)

	// The trace makes the check deterministic: wait until the first call's
	// connection is back in the idle pool, then see whether the second call
	// still gets it after an unrelated server closed.
	idle := make(chan struct{}, 2)
	var reused bool
	ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
		GotConn:     func(info httptrace.GotConnInfo) { reused = info.Reused },
		PutIdleConn: func(error) { idle <- struct{}{} },
	})
	get := func() {
		t.Helper()
		var out struct {
			ID string `json:"id"`
		}
		status, err := discordrest.GetJSON(ctx, "tok", srv.URL+"/ok", &out)
		if err != nil || status != http.StatusOK || out.ID != "42" {
			t.Fatalf("GetJSON = %d %v %+v", status, err, out)
		}
	}

	get()
	select {
	case <-idle:
	case <-time.After(10 * time.Second):
		t.Fatal("first call's connection never returned to the idle pool")
	}

	other := httptest.NewServer(http.NotFoundHandler())
	other.Close() // CloseIdleConnections on http.DefaultTransport

	get()
	if !reused {
		t.Fatal("an unrelated httptest.Server.Close dropped the idle Discord connection")
	}
}
