package web

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// jwtHeader is the pre-encoded JOSE header for HS256.
var jwtHeader = base64URLEncode([]byte(`{"alg":"HS256","typ":"JWT"}`))

// Claims holds the JWT payload for an authenticated user.
type Claims struct {
	Sub      string `json:"sub"`
	TenantID string `json:"tid"`
	Role     string `json:"role"`
	Issuer   string `json:"iss"`
	IssuedAt int64  `json:"iat"`
	Expires  int64  `json:"exp"`
}

// Valid returns true if the claims have not expired.
func (c Claims) Valid() bool {
	return time.Now().Unix() < c.Expires
}

// SignJWT creates a signed JWT for the given claims.
func SignJWT(secret string, claims Claims) (string, error) {
	claims.Issuer = "glyphoxa-manage"
	claims.IssuedAt = time.Now().Unix()
	if claims.Expires == 0 {
		claims.Expires = time.Now().Add(24 * time.Hour).Unix()
	}

	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("web: marshal claims: %w", err)
	}

	payloadEnc := base64URLEncode(payload)
	signingInput := jwtHeader + "." + payloadEnc

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signingInput))
	sig := base64URLEncode(mac.Sum(nil))

	return signingInput + "." + sig, nil
}

// VerifyJWT parses and verifies a JWT, returning the claims if valid.
func VerifyJWT(secret, token string) (*Claims, error) {
	parts := strings.SplitN(token, ".", 3)
	if len(parts) != 3 {
		return nil, fmt.Errorf("web: malformed JWT")
	}

	signingInput := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signingInput))
	expectedSig := base64URLEncode(mac.Sum(nil))

	if !hmac.Equal([]byte(parts[2]), []byte(expectedSig)) {
		return nil, fmt.Errorf("web: invalid JWT signature")
	}

	payload, err := base64URLDecode(parts[1])
	if err != nil {
		return nil, fmt.Errorf("web: decode JWT payload: %w", err)
	}

	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("web: unmarshal JWT claims: %w", err)
	}

	if !claims.Valid() {
		return nil, fmt.Errorf("web: JWT expired")
	}
	return &claims, nil
}

func base64URLEncode(data []byte) string {
	return strings.TrimRight(base64.URLEncoding.EncodeToString(data), "=")
}

func base64URLDecode(s string) ([]byte, error) {
	// Add padding back.
	switch len(s) % 4 {
	case 2:
		s += "=="
	case 3:
		s += "="
	}
	return base64.URLEncoding.DecodeString(s)
}

// discordHTTPClient is used for all outbound requests to the Discord API.
// It enforces a 10-second timeout to prevent slow upstream responses from
// blocking the web service indefinitely.
var discordHTTPClient = &http.Client{
	Timeout: 10 * time.Second,
}

// DiscordOAuthConfig holds Discord OAuth2 configuration.
type DiscordOAuthConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURI  string
}

// DiscordUser represents the user profile returned by the Discord API.
type DiscordUser struct {
	ID            string `json:"id"`
	Username      string `json:"username"`
	GlobalName    string `json:"global_name"`
	Email         string `json:"email"`
	Avatar        string `json:"avatar"`
	Discriminator string `json:"discriminator"`
}

// DisplayName returns the best display name for the Discord user.
func (u DiscordUser) DisplayName() string {
	if u.GlobalName != "" {
		return u.GlobalName
	}
	return u.Username
}

// AvatarURL returns the full URL to the user's avatar.
func (u DiscordUser) AvatarURL() string {
	if u.Avatar == "" {
		return ""
	}
	return fmt.Sprintf("https://cdn.discordapp.com/avatars/%s/%s.png", u.ID, u.Avatar)
}

