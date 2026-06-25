package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Discord OAuth2 endpoints (ADR-0016: Discord-only). Overridable on
// DiscordConfig so the token-exchange test points the client at an httptest
// cassette instead of discord.com — no live Discord call in CI.
const (
	defaultAuthorizeURL = "https://discord.com/api/oauth2/authorize"
	defaultTokenURL     = "https://discord.com/api/oauth2/token"
	defaultUserURL      = "https://discord.com/api/users/@me"
	// avatarCDN composes the user's avatar image URL from id + hash.
	avatarCDN = "https://cdn.discordapp.com/avatars/%s/%s.png"
)

// DiscordUser is the authenticated Discord identity returned by Exchange — the
// fields the operator record needs (ADR-0016).
type DiscordUser struct {
	ID         string
	Username   string
	GlobalName string
	// AvatarURL is the composed CDN URL, or "" when the user has no avatar.
	AvatarURL string
}

// DisplayName prefers the Discord global (display) name, falling back to the
// legacy username.
func (d DiscordUser) DisplayName() string {
	if d.GlobalName != "" {
		return d.GlobalName
	}
	return d.Username
}

// DiscordOAuth exchanges an OAuth authorization code for the authenticated
// Discord user. The OAuth callback depends on this interface (not the concrete
// client) so tests drive it with a fake — no live Discord call (ADR-0019 TDD).
type DiscordOAuth interface {
	// AuthCodeURL builds the Discord authorize redirect URL for the login start.
	AuthCodeURL(state string) string
	// Exchange swaps an authorization code for the authenticated Discord user.
	Exchange(ctx context.Context, code string) (DiscordUser, error)
}

// DiscordConfig configures a [DiscordClient]. ClientID/ClientSecret/RedirectURL
// are the operator's registered OAuth app credentials (a one-time setup, not
// code). The *URL fields default to discord.com when empty.
type DiscordConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string

	AuthorizeURL string
	TokenURL     string
	UserURL      string

	HTTPClient *http.Client
}

// DiscordClient is the live DiscordOAuth implementation.
type DiscordClient struct {
	cfg  DiscordConfig
	http *http.Client
}

var _ DiscordOAuth = (*DiscordClient)(nil)

// NewDiscordClient builds a DiscordClient, filling endpoint + HTTP-client
// defaults.
func NewDiscordClient(cfg DiscordConfig) *DiscordClient {
	if cfg.AuthorizeURL == "" {
		cfg.AuthorizeURL = defaultAuthorizeURL
	}
	if cfg.TokenURL == "" {
		cfg.TokenURL = defaultTokenURL
	}
	if cfg.UserURL == "" {
		cfg.UserURL = defaultUserURL
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	return &DiscordClient{cfg: cfg, http: hc}
}

// AuthCodeURL builds the authorize redirect URL: response_type=code, the
// identify scope (enough for the @me identity), and the anti-forgery state.
func (c *DiscordClient) AuthCodeURL(state string) string {
	q := url.Values{}
	q.Set("client_id", c.cfg.ClientID)
	q.Set("redirect_uri", c.cfg.RedirectURL)
	q.Set("response_type", "code")
	q.Set("scope", "identify")
	q.Set("state", state)
	return c.cfg.AuthorizeURL + "?" + q.Encode()
}

// Exchange performs the OAuth code→token→user round trip and composes the
// avatar URL. Errors carry no credential material.
func (c *DiscordClient) Exchange(ctx context.Context, code string) (DiscordUser, error) {
	token, err := c.exchangeCode(ctx, code)
	if err != nil {
		return DiscordUser{}, err
	}
	return c.fetchUser(ctx, token)
}

func (c *DiscordClient) exchangeCode(ctx context.Context, code string) (string, error) {
	form := url.Values{}
	form.Set("client_id", c.cfg.ClientID)
	form.Set("client_secret", c.cfg.ClientSecret)
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", c.cfg.RedirectURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.TokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("auth: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("auth: token request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("auth: token endpoint status %d", resp.StatusCode)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok); err != nil {
		return "", fmt.Errorf("auth: decode token response: %w", err)
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("auth: token endpoint returned no access_token")
	}
	return tok.AccessToken, nil
}

func (c *DiscordClient) fetchUser(ctx context.Context, accessToken string) (DiscordUser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.UserURL, nil)
	if err != nil {
		return DiscordUser{}, fmt.Errorf("auth: build userinfo request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return DiscordUser{}, fmt.Errorf("auth: userinfo request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return DiscordUser{}, fmt.Errorf("auth: userinfo endpoint status %d", resp.StatusCode)
	}
	var raw struct {
		ID         string `json:"id"`
		Username   string `json:"username"`
		GlobalName string `json:"global_name"`
		Avatar     string `json:"avatar"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&raw); err != nil {
		return DiscordUser{}, fmt.Errorf("auth: decode userinfo response: %w", err)
	}
	if raw.ID == "" {
		return DiscordUser{}, fmt.Errorf("auth: userinfo returned no id")
	}
	u := DiscordUser{ID: raw.ID, Username: raw.Username, GlobalName: raw.GlobalName}
	if raw.Avatar != "" {
		u.AvatarURL = fmt.Sprintf(avatarCDN, raw.ID, raw.Avatar)
	}
	return u, nil
}
