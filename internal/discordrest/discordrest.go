// Package discordrest is the small shared core of Glyphoxa's hand-rolled
// Discord REST callers (internal/discordguild, discordinvite, discordtag,
// discordshare): the API root, the User-Agent Discord requires of bots, the
// shared HTTP clients (on a transport of their own, not http.DefaultTransport),
// and the authenticated "Bot token GET → JSON" helper each of them used to
// redeclare.
//
// The calls are plain net/http, deliberately NOT disgo's rest.NewClient: that
// client's rate limiter starts a cleanup goroutine (`for range ticker.C`) its
// Close never stops, leaking one goroutine + ticker per call (ADR-0047).
package discordrest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// LiveAPIBaseURL is Discord's REST API root, matching the version disgo pins.
const LiveAPIBaseURL = "https://discord.com/api/v10"

// UserAgent is the DiscordBot form Discord requires of bot REST callers.
const UserAgent = "DiscordBot (https://github.com/MrWong99/Glyphoxa, v2)"

// transport is the clients' own connection pool, deliberately not
// http.DefaultTransport: anything in the process may call CloseIdleConnections
// on the default (every httptest.Server.Close does), which breaks a Discord
// call caught reusing a keep-alive connection with "http: CloseIdleConnections
// called" (#602).
var transport = newTransport()

// newTransport clones http.DefaultTransport, keeping its proxy-from-env,
// dial/TLS timeouts, HTTP/2 and idle-pool defaults. If something replaced the
// default with another RoundTripper there is nothing to clone, so that one is
// used as is.
func newTransport() http.RoundTripper {
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		return t.Clone()
	}
	return http.DefaultTransport
}

// Client is shared across the read calls (http.Client is safe for concurrent
// use); its Timeout is a belt on top of the caller's ctx deadline so a call made
// without one still cannot hang.
var Client = &http.Client{Transport: transport, Timeout: 15 * time.Second}

// UploadClient is [Client] with a looser belt for file uploads, which can be
// much larger than a read. Both ride the same transport, so one pool of
// connections to Discord.
var UploadClient = &http.Client{Transport: transport, Timeout: 30 * time.Second}

// BaseURL resolves a caller's base-URL test seam: "" means the live API.
func BaseURL(base string) string {
	if base == "" {
		return LiveAPIBaseURL
	}
	return base
}

// NewRequest builds a request authenticated with a Bot token and carrying the
// required User-Agent.
func NewRequest(ctx context.Context, method, rawURL, token string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bot "+token)
	req.Header.Set("User-Agent", UserAgent)
	return req, nil
}

// GetJSON issues an authenticated Bot GET with [Client] and, on 200, decodes
// the body into out. It returns the HTTP status (so the caller maps 403/404 to
// its own sentinels) and an error only for transport or decode failures.
func GetJSON(ctx context.Context, token, rawURL string, out any) (int, error) {
	req, err := NewRequest(ctx, http.MethodGet, rawURL, token, nil)
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	resp, err := Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return resp.StatusCode, fmt.Errorf("decode: %w", err)
	}
	return resp.StatusCode, nil
}

// StatusError is a non-2xx Discord answer the caller has no sentinel for,
// carried as data so a classifier upstream (e.g. the provider-health badge)
// can tell a rejected token (401) from Discord being down (5xx) without
// parsing text.
type StatusError struct {
	// Op names the call, e.g. "discordtag: GET /users/@me".
	Op     string
	Status int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s: HTTP %d", e.Op, e.Status)
}
