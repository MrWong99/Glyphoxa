package vault

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPKIClient_IssueCert(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != "pki-token" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/pki/issue/glyphoxa-grpc") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		var reqBody map[string]any
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		cn, _ := reqBody["common_name"].(string)
		if cn == "" {
			http.Error(w, "common_name required", http.StatusBadRequest)
			return
		}

		resp := map[string]any{
			"data": map[string]any{
				"certificate": "-----BEGIN CERTIFICATE-----\nfake-cert-for-" + cn + "\n-----END CERTIFICATE-----",
				"private_key": "-----BEGIN RSA PRIVATE KEY-----\nfake-key\n-----END RSA PRIVATE KEY-----",
				"ca_chain": []string{
					"-----BEGIN CERTIFICATE-----\nfake-ca\n-----END CERTIFICATE-----",
				},
				"expiration": time.Now().Add(24 * time.Hour).Unix(),
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)

	client := NewPKIClient(srv.URL, "pki-token", "glyphoxa-grpc")
	ctx := context.Background()

	t.Run("issue certificate", func(t *testing.T) {
		t.Parallel()
		bundle, err := client.IssueCert(ctx, "gateway.glyphoxa.svc", nil, 24*time.Hour)
		if err != nil {
			t.Fatalf("IssueCert() error: %v", err)
		}
		if !strings.Contains(bundle.Certificate, "fake-cert-for-gateway.glyphoxa.svc") {
			t.Fatalf("IssueCert() cert = %q, want to contain common name", bundle.Certificate)
		}
		if bundle.PrivateKey == "" {
			t.Fatal("IssueCert() private key is empty")
		}
		if bundle.CAChain == "" {
			t.Fatal("IssueCert() CA chain is empty")
		}
		if bundle.ExpiresAt().Before(time.Now()) {
			t.Fatal("IssueCert() certificate already expired")
		}
	})

	t.Run("issue with SANs", func(t *testing.T) {
		t.Parallel()
		bundle, err := client.IssueCert(ctx, "gateway.glyphoxa.svc",
			[]string{"worker.glyphoxa.svc", "localhost"}, 1*time.Hour)
		if err != nil {
			t.Fatalf("IssueCert() error: %v", err)
		}
		if bundle.Certificate == "" {
			t.Fatal("IssueCert() certificate is empty")
		}
	})
}

func TestCertBundle_WriteToDisk(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	bundle := CertBundle{
		Certificate: "-----BEGIN CERTIFICATE-----\ntest-cert\n-----END CERTIFICATE-----",
		PrivateKey:  "-----BEGIN RSA PRIVATE KEY-----\ntest-key\n-----END RSA PRIVATE KEY-----",
		CAChain:     "-----BEGIN CERTIFICATE-----\ntest-ca\n-----END CERTIFICATE-----",
		Expiration:  time.Now().Add(24 * time.Hour).Unix(),
	}

	certPath, keyPath, caPath, err := bundle.WriteToDisk(dir)
	if err != nil {
		t.Fatalf("WriteToDisk() error: %v", err)
	}

	// Verify files exist and have correct content.
	for _, tc := range []struct {
		path    string
		content string
	}{
		{certPath, bundle.Certificate},
		{keyPath, bundle.PrivateKey},
		{caPath, bundle.CAChain},
	} {
		got, err := os.ReadFile(tc.path)
		if err != nil {
			t.Fatalf("ReadFile(%q) error: %v", tc.path, err)
		}
		if string(got) != tc.content {
			t.Fatalf("File %q content = %q, want %q", tc.path, string(got), tc.content)
		}

		// Verify restrictive permissions.
		info, err := os.Stat(tc.path)
		if err != nil {
			t.Fatalf("Stat(%q) error: %v", tc.path, err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("File %q permissions = %o, want 0600", tc.path, info.Mode().Perm())
		}
	}

	// Verify paths are in the expected directory.
	if filepath.Dir(certPath) != dir {
		t.Fatalf("certPath dir = %q, want %q", filepath.Dir(certPath), dir)
	}
}

func TestPKIClient_IssueCert_VaultError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"errors": ["role not found"]}`, http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)

	client := NewPKIClient(srv.URL, "token", "nonexistent-role")
	ctx := context.Background()

	_, err := client.IssueCert(ctx, "test.local", nil, time.Hour)
	if err == nil {
		t.Fatal("IssueCert() should return error for Vault error response")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Fatalf("IssueCert() error = %v, want to mention status code", err)
	}
}
