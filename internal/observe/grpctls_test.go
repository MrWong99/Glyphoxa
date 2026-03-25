package observe_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MrWong99/glyphoxa/internal/observe"
)

// generateTestCert creates a self-signed certificate and key in a temp directory,
// returning paths to the PEM files.
func generateTestCert(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	dir := t.TempDir()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{Organization: []string{"Test"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")

	certOut, err := os.Create(certFile)
	if err != nil {
		t.Fatalf("create cert file: %v", err)
	}
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}); err != nil {
		t.Fatalf("encode cert: %v", err)
	}
	certOut.Close()

	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyOut, err := os.Create(keyFile)
	if err != nil {
		t.Fatalf("create key file: %v", err)
	}
	if err := pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}); err != nil {
		t.Fatalf("encode key: %v", err)
	}
	keyOut.Close()

	return certFile, keyFile
}

func TestGRPCServerCredentials_NoEnvVars(t *testing.T) {
	// t.Setenv clears env vars and cannot run in parallel.
	t.Setenv("GLYPHOXA_GRPC_TLS_CERT", "")
	t.Setenv("GLYPHOXA_GRPC_TLS_KEY", "")
	t.Setenv("GLYPHOXA_GRPC_TLS_CA", "")

	opt, err := observe.GRPCServerCredentials()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opt != nil {
		t.Error("expected nil ServerOption when TLS env vars are not set")
	}
}

func TestGRPCServerCredentials_ValidCert(t *testing.T) {
	certFile, keyFile := generateTestCert(t)

	t.Setenv("GLYPHOXA_GRPC_TLS_CERT", certFile)
	t.Setenv("GLYPHOXA_GRPC_TLS_KEY", keyFile)
	t.Setenv("GLYPHOXA_GRPC_TLS_CA", "")

	opt, err := observe.GRPCServerCredentials()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opt == nil {
		t.Fatal("expected non-nil ServerOption with valid TLS cert")
	}
}

func TestGRPCServerCredentials_InvalidPaths(t *testing.T) {
	t.Setenv("GLYPHOXA_GRPC_TLS_CERT", "/nonexistent/cert.pem")
	t.Setenv("GLYPHOXA_GRPC_TLS_KEY", "/nonexistent/key.pem")
	t.Setenv("GLYPHOXA_GRPC_TLS_CA", "")

	_, err := observe.GRPCServerCredentials()
	if err == nil {
		t.Fatal("expected error with invalid cert/key paths")
	}
}

func TestGRPCServerCredentials_InvalidCAPath(t *testing.T) {
	certFile, keyFile := generateTestCert(t)

	t.Setenv("GLYPHOXA_GRPC_TLS_CERT", certFile)
	t.Setenv("GLYPHOXA_GRPC_TLS_KEY", keyFile)
	t.Setenv("GLYPHOXA_GRPC_TLS_CA", "/nonexistent/ca.pem")

	_, err := observe.GRPCServerCredentials()
	if err == nil {
		t.Fatal("expected error with invalid CA path")
	}
}

func TestGRPCServerCredentials_InvalidCAContent(t *testing.T) {
	certFile, keyFile := generateTestCert(t)

	// Create a CA file with invalid content.
	caFile := filepath.Join(t.TempDir(), "bad-ca.pem")
	if err := os.WriteFile(caFile, []byte("not a valid PEM"), 0o644); err != nil {
		t.Fatalf("write bad CA: %v", err)
	}

	t.Setenv("GLYPHOXA_GRPC_TLS_CERT", certFile)
	t.Setenv("GLYPHOXA_GRPC_TLS_KEY", keyFile)
	t.Setenv("GLYPHOXA_GRPC_TLS_CA", caFile)

	_, err := observe.GRPCServerCredentials()
	if err == nil {
		t.Fatal("expected error with invalid CA content")
	}
}

func TestGRPCServerCredentials_mTLS(t *testing.T) {
	certFile, keyFile := generateTestCert(t)

	// Use the same self-signed cert as the CA for mTLS test.
	t.Setenv("GLYPHOXA_GRPC_TLS_CERT", certFile)
	t.Setenv("GLYPHOXA_GRPC_TLS_KEY", keyFile)
	t.Setenv("GLYPHOXA_GRPC_TLS_CA", certFile)

	opt, err := observe.GRPCServerCredentials()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opt == nil {
		t.Fatal("expected non-nil ServerOption with mTLS config")
	}
}

