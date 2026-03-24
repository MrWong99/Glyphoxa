// Command glyphoxa-web runs the Glyphoxa Web Management Service.
//
// The service provides user authentication (Discord OAuth2), campaign and NPC
// management, session/transcript viewing, usage queries, and tenant
// administration via gRPC to the gateway. It is a standalone binary that
// shares the same PostgreSQL database as the Glyphoxa gateway.
//
// Configuration is via environment variables:
//
//	GLYPHOXA_WEB_DATABASE_DSN            — PostgreSQL connection string (required)
//	GLYPHOXA_WEB_JWT_SECRET              — HMAC key for JWT signing (required)
//	GLYPHOXA_WEB_DISCORD_CLIENT_ID       — Discord OAuth2 client ID (required)
//	GLYPHOXA_WEB_DISCORD_CLIENT_SECRET   — Discord OAuth2 client secret (required)
//	GLYPHOXA_WEB_DISCORD_REDIRECT_URI    — OAuth2 callback URL (required)
//	GLYPHOXA_WEB_GATEWAY_GRPC_ADDR       — Gateway gRPC address (e.g. "gateway:50051")
//	GLYPHOXA_WEB_GATEWAY_TLS_CERT        — Client TLS cert for mTLS (optional)
//	GLYPHOXA_WEB_GATEWAY_TLS_KEY         — Client TLS key for mTLS (optional)
//	GLYPHOXA_WEB_GATEWAY_TLS_CA          — CA cert for verifying gateway (optional)
//	GLYPHOXA_WEB_LISTEN_ADDR             — Listen address (default :8090)
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"

	pb "github.com/MrWong99/glyphoxa/gen/glyphoxa/v1"
	"github.com/MrWong99/glyphoxa/internal/agent/npcstore"
	"github.com/MrWong99/glyphoxa/internal/web"
)

func main() {
	if err := run(); err != nil {
		slog.Error("glyphoxa-web: fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	cfg, err := web.LoadConfig()
	if err != nil {
		return err
	}

	slog.Info("glyphoxa-web: starting",
		"listen_addr", cfg.ListenAddr,
		"gateway_grpc_addr", cfg.GatewayGRPCAddr,
	)

	// Connect to PostgreSQL.
	pool, err := pgxpool.New(ctx, cfg.DatabaseDSN)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		return err
	}
	slog.Info("glyphoxa-web: database connected")

	// Initialize store (runs migrations).
	store, err := web.NewStore(ctx, pool)
	if err != nil {
		return err
	}

	// Initialize NPC store (reuses existing npcstore package).
	npcs := npcstore.NewPostgresStore(pool)
	if err := npcs.Migrate(ctx); err != nil {
		return err
	}

	// Server options.
	// Connect to gateway gRPC if configured.
	var gwClient pb.ManagementServiceClient
	if cfg.GatewayGRPCAddr != "" {
		var gwConn *grpc.ClientConn
		gwClient, gwConn, err = web.DialGateway(cfg)
		if err != nil {
			return err
		}
		defer gwConn.Close()
		slog.Info("glyphoxa-web: gateway gRPC connected", "addr", cfg.GatewayGRPCAddr)
	} else {
		slog.Warn("glyphoxa-web: no gateway gRPC addr — tenant/session ops disabled")
	}

	srv := web.NewServer(cfg, store, npcs, gwClient)

	httpServer := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      srv.Handler(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start server in background.
	errCh := make(chan error, 1)
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	slog.Info("glyphoxa-web: listening", "addr", cfg.ListenAddr)

	// Wait for shutdown signal or server error.
	select {
	case <-ctx.Done():
		slog.Info("glyphoxa-web: shutting down")
	case err := <-errCh:
		return err
	}

	// Graceful shutdown with 10-second deadline.
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutCancel()

	if err := httpServer.Shutdown(shutCtx); err != nil {
		return err
	}

	slog.Info("glyphoxa-web: stopped")
	return nil
}
