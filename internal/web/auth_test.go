package web

import (
	"testing"
	"time"
)

func TestSignAndVerifyJWT(t *testing.T) {
	t.Parallel()

	secret := "test-secret-key-for-jwt"
	claims := Claims{
		Sub:      "user-123",
		TenantID: "tenant-abc",
		Role:     "dm",
		Expires:  time.Now().Add(1 * time.Hour).Unix(),
	}

	token, err := SignJWT(secret, claims)
	if err != nil {
		t.Fatalf("SignJWT: %v", err)
	}
	if token == "" {
		t.Fatal("SignJWT returned empty token")
	}

	verified, err := VerifyJWT(secret, token)
	if err != nil {
		t.Fatalf("VerifyJWT: %v", err)
	}
	if verified.Sub != claims.Sub {
		t.Errorf("Sub = %q, want %q", verified.Sub, claims.Sub)
	}
	if verified.TenantID != claims.TenantID {
		t.Errorf("TenantID = %q, want %q", verified.TenantID, claims.TenantID)
	}
	if verified.Role != claims.Role {
		t.Errorf("Role = %q, want %q", verified.Role, claims.Role)
	}
	if verified.Issuer != "glyphoxa-manage" {
		t.Errorf("Issuer = %q, want %q", verified.Issuer, "glyphoxa-manage")
	}
}

func TestVerifyJWT_WrongSecret(t *testing.T) {
	t.Parallel()

	token, err := SignJWT("secret-1", Claims{
		Sub:     "user-1",
		Expires: time.Now().Add(1 * time.Hour).Unix(),
	})
	if err != nil {
		t.Fatalf("SignJWT: %v", err)
	}

	_, err = VerifyJWT("secret-2", token)
	if err == nil {
		t.Fatal("expected error for wrong secret, got nil")
	}
}

func TestVerifyJWT_Expired(t *testing.T) {
	t.Parallel()

	token, err := SignJWT("secret", Claims{
		Sub:     "user-1",
		Expires: time.Now().Add(-1 * time.Hour).Unix(),
	})
	if err != nil {
		t.Fatalf("SignJWT: %v", err)
	}

	_, err = VerifyJWT("secret", token)
	if err == nil {
		t.Fatal("expected error for expired token, got nil")
	}
}

func TestVerifyJWT_Malformed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"no dots", "abcdef"},
		{"one dot", "abc.def"},
		{"garbage", "not.a.jwt"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := VerifyJWT("secret", tt.token)
			if err == nil {
				t.Errorf("expected error for token %q, got nil", tt.token)
			}
		})
	}
}

func TestClaimsValid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		expires int64
		want    bool
	}{
		{"future expiry", time.Now().Add(1 * time.Hour).Unix(), true},
		{"past expiry", time.Now().Add(-1 * time.Hour).Unix(), false},
		{"zero expiry", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := Claims{Sub: "user-1", Expires: tt.expires}
			if got := c.Valid(); got != tt.want {
				t.Errorf("Valid() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDiscordUser_DisplayName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		user     DiscordUser
		wantName string
	}{
		{
			name:     "prefers global name",
			user:     DiscordUser{Username: "user123", GlobalName: "Cool User"},
			wantName: "Cool User",
		},
		{
			name:     "falls back to username",
			user:     DiscordUser{Username: "user123", GlobalName: ""},
			wantName: "user123",
		},
		{
			name:     "both empty",
			user:     DiscordUser{Username: "", GlobalName: ""},
			wantName: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.user.DisplayName(); got != tt.wantName {
				t.Errorf("DisplayName() = %q, want %q", got, tt.wantName)
			}
		})
	}
}

func TestDiscordUser_AvatarURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		user    DiscordUser
		wantURL string
	}{
		{
			name:    "with avatar",
			user:    DiscordUser{ID: "123456", Avatar: "abc123"},
			wantURL: "https://cdn.discordapp.com/avatars/123456/abc123.png",
		},
		{
			name:    "no avatar",
			user:    DiscordUser{ID: "123456", Avatar: ""},
			wantURL: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.user.AvatarURL(); got != tt.wantURL {
				t.Errorf("AvatarURL() = %q, want %q", got, tt.wantURL)
			}
		})
	}
}

func TestSignJWT_DefaultExpiration(t *testing.T) {
	t.Parallel()

	// When Expires is 0, SignJWT should set a default (24h).
	token, err := SignJWT("secret", Claims{Sub: "user-1"})
	if err != nil {
		t.Fatalf("SignJWT: %v", err)
	}

	claims, err := VerifyJWT("secret", token)
	if err != nil {
		t.Fatalf("VerifyJWT: %v", err)
	}

	// Expires should be roughly 24h from now.
	expectedMin := time.Now().Add(23 * time.Hour).Unix()
	expectedMax := time.Now().Add(25 * time.Hour).Unix()
	if claims.Expires < expectedMin || claims.Expires > expectedMax {
		t.Errorf("Expires = %d, want within 24h from now", claims.Expires)
	}
}

func TestSignJWT_SetsIssuerAndIssuedAt(t *testing.T) {
	t.Parallel()

	before := time.Now().Unix()
	token, err := SignJWT("secret", Claims{Sub: "user-1", Expires: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatalf("SignJWT: %v", err)
	}
	after := time.Now().Unix()

	claims, err := VerifyJWT("secret", token)
	if err != nil {
		t.Fatalf("VerifyJWT: %v", err)
	}

	if claims.Issuer != "glyphoxa-manage" {
		t.Errorf("Issuer = %q, want %q", claims.Issuer, "glyphoxa-manage")
	}
	if claims.IssuedAt < before || claims.IssuedAt > after {
		t.Errorf("IssuedAt = %d, want between %d and %d", claims.IssuedAt, before, after)
	}
}

func TestGenerateState(t *testing.T) {
	t.Parallel()

	state1, err := GenerateState()
	if err != nil {
		t.Fatalf("GenerateState: %v", err)
	}
	state2, err := GenerateState()
	if err != nil {
		t.Fatalf("GenerateState: %v", err)
	}

	if state1 == "" {
		t.Fatal("GenerateState returned empty string")
	}
	if state1 == state2 {
		t.Fatal("GenerateState returned duplicate values")
	}
	if len(state1) != 32 { // 16 bytes = 32 hex chars
		t.Errorf("state length = %d, want 32", len(state1))
	}
}