func TestGRPCClientCredentials_NoEnvVars(t *testing.T) {
	t.Setenv("GLYPHOXA_GRPC_TLS_CERT", "")
	t.Setenv("GLYPHOXA_GRPC_TLS_KEY", "")
	t.Setenv("GLYPHOXA_GRPC_TLS_CA", "")

	opt, err := observe.GRPCClientCredentials()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opt == nil {
		t.Fatal("expected non-nil DialOption (insecure fallback)")
	}
}

func TestGRPCClientCredentials_WithCA(t *testing.T) {
	certFile, _ := generateTestCert(t)

	t.Setenv("GLYPHOXA_GRPC_TLS_CERT", "")
	t.Setenv("GLYPHOXA_GRPC_TLS_KEY", "")
	t.Setenv("GLYPHOXA_GRPC_TLS_CA", certFile)

	opt, err := observe.GRPCClientCredentials()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opt == nil {
		t.Fatal("expected non-nil DialOption with CA")
	}
}

func TestGRPCClientCredentials_InvalidCAPath(t *testing.T) {
	t.Setenv("GLYPHOXA_GRPC_TLS_CERT", "")
	t.Setenv("GLYPHOXA_GRPC_TLS_KEY", "")
	t.Setenv("GLYPHOXA_GRPC_TLS_CA", "/nonexistent/ca.pem")

	_, err := observe.GRPCClientCredentials()
	if err == nil {
		t.Fatal("expected error with invalid CA path")
	}
}

func TestGRPCClientCredentials_InvalidCAContent(t *testing.T) {
	caFile := filepath.Join(t.TempDir(), "bad-ca.pem")
	if err := os.WriteFile(caFile, []byte("not valid PEM data"), 0o644); err != nil {
		t.Fatalf("write bad CA: %v", err)
	}

	t.Setenv("GLYPHOXA_GRPC_TLS_CERT", "")
	t.Setenv("GLYPHOXA_GRPC_TLS_KEY", "")
	t.Setenv("GLYPHOXA_GRPC_TLS_CA", caFile)

	_, err := observe.GRPCClientCredentials()
	if err == nil {
		t.Fatal("expected error with invalid CA content")
	}
}

func TestGRPCClientCredentials_mTLS(t *testing.T) {
	certFile, keyFile := generateTestCert(t)

	t.Setenv("GLYPHOXA_GRPC_TLS_CERT", certFile)
	t.Setenv("GLYPHOXA_GRPC_TLS_KEY", keyFile)
	t.Setenv("GLYPHOXA_GRPC_TLS_CA", certFile)

	opt, err := observe.GRPCClientCredentials()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opt == nil {
		t.Fatal("expected non-nil DialOption with mTLS config")
	}
}

func TestGRPCClientCredentials_InvalidCertPath(t *testing.T) {
	certFile, _ := generateTestCert(t)

	t.Setenv("GLYPHOXA_GRPC_TLS_CERT", "/nonexistent/client-cert.pem")
	t.Setenv("GLYPHOXA_GRPC_TLS_KEY", "/nonexistent/client-key.pem")
	t.Setenv("GLYPHOXA_GRPC_TLS_CA", certFile)

	_, err := observe.GRPCClientCredentials()
	if err == nil {
		t.Fatal("expected error with invalid client cert path")
	}
}

func TestGRPCServerCredentials_OnlyCertNoKey(t *testing.T) {
	certFile, _ := generateTestCert(t)

	t.Setenv("GLYPHOXA_GRPC_TLS_CERT", certFile)
	t.Setenv("GLYPHOXA_GRPC_TLS_KEY", "")
	t.Setenv("GLYPHOXA_GRPC_TLS_CA", "")

	// When only cert is set but not key, should return nil (not configured).
	opt, err := observe.GRPCServerCredentials()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opt != nil {
		t.Error("expected nil when only cert is set without key")
	}
}
