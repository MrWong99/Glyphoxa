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
