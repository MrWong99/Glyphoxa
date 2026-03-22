package vault

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTransitClient_EncryptDecrypt(t *testing.T) {
	t.Parallel()

	// Fake Vault Transit server.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != "test-token" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		switch {
		case strings.HasSuffix(r.URL.Path, "/encrypt/test-key"):
			var req transitEncryptRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			resp := transitResponse{
				Data: transitResponseData{
					"ciphertext": "vault:v1:encrypted-" + req.Plaintext,
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)

		case strings.HasSuffix(r.URL.Path, "/decrypt/test-key"):
			var req transitDecryptRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			// Reverse our fake encryption.
			pt := strings.TrimPrefix(req.Ciphertext, "vault:v1:encrypted-")
			resp := transitResponse{
				Data: transitResponseData{
					"plaintext": pt,
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)

		case strings.HasSuffix(r.URL.Path, "/sys/health"):
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"initialized": true, "sealed": false}`))

		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	client := NewTransitClient(srv.URL, "test-token", "test-key")
	ctx := context.Background()

	t.Run("encrypt and decrypt round-trip", func(t *testing.T) {
		t.Parallel()
		ct, err := client.Encrypt(ctx, "my-secret-token")
		if err != nil {
			t.Fatalf("Encrypt() error: %v", err)
		}
		if !strings.HasPrefix(ct, vaultCiphertextPrefix) {
			t.Fatalf("Encrypt() = %q, want vault:v1: prefix", ct)
		}

		pt, err := client.Decrypt(ctx, ct)
		if err != nil {
			t.Fatalf("Decrypt() error: %v", err)
		}
		if pt != "my-secret-token" {
			t.Fatalf("Decrypt() = %q, want %q", pt, "my-secret-token")
		}
	})

	t.Run("encrypt empty string", func(t *testing.T) {
		t.Parallel()
		ct, err := client.Encrypt(ctx, "")
		if err != nil {
			t.Fatalf("Encrypt('') error: %v", err)
		}
		if ct != "" {
			t.Fatalf("Encrypt('') = %q, want empty", ct)
		}
	})

	t.Run("decrypt empty string", func(t *testing.T) {
		t.Parallel()
		pt, err := client.Decrypt(ctx, "")
		if err != nil {
			t.Fatalf("Decrypt('') error: %v", err)
		}
		if pt != "" {
			t.Fatalf("Decrypt('') = %q, want empty", pt)
		}
	})

	t.Run("decrypt non-vault ciphertext passes through", func(t *testing.T) {
		t.Parallel()
		pt, err := client.Decrypt(ctx, "plain-token-value")
		if err != nil {
			t.Fatalf("Decrypt() error: %v", err)
		}
		if pt != "plain-token-value" {
			t.Fatalf("Decrypt() = %q, want %q", pt, "plain-token-value")
		}
	})

	t.Run("ping success", func(t *testing.T) {
		t.Parallel()
		if err := client.Ping(ctx); err != nil {
			t.Fatalf("Ping() error: %v", err)
		}
	})
}

func TestTransitClient_GracefulDegradation(t *testing.T) {
	t.Parallel()

	// Vault server that always returns 503.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	client := NewTransitClient(srv.URL, "test-token", "test-key")
	ctx := context.Background()

	t.Run("encrypt returns plaintext on failure", func(t *testing.T) {
		t.Parallel()
		ct, err := client.Encrypt(ctx, "my-token")
		if err != nil {
			t.Fatalf("Encrypt() should not return error in degraded mode: %v", err)
		}
		if ct != "my-token" {
			t.Fatalf("Encrypt() = %q, want %q (plaintext fallback)", ct, "my-token")
		}
	})

	t.Run("decrypt vault ciphertext returns error when disabled", func(t *testing.T) {
		t.Parallel()

		// Create a fresh client that we manually disable.
		c := NewTransitClient(srv.URL, "test-token", "test-key")
		c.mu.Lock()
		c.disabled = true
		c.mu.Unlock()

		_, err := c.Decrypt(ctx, "vault:v1:some-encrypted-data")
		if err == nil {
			t.Fatal("Decrypt() should return error for vault ciphertext when disabled")
		}
	})
}

func TestTransitClient_Unreachable(t *testing.T) {
	t.Parallel()

	// Point at a server that doesn't exist.
	client := NewTransitClient("http://127.0.0.1:1", "test-token", "test-key")
	ctx := context.Background()

	t.Run("encrypt gracefully degrades", func(t *testing.T) {
		t.Parallel()
		ct, err := client.Encrypt(ctx, "my-token")
		if err != nil {
			t.Fatalf("Encrypt() should not error on unreachable: %v", err)
		}
		if ct != "my-token" {
			t.Fatalf("Encrypt() = %q, want plaintext fallback", ct)
		}
	})

	t.Run("ping returns error", func(t *testing.T) {
		t.Parallel()
		if err := client.Ping(ctx); err == nil {
			t.Fatal("Ping() should return error for unreachable vault")
		}
	})
}

func TestNoopEncryptor(t *testing.T) {
	t.Parallel()

	enc := NoopEncryptor{}
	ctx := context.Background()

	t.Run("encrypt passthrough", func(t *testing.T) {
		t.Parallel()
		ct, err := enc.Encrypt(ctx, "token")
		if err != nil {
			t.Fatalf("Encrypt error: %v", err)
		}
		if ct != "token" {
			t.Fatalf("Encrypt = %q, want %q", ct, "token")
		}
	})

	t.Run("decrypt passthrough", func(t *testing.T) {
		t.Parallel()
		pt, err := enc.Decrypt(ctx, "vault:v1:something")
		if err != nil {
			t.Fatalf("Decrypt error: %v", err)
		}
		if pt != "vault:v1:something" {
			t.Fatalf("Decrypt = %q, want %q", pt, "vault:v1:something")
		}
	})
}

func TestTransitClient_WrongToken(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != "correct-token" {
			http.Error(w, `{"errors": ["permission denied"]}`, http.StatusForbidden)
			return
		}
	}))
	t.Cleanup(srv.Close)

	client := NewTransitClient(srv.URL, "wrong-token", "test-key")
	ctx := context.Background()

	// Encrypt should gracefully degrade (return plaintext).
	ct, err := client.Encrypt(ctx, "secret")
	if err != nil {
		t.Fatalf("Encrypt() should not error: %v", err)
	}
	if ct != "secret" {
		t.Fatalf("Encrypt() = %q, want plaintext fallback", ct)
	}
}
