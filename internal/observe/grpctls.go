package observe

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"os"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// TLS env var names read by [GRPCServerCredentials] and [GRPCClientCredentials].
const (
	envTLSCert = "GLYPHOXA_GRPC_TLS_CERT"
	envTLSKey  = "GLYPHOXA_GRPC_TLS_KEY"
	envTLSCA   = "GLYPHOXA_GRPC_TLS_CA"
)

// GRPCServerCredentials returns a [grpc.ServerOption] for TLS if the
// GLYPHOXA_GRPC_TLS_CERT and GLYPHOXA_GRPC_TLS_KEY env vars are set.
// When GLYPHOXA_GRPC_TLS_CA is also set, mutual TLS (mTLS) is enabled
// and client certificates are verified against the CA bundle.
//
// Returns nil when TLS env vars are not set (caller should use insecure).
func GRPCServerCredentials() (grpc.ServerOption, error) {
	certFile := os.Getenv(envTLSCert)
	keyFile := os.Getenv(envTLSKey)

	if certFile == "" || keyFile == "" {
		slog.Warn("gRPC TLS not configured — running insecure (set GLYPHOXA_GRPC_TLS_CERT and GLYPHOXA_GRPC_TLS_KEY to enable)")
		return nil, nil
	}

	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("observe: load gRPC TLS cert/key: %w", err)
	}

	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}

	// Optional mTLS: require client certs verified against CA.
	caFile := os.Getenv(envTLSCA)
	if caFile != "" {
		caBytes, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("observe: read gRPC TLS CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caBytes) {
			return nil, fmt.Errorf("observe: no valid certs found in %s", caFile)
		}
		tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
		tlsCfg.ClientCAs = pool
		slog.Info("gRPC mTLS enabled", "cert", certFile, "key", keyFile, "ca", caFile)
	} else {
		slog.Info("gRPC TLS enabled (server-only)", "cert", certFile, "key", keyFile)
	}

	return grpc.Creds(credentials.NewTLS(tlsCfg)), nil
}

// GRPCClientCredentials returns a [grpc.DialOption] for TLS if the
// GLYPHOXA_GRPC_TLS_CA env var is set (server certificate verification).
// When GLYPHOXA_GRPC_TLS_CERT and GLYPHOXA_GRPC_TLS_KEY are also set,
// the client presents a certificate for mTLS.
//
// Falls back to insecure credentials when no TLS env vars are set.
func GRPCClientCredentials() (grpc.DialOption, error) {
	caFile := os.Getenv(envTLSCA)
	certFile := os.Getenv(envTLSCert)
	keyFile := os.Getenv(envTLSKey)

	if caFile == "" && certFile == "" {
		slog.Warn("gRPC client TLS not configured — connecting insecure")
		return grpc.WithTransportCredentials(insecure.NewCredentials()), nil
	}

	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}

	// Server CA verification.
	if caFile != "" {
		caBytes, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("observe: read gRPC client CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caBytes) {
			return nil, fmt.Errorf("observe: no valid certs found in %s", caFile)
		}
		tlsCfg.RootCAs = pool
	}

	// Optional client certificate for mTLS.
	if certFile != "" && keyFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("observe: load gRPC client cert/key: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
		slog.Info("gRPC client mTLS enabled", "cert", certFile, "ca", caFile)
	} else {
		slog.Info("gRPC client TLS enabled (server verification only)", "ca", caFile)
	}

	return grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)), nil
}
