package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/auth"
)

// TestDiscordClient_Exchange covers the OAuth token exchange against an httptest
// "cassette" standing in for discord.com — no live Discord call (issue #67
// acceptance). It asserts the code→token→user round trip parses correctly and
// composes the avatar CDN URL, and that the token request carried the code +
// client credentials.
func TestDiscordClient_Exchange(t *testing.T) {
	t.Parallel()

	var gotCode, gotClientID, gotGrant, gotBearer string
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotCode = r.Form.Get("code")
		gotClientID = r.Form.Get("client_id")
		gotGrant = r.Form.Get("grant_type")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at-123","token_type":"Bearer"}`))
	})
	mux.HandleFunc("/users/@me", func(w http.ResponseWriter, r *http.Request) {
		gotBearer = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"99","username":"sora","global_name":"Sora Vance","avatar":"abc"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := auth.NewDiscordClient(auth.DiscordConfig{
		ClientID:     "client-1",
		ClientSecret: "secret-1",
		RedirectURL:  "https://app/auth/discord/callback",
		TokenURL:     srv.URL + "/oauth2/token",
		UserURL:      srv.URL + "/users/@me",
		HTTPClient:   srv.Client(),
	})

	du, err := client.Exchange(context.Background(), "the-code")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if du.ID != "99" || du.Username != "sora" || du.GlobalName != "Sora Vance" {
		t.Errorf("user = %+v", du)
	}
	if du.DisplayName() != "Sora Vance" {
		t.Errorf("DisplayName = %q, want global name", du.DisplayName())
	}
	if du.AvatarURL != "https://cdn.discordapp.com/avatars/99/abc.png" {
		t.Errorf("AvatarURL = %q", du.AvatarURL)
	}
	if gotCode != "the-code" || gotClientID != "client-1" || gotGrant != "authorization_code" {
		t.Errorf("token request: code=%q client_id=%q grant=%q", gotCode, gotClientID, gotGrant)
	}
	if gotBearer != "Bearer at-123" {
		t.Errorf("userinfo Authorization = %q, want Bearer at-123", gotBearer)
	}
}

// TestDiscordClient_AuthCodeURL asserts the authorize redirect carries the
// client id, redirect uri, response_type=code, identify scope, and the state.
func TestDiscordClient_AuthCodeURL(t *testing.T) {
	t.Parallel()
	client := auth.NewDiscordClient(auth.DiscordConfig{
		ClientID:    "cid",
		RedirectURL: "https://app/cb",
	})
	got := client.AuthCodeURL("state-xyz")
	for _, want := range []string{
		"client_id=cid", "response_type=code", "scope=identify", "state=state-xyz",
		"redirect_uri=https%3A%2F%2Fapp%2Fcb",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("AuthCodeURL missing %q in %q", want, got)
		}
	}
}