// GenerateState creates a random state parameter for CSRF protection.
func GenerateState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("web: generate state: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// ExchangeDiscordCode exchanges an authorization code for a Discord access token.
func ExchangeDiscordCode(ctx context.Context, cfg DiscordOAuthConfig, code string) (string, error) {
	data := url.Values{
		"client_id":     {cfg.ClientID},
		"client_secret": {cfg.ClientSecret},
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {cfg.RedirectURI},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://discord.com/api/oauth2/token", strings.NewReader(data.Encode()))
	if err != nil {
		return "", fmt.Errorf("web: create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := discordHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("web: exchange discord code: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("web: discord token exchange returned %d", resp.StatusCode)
	}

	var result struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("web: decode discord token response: %w", err)
	}
	return result.AccessToken, nil
}

// FetchDiscordUser fetches the authenticated user's profile from the Discord API.
func FetchDiscordUser(ctx context.Context, accessToken string) (*DiscordUser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://discord.com/api/users/@me", nil)
	if err != nil {
		return nil, fmt.Errorf("web: create discord user request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := discordHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("web: fetch discord user: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("web: discord user endpoint returned %d", resp.StatusCode)
	}

	var user DiscordUser
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, fmt.Errorf("web: decode discord user: %w", err)
	}
	return &user, nil
}

// --- Google OAuth2 ---

// oauthHTTPClient is shared for all outbound OAuth HTTP calls.
var oauthHTTPClient = &http.Client{
	Timeout: 10 * time.Second,
}

// GoogleOAuthConfig holds Google OAuth2 configuration.
type GoogleOAuthConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURI  string
}

// GoogleUser represents the user profile returned by Google's userinfo endpoint.
type GoogleUser struct {
	Sub        string `json:"sub"`
	Email      string `json:"email"`
	Name       string `json:"name"`
	Picture    string `json:"picture"`
	GivenName  string `json:"given_name"`
	FamilyName string `json:"family_name"`
}

// DisplayName returns the best display name for the Google user.
func (u GoogleUser) DisplayName() string {
	if u.Name != "" {
		return u.Name
	}
	if u.GivenName != "" {
		return u.GivenName
	}
	return u.Email
}

// ExchangeGoogleCode exchanges an authorization code for a Google access token.
func ExchangeGoogleCode(ctx context.Context, cfg GoogleOAuthConfig, code string) (string, error) {
	data := url.Values{
		"client_id":     {cfg.ClientID},
		"client_secret": {cfg.ClientSecret},
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {cfg.RedirectURI},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://oauth2.googleapis.com/token", strings.NewReader(data.Encode()))
	if err != nil {
		return "", fmt.Errorf("web: create google token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("web: exchange google code: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("web: google token exchange returned %d", resp.StatusCode)
	}

	var result struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("web: decode google token response: %w", err)
	}
	return result.AccessToken, nil
}

// FetchGoogleUser fetches the authenticated user's profile from Google's userinfo endpoint.
func FetchGoogleUser(ctx context.Context, accessToken string) (*GoogleUser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.googleapis.com/oauth2/v3/userinfo", nil)
	if err != nil {
		return nil, fmt.Errorf("web: create google user request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("web: fetch google user: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("web: google userinfo endpoint returned %d", resp.StatusCode)
	}

	var user GoogleUser
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, fmt.Errorf("web: decode google user: %w", err)
	}
	return &user, nil
}

// --- GitHub OAuth2 ---

// GitHubOAuthConfig holds GitHub OAuth2 configuration.
type GitHubOAuthConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURI  string
}

// GitHubUser represents the user profile returned by the GitHub API.
type GitHubUser struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	AvatarURL string `json:"avatar_url"`
}

// DisplayName returns the best display name for the GitHub user.
func (u GitHubUser) DisplayName() string {
	if u.Name != "" {
		return u.Name
	}
	return u.Login
}

// GitHubID returns the string representation of the numeric GitHub user ID.
func (u GitHubUser) GitHubID() string {
	return fmt.Sprintf("%d", u.ID)
}

// ExchangeGitHubCode exchanges an authorization code for a GitHub access token.
func ExchangeGitHubCode(ctx context.Context, cfg GitHubOAuthConfig, code string) (string, error) {
	data := url.Values{
		"client_id":     {cfg.ClientID},
		"client_secret": {cfg.ClientSecret},
		"code":          {code},
		"redirect_uri":  {cfg.RedirectURI},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://github.com/login/oauth/access_token", strings.NewReader(data.Encode()))
	if err != nil {
		return "", fmt.Errorf("web: create github token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("web: exchange github code: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("web: github token exchange returned %d", resp.StatusCode)
	}

	var result struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("web: decode github token response: %w", err)
	}
	if result.Error != "" {
		return "", fmt.Errorf("web: github token exchange error: %s", result.Error)
	}
	return result.AccessToken, nil
}

// FetchGitHubUser fetches the authenticated user's profile from the GitHub API.
func FetchGitHubUser(ctx context.Context, accessToken string) (*GitHubUser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		return nil, fmt.Errorf("web: create github user request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("web: fetch github user: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("web: github user endpoint returned %d", resp.StatusCode)
	}

	var user GitHubUser
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, fmt.Errorf("web: decode github user: %w", err)
	}
	return &user, nil
}
