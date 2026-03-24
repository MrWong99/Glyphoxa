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

	resp, err := http.DefaultClient.Do(req)
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

	resp, err := http.DefaultClient.Do(req)
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
