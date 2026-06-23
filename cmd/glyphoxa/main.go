// Command glyphoxa is the Glyphoxa v2 binary. In v1.0 it runs one Mode at a
// time; this MVP slice ships the `voice` mode that joins a Discord voice
// channel and gives one Character NPC a live voice loop (issue #1–#5), plus the
// `migrate` subcommand (ADR-0031) that applies the schema migrations.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	// pgx stdlib driver: the /readyz probe pings through a database/sql handle
	// (issue #33). Registered here as well as in migrate.go so this file's use of
	// sql.Open("pgx", …) is self-documenting; the blank import is idempotent.
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/web"
	"github.com/MrWong99/Glyphoxa/internal/wirenpc"
)

func main() {
	// The Prometheus adapter is built first so its DAVE-decrypt counter hook can
	// feed the slog filter: A1 suppresses the benign disgo noise from the console
	// but preserves the information as glyphoxa_voice_dave_decrypt_errors_total
	// (observability.md §1 — "nothing is actually lost").
	metrics := observe.NewPrometheusRecorder()

	// ADR-0032: mode-selected handler (JSON prod / text dev) replacing the old
	// hardcoded TextHandler/Info, with the disgo DAVE-decrypt noise filtered (A1).
	// slog.SetDefault routes ANY library on the default logger — not just disgo's
	// bot logger — through the same handler (observability.md §1.5).
	format := observe.ParseLogFormat(os.Getenv("GLYPHOXA_LOG_FORMAT"))
	log := observe.NewLogger(os.Stderr, format, slog.LevelInfo, metrics.DAVEDecryptHook())
	slog.SetDefault(log)

	// `migrate` and `seed` are subcommands with their own argument grammar,
	// dispatched before flag parsing. The full Mode dispatcher (all/web) and
	// root command surface belong to the control-plane task (#6); this slice
	// wires `migrate` (ADR-0031), `seed` (task #5), and the `voice` mode.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "migrate":
			if err := RunMigrate(context.Background(), os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		case "seed":
			if err := RunSeed(context.Background(), log, os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		}
	}

	mode := flag.String("mode", "voice", "process mode: voice|web|all")
	var cfg wirenpc.Config
	flag.StringVar(&cfg.Guild, "guild", "", "Discord guild (server) snowflake ID")
	flag.StringVar(&cfg.Channel, "channel", "", "Discord voice channel snowflake ID")
	hardcoded := flag.Bool("hardcoded", false, "use the in-code NPC instead of loading from the database — no Postgres needed, for smoke-testing audio without a seeded DB")
	metricsAddr := flag.String("metrics-addr", ":9090", "address for the voice-mode /metrics listener (ADR-0032); empty disables it")
	webAddr := flag.String("web-addr", ":8080", "address for the web/all-mode HTTP listener (Connect RPC + /metrics + probes, ADR-0039)")
	flag.Parse()

	switch *mode {
	case "voice":
		if err := runVoice(log, cfg, *hardcoded, metrics, *metricsAddr); err != nil {
			log.Error("voice mode exited with error", "err", err)
			os.Exit(1)
		}
	case "web":
		if err := runWeb(log, cfg, metrics, *webAddr, false); err != nil {
			log.Error("web mode exited with error", "err", err)
			os.Exit(1)
		}
	case "all":
		if err := runWeb(log, cfg, metrics, *webAddr, true); err != nil {
			log.Error("all mode exited with error", "err", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown mode %q (one of voice|web|all)\n", *mode)
		os.Exit(2)
	}
}

// runVoice resolves runtime credentials from the environment, builds the live
// NPC voice loop, and runs it until SIGINT/SIGTERM. Credentials are never
// compiled in: DISCORD_BOT_TOKEN, plus the provider keys the STT/TTS/LLM
// adapters read from their own env vars / keyring (the encrypted provider_config
// credential is the web-app BYOK path, not the self-host voice path).
//
// By default the NPC's Persona/Voice/identity load from Postgres
// ($GLYPHOXA_DATABASE_URL) via the task-#5 path. The -hardcoded escape hatch
// uses the in-code NPC instead, so audio can be smoke-tested without a seeded DB.
//
// metrics is the process Prometheus adapter; when metricsAddr is non-empty a
// metrics-only /metrics listener is served for its lifetime (ADR-0032 §2.3,
// voice mode). The single adapter satisfies both recorder interfaces, so it
// drives the hot-path plumbing counters (Config.Metrics → Manager) AND the
// orchestrator stage/turn latency + provider series (Config.StageMetrics →
// buildConversation: the bus subscriber + the agenttool provider adapter).
func runVoice(log *slog.Logger, cfg wirenpc.Config, hardcoded bool, metrics *observe.PrometheusRecorder, metricsAddr string) error {
	cfg.Token = os.Getenv("DISCORD_BOT_TOKEN")
	if cfg.Token == "" {
		return fmt.Errorf("DISCORD_BOT_TOKEN is not set")
	}
	if cfg.Guild == "" || cfg.Channel == "" {
		return fmt.Errorf("-guild and -channel are required for voice mode")
	}
	cfg.Logger = log
	// Inject the recorder into the pipeline; without this the live Manager + stage
	// recorders get the nil zero-value and every glyphoxa_voice_* series stays
	// empty except the DAVE counter and the process collectors.
	cfg.Metrics = metrics
	cfg.StageMetrics = metrics

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Resolve the DB DSN once: it both gates the load path below and, when
	// present, backs the /readyz probe (issue #33). -hardcoded runs with no DB,
	// so its readiness probe is nil (always-ready — see observe.ReadinessProbe).
	dsn := ""
	if !hardcoded {
		dsn = databaseURL()
		if dsn == "" {
			return fmt.Errorf("voice mode loads the NPC from the DB by default; set $GLYPHOXA_DATABASE_URL (or $DATABASE_URL), or pass -hardcoded to use the in-code NPC")
		}
	}

	if metricsAddr != "" {
		var ready observe.ReadinessProbe
		if dsn != "" {
			// The live pgxpool is opened later inside wirenpc.RunFromDB and isn't
			// reachable here, so /readyz pings through a small standalone
			// database/sql handle (pgx stdlib driver, already a dep — see
			// migrate.go). It lives for the metrics server's lifetime alongside the
			// voice loop. NOTE: this ping-handle may later be consolidated with the
			// schema-check handle from the wirenpc boot path (#32) once both merge.
			db, err := sql.Open("pgx", dsn)
			if err != nil {
				return fmt.Errorf("voice: open readiness-probe db handle: %w", err)
			}
			defer db.Close()
			ready = db.PingContext
		}
		observe.NewMetricsServer(metricsAddr, metrics, ready, log).Start(ctx)
	}

	if hardcoded {
		return wirenpc.Run(ctx, cfg)
	}
	return wirenpc.RunFromDB(ctx, cfg, dsn)
}

// runWeb is the web/all-mode entrypoint (ADR-0039). It resolves the required DB
// DSN, opens a pgxpool-backed storage.Store, builds the web tier (Connect
// CampaignService + /metrics + probes on one cleartext h2c port), and runs it
// until SIGINT/SIGTERM. metrics is mounted on the web mux — no standalone
// MetricsServer in this mode (ADR-0032).
//
// When withVoice is set (-mode=all) AND the Discord credentials are present
// (DISCORD_BOT_TOKEN + -guild + -channel), the existing env-cred voice loop runs
// concurrently under the same context, so SIGTERM stops both. Missing creds
// degrade to web-only with a warning rather than failing (matches the #44
// resilience posture); the single Prometheus recorder feeds both halves.
func runWeb(log *slog.Logger, cfg wirenpc.Config, metrics *observe.PrometheusRecorder, webAddr string, withVoice bool) error {
	dsn := databaseURL()
	if dsn == "" {
		return fmt.Errorf("web/all mode require a database; set $GLYPHOXA_DATABASE_URL (or $DATABASE_URL)")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("web: open db pool: %w", err)
	}
	defer pool.Close()
	store := storage.New(pool)

	// /readyz pings through a standalone database/sql handle (pgx stdlib driver,
	// already a dep — see migrate.go); the pgxpool above is the request path, this
	// is the probe path, mirroring runVoice.
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("web: open readiness-probe db handle: %w", err)
	}
	defer db.Close()

	mountPath, mountHandler := rpc.NewCampaignServer(store).Handler()
	srv := web.NewServer(web.Config{
		Addr:     webAddr,
		Mounts:   []web.Mount{{Path: mountPath, Handler: mountHandler}},
		Recorder: metrics,
		Ready:    observe.ReadinessProbe(db.PingContext),
		Logger:   log,
	})

	// Without the voice half, runWebTier blocks on the web server alone.
	if !withVoice {
		return runWebTier(ctx, srv)
	}

	// all-mode: the voice loop only joins when the Discord credentials are
	// present. Resolving the token here (not in wirenpc) keeps the no-creds
	// fallback a local decision: web-only is a healthy all-mode run, not an error.
	cfg.Token = os.Getenv("DISCORD_BOT_TOKEN")
	voiceEnabled := cfg.Token != "" && cfg.Guild != "" && cfg.Channel != ""
	if !voiceEnabled {
		log.Warn("voice disabled: set DISCORD_BOT_TOKEN, -guild, -channel to enable")
		return runWebTier(ctx, srv)
	}

	cfg.Logger = log
	cfg.Metrics = metrics
	cfg.StageMetrics = metrics

	// errgroup ties the two halves to one context: the first to fail cancels the
	// other, and SIGTERM (via ctx) stops both. A clean shutdown of either returns
	// nil, so the process exits non-zero only on a real error.
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return runWebTier(gctx, srv) })
	g.Go(func() error { return wirenpc.RunFromDB(gctx, cfg, dsn) })
	return g.Wait()
}

// runWebTier starts the web server on ctx and blocks until ctx is cancelled,
// returning any bind error from Start. Factored out so the keyless default-gate
// test can boot a fake-handler server and assert clean boot+shutdown without
// Postgres or Discord credentials.
func runWebTier(ctx context.Context, srv *web.Server) error {
	if err := srv.Start(ctx); err != nil {
		return fmt.Errorf("web: start server: %w", err)
	}
	<-ctx.Done()
	return nil
}
