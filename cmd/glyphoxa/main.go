// Command glyphoxa is the Glyphoxa v2 binary. In v1.0 it runs one Mode at a
// time; this MVP slice ships the `voice` mode that joins a Discord voice
// channel and gives one Character NPC a live voice loop (issue #1–#5), plus the
// `migrate` subcommand (ADR-0031) that applies the schema migrations.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/spa"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
	"github.com/MrWong99/Glyphoxa/internal/transcript"
	"github.com/MrWong99/Glyphoxa/internal/web"
	"github.com/MrWong99/Glyphoxa/internal/wirenpc"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
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
	// dispatched before flag parsing. `voice`, `web`, and `all` are the Modes
	// (ADR-0005); the broader root-command surface still belongs to the
	// control-plane task (#6). NOTE: ADR-0005's eventual default Mode is `all`,
	// but the binary defaults `-mode` to `voice` for the MVP slices and migrates
	// the default to `all` with #6 — a recorded choice, not silent drift.
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
	metricsAddr := flag.String("metrics-addr", ":9090", "address for the /metrics + /healthz + /readyz listener (all Modes; kept off the public web API port, ADR-0032); empty disables it")
	webAddr := flag.String("web-addr", ":8080", "address for the web/all-mode Connect RPC API listener (ADR-0039); observability is on -metrics-addr")
	flag.Parse()

	switch *mode {
	case "voice":
		if err := runVoice(log, cfg, *hardcoded, metrics, *metricsAddr); err != nil {
			log.Error("voice mode exited with error", "err", err)
			os.Exit(1)
		}
	case "web":
		if err := runWeb(log, cfg, metrics, *webAddr, *metricsAddr, false); err != nil {
			log.Error("web mode exited with error", "err", err)
			os.Exit(1)
		}
	case "all":
		if err := runWeb(log, cfg, metrics, *webAddr, *metricsAddr, true); err != nil {
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

	// -hardcoded runs with no DB: no pool, and the readiness probe is nil
	// (always-ready — see observe.ReadinessProbe). The default path resolves the
	// DSN and opens ONE pgxpool that serves BOTH the /readyz probe (pool.Ping) and
	// the NPC load inside RunFromDB — the voice node no longer opens a separate
	// standalone readiness handle alongside RunFromDB's own pool (issue #77).
	if hardcoded {
		if metricsAddr != "" {
			observe.NewMetricsServer(metricsAddr, metrics, nil, log).Start(ctx)
		}
		return wirenpc.Run(ctx, cfg)
	}

	dsn := databaseURL()
	if dsn == "" {
		return fmt.Errorf("voice mode loads the NPC from the DB by default; set $GLYPHOXA_DATABASE_URL (or $DATABASE_URL), or pass -hardcoded to use the in-code NPC")
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("voice: open db pool: %w", err)
	}
	defer pool.Close()

	if metricsAddr != "" {
		observe.NewMetricsServer(metricsAddr, metrics, observe.ReadinessProbe(pool.Ping), log).Start(ctx)
	}

	// The BYOK credential cipher (ADR-0004) is best-effort, mirroring runWeb: a
	// self-host voice node with no $GLYPHOXA_SECRET still runs on the seeded "env"
	// placeholder configs (which resolve to the adapters' env vars — today's
	// behavior). A real saved key WITH no cipher is the only failure, and
	// RunFromDB surfaces it clearly (issue #69) rather than silently using ENV.
	cipher, err := appCipher()
	if err != nil {
		log.Warn("provider credential decryption is disabled; the voice loop will "+
			"use env-var API keys unless a saved BYOK key requires $GLYPHOXA_SECRET", "err", err)
		cipher = nil
	}

	return wirenpc.RunFromDB(ctx, cfg, pool, cipher)
}

// runWeb is the web/all-mode entrypoint (ADR-0039). It resolves the required DB
// DSN, opens a pgxpool-backed storage.Store, and runs two listeners until
// SIGINT/SIGTERM: the public Connect API (CampaignService) on webAddr, and the
// metrics + k8s probes (/metrics, /healthz, /readyz) on the separate internal
// metricsAddr — so the actuator endpoints stay off the public API surface.
//
// When withVoice is set (-mode=all) the process drives the voice loop in-process
// via the SessionManager (ADR-0039): the Session screen starts/stops it, the
// loop is not run at boot, and SIGTERM stops both the web tier and any active
// session. A web-only run (withVoice false) still serves SessionService but
// rejects Start — it does not drive the loop. The single Prometheus recorder
// feeds both halves.
func runWeb(log *slog.Logger, cfg wirenpc.Config, metrics *observe.PrometheusRecorder, webAddr, metricsAddr string, withVoice bool) error {
	dsn := databaseURL()
	if dsn == "" {
		return fmt.Errorf("web/all modes require a database; set $GLYPHOXA_DATABASE_URL (or $DATABASE_URL)")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("web: open db pool: %w", err)
	}
	defer pool.Close()
	store := storage.New(pool)

	// The BYOK credential cipher (ADR-0004) is best-effort at boot: without
	// $GLYPHOXA_SECRET the web tier still serves (Configuration reads work), but
	// saving a provider key / Bot token fails loudly (CodeFailedPrecondition) —
	// the #44 keyless-degradation posture, not a hard boot failure.
	cipher, err := appCipher()
	if err != nil {
		log.Warn("provider credential encryption is disabled; saving keys in "+
			"Configuration will fail until $GLYPHOXA_SECRET is set", "err", err)
		cipher = nil
	}

	// Metrics + k8s probes (/metrics, /healthz, /readyz) listen on their OWN port
	// (metricsAddr), separate from the public web API — so they are scrapeable by
	// Prometheus and the kubelet but never exposed on the external API surface.
	// /readyz pings the request pool directly: the web tier owns its pool here
	// (unlike the voice node, whose live pool is unreachable from main, so it
	// needs a standalone handle).
	if metricsAddr != "" {
		observe.NewMetricsServer(metricsAddr, metrics, observe.ReadinessProbe(pool.Ping), log).Start(ctx)
	}

	// Base voice config for manager-driven sessions (ADR-0039): the Session screen
	// starts/stops the live loop in-process via the SessionManager. The base
	// Discord token is the env fallback (the deployment-shared Bot); a saved
	// deployment token (decrypted via cipher) overrides it per-session (#87). The
	// guild and voice channel are sourced per-session from the saved deployment
	// config, not these flags (#72). The credential-bridge keys (#69) are resolved
	// inside RunFromDB. enabled = withVoice: only `all` mode drives the loop — a
	// web-only replica answers GetSession (idle) but rejects Start.
	cfg.Token = os.Getenv("DISCORD_BOT_TOKEN")
	cfg.Logger = log
	cfg.Metrics = metrics
	cfg.StageMetrics = metrics
	// ONE process-wide event bus (issue #73, ADR-0014): set on the base config
	// BEFORE the Manager copies it, so the bus pointer flows through every
	// manager-started session (Manager.base → RunFromDB → connectAndServe) and
	// the SSE relay can subscribe once and observe events across reconnect cycles
	// and sessions. Created here so the same instance feeds both halves.
	eventBus := voiceevent.NewBus()
	cfg.Bus = eventBus
	runner := func(rctx context.Context, c wirenpc.Config) error {
		return wirenpc.RunFromDB(rctx, c, pool, cipher)
	}
	mgr := session.NewManager(store, runner, cfg, cipher, log, withVoice)

	// The SSE transcript relay (issue #73, ADR-0014 Hop-B) subscribes to the
	// process bus once and reads the active session from the manager (Snapshot).
	// The store backs incremental line persistence + replay-on-reload (#74,
	// ADR-0040); the manager finalizes the relay's writer queue on Stop (below).
	relay := transcript.NewRelay(eventBus, mgr, store, log)

	// The web tier serves the auth-guarded Connect API under /api, the Discord
	// OAuth carve-out under /auth (ADR-0015/0016), and the embedded SPA at /
	// (ADR-0013/0039). The SPA handler is the "/" catch-all; ServeMux's
	// longest-prefix match keeps /api/ and /auth/ ahead of it, so only non-API
	// paths (and client-side deep links) reach the SPA fallback.
	srv := web.NewServer(web.Config{
		Addr:   webAddr,
		Mounts: managementMounts(store, cipher, log, mgr, relay),
		Root:   spa.Handler(),
		Logger: log,
	})

	// Sessions are manager-driven (ADR-0039): the loop starts when the Session
	// screen asks, not at boot. Run the web tier until SIGTERM, then stop any
	// active session BEFORE the deferred pool.Close, so a row never stays stuck
	// 'running' and the loop's ended_at write never races a closing pool.
	err = runWebTier(ctx, srv)
	mgr.Shutdown()
	return err
}

// managementMounts wires the auth tier and the management Connect services into
// the web mux (ADR-0015/0016/0039): the interceptor-guarded Connect handlers
// (CampaignService, AuthService, ProviderService) under /api, and the net/http
// Discord OAuth redirect + callback under /auth. The single [auth.Stack] gates
// every Connect service identically — auth (session cookie) → CSRF double-submit
// → tenant pass-through — with AuthService.GetCurrentUser left reachable
// unauthenticated so the SPA can probe the session at boot. Live Discord login
// requires the operator's OAuth app credentials (DISCORD_OAUTH_CLIENT_ID /
// _SECRET / _REDIRECT_URL) — a one-time setup, not code; absent them the gate
// still stands and the API simply has no way in. cipher seals BYOK provider
// keys (ADR-0004); it may be nil (saving keys then fails CodeFailedPrecondition).
// mgr drives the in-process voice loop for SessionService (#72, ADR-0039).
// relay serves the live transcript over SSE + a JSON snapshot (#73, ADR-0014):
// its two plain net/http reads mount OUTSIDE the Connect /api prefix at
// /api/v1/sessions/{id}[/events], each guarded by auth.RequireSession (the
// Connect interceptor chain does not cover them).
func managementMounts(store *storage.Store, cipher *crypto.Cipher, log *slog.Logger, mgr *session.Manager, relay *transcript.Relay) []web.Mount {
	clientID := os.Getenv("DISCORD_OAUTH_CLIENT_ID")
	if clientID == "" {
		log.Warn("Discord OAuth is not configured; login is disabled until " +
			"DISCORD_OAUTH_CLIENT_ID, DISCORD_OAUTH_CLIENT_SECRET and " +
			"DISCORD_OAUTH_REDIRECT_URL are set")
	}
	discord := auth.NewDiscordClient(auth.DiscordConfig{
		ClientID:     clientID,
		ClientSecret: os.Getenv("DISCORD_OAUTH_CLIENT_SECRET"),
		RedirectURL:  os.Getenv("DISCORD_OAUTH_REDIRECT_URL"),
	})
	oauth := auth.NewOAuth(store, discord, "/", log)
	authServer := auth.NewAuthServer(store, log)

	// The store satisfies both Authenticator (AuthenticateSession) and
	// TenantResolver (TenantForUser); GetCurrentUser is the only public procedure.
	stack := auth.NewStack(store, store, managementv1connect.AuthServiceGetCurrentUserProcedure)

	campaignPath, campaignHandler := rpc.NewCampaignServer(store).Handler(stack.HandlerOptions()...)
	authPath, authHandler := authServer.Handler(stack.HandlerOptions()...)
	providerPath, providerHandler := rpc.NewProviderServer(store, cipher, log).Handler(stack.HandlerOptions()...)
	// VoiceService (#70) serves the live provider data the Configuration +
	// Campaign screens render — the ElevenLabs voice catalog + preview, the Groq
	// model allowlist, and the async provider-health signal — all via the
	// decrypted tenant key (ADR-0004 credential bridge). Appended last so the
	// existing mounts keep their order.
	voicePath, voiceHandler := rpc.NewVoiceServer(store, cipher, log).Handler(stack.HandlerOptions()...)
	sessionPath, sessionHandler := rpc.NewSessionServer(mgr, store, log).Handler(stack.HandlerOptions()...)

	return []web.Mount{
		web.APIMount(campaignPath, campaignHandler),
		web.APIMount(authPath, authHandler),
		web.APIMount(providerPath, providerHandler),
		web.APIMount(voicePath, voiceHandler),
		web.APIMount(sessionPath, sessionHandler),
		// The SSE relay + snapshot are PLAIN mounts (not web.APIMount): they want
		// the full /api/v1/... path, not the /api-stripped Connect method path.
		// Go 1.22 method+wildcard patterns keep them off the Connect mounts
		// (/api/glyphoxa.management.v1.*) and the SPA root. auth.RequireSession
		// validates the glyphoxa_session cookie the EventSource/fetch send.
		{Path: "GET /api/v1/sessions/{id}/events", Handler: auth.RequireSession(store, http.HandlerFunc(relay.ServeEvents))},
		{Path: "GET /api/v1/sessions/{id}", Handler: auth.RequireSession(store, http.HandlerFunc(relay.ServeSnapshot))},
		{Path: "/auth/discord/login", Handler: http.HandlerFunc(oauth.Login)},
		{Path: "/auth/discord/callback", Handler: http.HandlerFunc(oauth.Callback)},
	}
}

// runWebTier starts the web API server on ctx and blocks until it has fully shut
// down — Start binds the listener, then Wait returns only after the ctx-triggered
// graceful Shutdown has drained in-flight handlers, so the caller's deferred
// pool.Close runs strictly after the drain. Factored out so the keyless
// default-gate test can boot a fake-handler server and assert clean boot+shutdown
// without Postgres or Discord credentials.
func runWebTier(ctx context.Context, srv *web.Server) error {
	if err := srv.Start(ctx); err != nil {
		return fmt.Errorf("web: start server: %w", err)
	}
	srv.Wait()
	return nil
}
