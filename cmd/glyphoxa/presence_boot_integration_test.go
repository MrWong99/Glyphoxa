//go:build integration

package main

import (
	"context"
	"database/sql"
	"log/slog"
	"net"
	"net/http"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/wirenpc"
)

// TestRunWebBindsWhileThePresenceSeedHangs (#642): the REAL runWeb, in all mode
// (withVoice, so the standing-client registry is built and seeded), must bind
// its web listener while the presence seed is stuck. The seed is held by an
// ACCESS EXCLUSIVE lock on deployment_config, so its ListDeploymentConfigs
// blocks for as long as the test wants — the same shape as a seed stuck on an
// unreachable Discord. Boot used to run that seed synchronously before the
// listener, so /readyz stayed down for the whole outage.
func TestRunWebBindsWhileThePresenceSeedHangs(t *testing.T) {
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, bootPGImage,
		tcpostgres.WithDatabase("glyphoxa_test"),
		tcpostgres.WithUsername("glyphoxa"),
		tcpostgres.WithPassword("glyphoxa"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Skipf("SKIPPED DB TEST — NO POSTGRES: could not start %s (is Docker running?): %v", bootPGImage, err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(container) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := storage.MigrateUp(ctx, db); err != nil {
		t.Fatalf("migrate up: %v", err)
	}

	// Hold the seed: an open transaction with an exclusive lock on the table it reads.
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if _, err := tx.Exec(ctx, `LOCK TABLE deployment_config IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatalf("lock deployment_config: %v", err)
	}

	t.Setenv("GLYPHOXA_DEV_MODE", "1")
	t.Setenv("GLYPHOXA_DATABASE_URL", dsn)
	t.Setenv("DATABASE_URL", "")
	t.Setenv("GLYPHOXA_SECRET", "0123456789abcdef0123456789abcdef")

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick a port: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	done := make(chan error, 1)
	go func() {
		done <- runWeb(slog.New(slog.DiscardHandler), wirenpc.Config{}, observe.NewPrometheusRecorder(), addr, "", true)
	}()

	deadline := time.Now().Add(60 * time.Second)
	for {
		select {
		case err := <-done:
			t.Fatalf("runWeb returned before serving: %v", err)
		default:
		}
		resp, err := http.Get("http://" + addr + "/")
		if err == nil {
			_ = resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the web listener never bound while the presence seed was held; boot is waiting on the seed")
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Shut runWeb down the way a pod is: SIGTERM, which its NotifyContext catches.
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("signal: %v", err)
	}
	select {
	case err := <-done:
		// Shutting down promptly is what this test pins, not how runWeb reports it.
		t.Logf("runWeb returned after SIGTERM: %v", err)
	case <-time.After(60 * time.Second):
		t.Fatal("runWeb did not shut down after SIGTERM")
	}
}
