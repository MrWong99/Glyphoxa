package web

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	pb "github.com/MrWong99/glyphoxa/gen/glyphoxa/v1"
)

// DialGateway establishes a gRPC connection to the gateway's ManagementService.
// If TLS cert/key/CA paths are provided, mutual TLS is used; otherwise the
// connection is plaintext (suitable for development or same-node deployment).
func DialGateway(cfg *Config) (pb.ManagementServiceClient, *grpc.ClientConn, error) {
	if cfg.GatewayGRPCAddr == "" {
		return nil, nil, fmt.Errorf("web: GLYPHOXA_WEB_GATEWAY_GRPC_ADDR is required for gateway communication")
	}

	var opts []grpc.DialOption

	if cfg.GatewayTLSCert != "" {
		tlsCfg, err := buildMTLSConfig(cfg.GatewayTLSCert, cfg.GatewayTLSKey, cfg.GatewayTLSCA)
		if err != nil {
			return nil, nil, fmt.Errorf("web: mTLS setup: %w", err)
		}
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	} else {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	// Send shared secret in gRPC metadata for ManagementService auth.
	if cfg.GatewaySharedSecret != "" {
		secret := cfg.GatewaySharedSecret
		opts = append(opts, grpc.WithUnaryInterceptor(
			func(
				ctx context.Context,
				method string,
				req, reply any,
				cc *grpc.ClientConn,
				invoker grpc.UnaryInvoker,
				callOpts ...grpc.CallOption,
			) error {
				ctx = metadata.AppendToOutgoingContext(ctx, "x-mgmt-secret", secret)
				return invoker(ctx, method, req, reply, cc, callOpts...)
			},
		))
	}

	conn, err := grpc.NewClient(cfg.GatewayGRPCAddr, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("web: dial gateway at %s: %w", cfg.GatewayGRPCAddr, err)
	}

	return pb.NewManagementServiceClient(conn), conn, nil
}

// buildMTLSConfig creates a TLS config for mutual TLS using the given
// client cert, key, and CA certificate files.
func buildMTLSConfig(certPath, keyPath, caPath string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load client cert/key: %w", err)
	}

	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		return nil, fmt.Errorf("read CA cert: %w", err)
	}

	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("failed to parse CA certificate from %s", caPath)
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caPool,
		MinVersion:   tls.VersionTLS12,
	}, nil
}
