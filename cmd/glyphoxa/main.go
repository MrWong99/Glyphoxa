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
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/embedworker"
	"github.com/MrWong99/Glyphoxa/internal/kgfacts"
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/presence"
	"github.com/MrWong99/Glyphoxa/internal/recall"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/spa"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
	"github.com/MrWong99/Glyphoxa/internal/transcript"
	"github.com/MrWong99/Glyphoxa/internal/web"
	"github.com/MrWong99/Glyphoxa/internal/wirenpc"
	"github.com/MrWong99/Glyphoxa/pkg/tool"
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
	// Streaming STT (ADR-0042, issue #180) is an env opt-in shared by voice and
	// all mode; default OFF keeps the batch STT path byte-for-byte.
	cfg.STTStreaming = sttStreaming(os.Getenv)
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
	// Boot preflight (ADR-0041, issue #112): a web/all-Mode Web Instance with no
	// usable login must fail loud, not look healthy. Unless the GLYPHOXA_DEV_MODE
	// opt-out is set, the three DISCORD_OAUTH_* vars AND a non-empty operator
	// allowlist are mandatory; requireWebEnv names every missing one. Run before
	// opening the pool so a mis-configured deploy fails fast. Dev-mode skips this
	// and instead auto-authenticates on a forced loopback bind (below).
	dev := devMode(os.Getenv)
	if !dev {
		if err := requireWebEnv(os.Getenv); err != nil {
			return err
		}
	}

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

	// Boot-time session sweep (ADR-0041 amendment, issue #184): the allowlist
	// gates only NEW logins at the OAuth callback, so sessions issued before the
	// gate existed — or before a snowflake was removed — would stay valid for up
	// to 30 days. The allowlist is parsed at boot, so a restart is exactly when
	// a grant change takes effect: revoke every session whose owner is no longer
	// allowlisted (including leftover GLYPHOXA_DEV_MODE sessions). Dev mode has
	// no allowlist and skips the sweep.
	if !dev {
		allow := auth.ParseOperatorAllowlist(os.Getenv("GLYPHOXA_OPERATOR_IDS"))
		revoked, err := store.RevokeSessionsOutsideAllowlist(ctx, allow.IDs())
		if err != nil {
			return fmt.Errorf("web: revoke sessions outside the operator allowlist: %w", err)
		}
		if revoked > 0 {
			log.Warn("revoked sessions of users not on the operator allowlist (ADR-0041)",
				"count", revoked)
		}
	}

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

	// Standing Discord presence + slash-command surface (#102, ADR-0010
	// amendment): ONE boot-owned gateway client — shared with the voice Manager,
	// never a second connection per Voice Session — that registers /roll against
	// the configured Guild and answers it directly with the built-in Dice Tool,
	// surviving with no Voice Session active. Built only when this Instance drives
	// voice (`all` mode); a web-only replica runs no Bot. Declared at function
	// scope so the shutdown path below can Close it after the Manager drains.
	var pres *presence.Presence
	if withVoice {
		allow := auth.ParseOperatorAllowlist(os.Getenv("GLYPHOXA_OPERATOR_IDS"))
		gate := presence.NewGate(allow, func() string {
			if pres == nil {
				return ""
			}
			return pres.GuildID()
		})
		reg := presence.NewRegistry(gate, log)
		reg.Register(presence.RollCommand(tool.NewDice()))
		pres = presence.New(store, cipher, reg, cfg.Token, log)
		// The voice loop borrows this one client instead of dialing its own per
		// session; set BEFORE the Manager copies cfg into its base config.
		cfg.Client = pres.ClientProvider()
		// Bring the presence up at boot (AC: /roll appears with no Voice Session).
		// Non-fatal: a bad or absent Bot token must not kill the web tier — it
		// stays in the wait-state and the RPC refresher retries on the next save.
		if err := pres.Ensure(ctx); err != nil {
			log.Warn("presence: initial ensure failed; the slash-command surface "+
				"will retry when Discord settings are next saved", "err", err)
		}
	}

	runner := func(rctx context.Context, c wirenpc.Config) error {
		return wirenpc.RunFromDB(rctx, c, pool, cipher)
	}
	mgr := session.NewManager(store, runner, cfg, cipher, log, withVoice)
	// Boot-time reconciliation (#143): close voice_sessions rows a previous run
	// left 'running' (crash / failed end-write). No loop is live yet, so every
	// such row is an orphan; done before the web tier serves so GetSession never
	// reports a dead session as live. Web-only mode skips inside (it owns no
	// rows). A failure here is a broken DB — fail the boot loudly.
	if err := mgr.ReconcileOrphans(ctx); err != nil {
		return fmt.Errorf("web: %w", err)
	}

	// The SSE transcript relay (issue #73, ADR-0014 Hop-B) subscribes to the
	// process bus once and reads the active session from the manager (Snapshot).
	// The store backs incremental line persistence + replay-on-reload (#74,
	// ADR-0040); the manager finalizes the relay's writer queue on Stop (below).
	relay := transcript.NewRelay(eventBus, mgr, store, log)
	// Back-wire the finalizer so Stop drains the relay's writer queue and records
	// the authoritative line_count (#74). Done after the relay exists because the
	// relay needs the manager (Snapshot), so the manager is built first.
	mgr.SetTranscript(relay)

	// The Transcript Chunk writer (#104, ADR-0011) subscribes to the SAME process
	// bus and folds utterances into 3–6-utterance chunks written with embedding
	// NULL (the async embedding pipeline, #116, fills them later); it refreshes the
	// embedding-backlog gauge from the DB after each write. The manager closes its
	// open chunk on Stop / loop exit. This CHUNK grain is independent of the relay's
	// line grain (ADR-0040). Voice-standalone mode does not chunk (same posture as
	// line persistence).
	chunker := transcript.NewChunker(eventBus, mgr, store, metrics, log, transcript.ChunkerConfig{})
	mgr.SetChunkFlusher(chunker)
	// Seed the backlog gauge from the DB at boot so it reads the true count before
	// the first chunk is written (idempotent Set-from-COUNT, ADR-0032). A read
	// failure logs and leaves the gauge at 0 rather than failing the boot.
	if n, err := store.CountUnembeddedChunks(ctx); err != nil {
		log.Warn("seed embedding-backlog gauge", "err", err)
	} else {
		metrics.SetEmbeddingBacklog(n)
	}

	// Resolve the process embeddings provider ONCE and share it across the two
	// consumers (#122): the async backfill worker (#116) drains the NULL-embedding
	// backlog, and the NPC memory recaller (#122) embeds utterances for Hot Context
	// retrieval. Resolving it twice would open two independent clients. A resolve
	// failure (an unsupported provider OR a config-read error) is loud-but-non-fatal
	// for BOTH: the backlog gauge exposes the permanent stall and NPC memory recall
	// stays disabled (AC6 — Agent turns behave exactly as before), rather than
	// crashing the process.
	if provider, model, err := embedworker.ResolveProvider(ctx, store); err != nil {
		log.Error("embeddings provider unavailable; embedding backfill and NPC memory recall disabled", "err", err)
	} else {
		// Backfill worker (#116, ADR-0011): claims chunks written with embedding NULL,
		// embeds their text, and UPDATEs each row — draining the gauge toward zero and
		// making the chunks returnable by embedding-filtered retrieval. It needs only
		// the DB + provider (not the voice loop), so it runs in web AND all mode. It
		// rides the process signal ctx, so SIGTERM stops it and any in-flight provider
		// call aborts with the same context.
		go embedworker.New(store, provider, model, metrics, log, embedworker.Config{}).Run(ctx)

		// NPC memory recall (#122, ADR-0011/0042): one recaller over the shared
		// provider + the process store (ANN retriever, #119) + the session Manager
		// (the active Campaign) + the process bus (STTPartial speculation). Set on the
		// Manager's base voice config so every manager-started session's Agent loops
		// fill the reserved Hot Context memory slot; a slow/unavailable path degrades
		// to no-memory within the turn budget. Bound to the run ctx — AfterFunc drops
		// the bus subscription and stops the speculator on shutdown. In web-only mode
		// the Manager starts no sessions, so the recaller stays dormant (bus idle).
		recaller := recall.New(provider, store, mgr, eventBus, metrics, log, recall.Config{})
		context.AfterFunc(ctx, recaller.Close)
		mgr.SetMemory(recaller)
	}

	// NPC KG-facts recall (#126, ADR-0008): the reserved Hot Context KG-facts slot,
	// filled per turn from the active Campaign's gm-public Nodes. UNCONDITIONAL —
	// OUTSIDE the embeddings-provider branch above — because it needs no embeddings
	// provider, only the process store (an indexed OLTP read) and the session
	// Manager (the active Campaign). Gating it on the provider would silently lose
	// the feature on keyless deployments. It owns no goroutine/subscription, so
	// there is nothing to Close.
	mgr.SetFacts(kgfacts.New(store, mgr, metrics, log, kgfacts.Config{}))

	// The web tier serves the auth-guarded Connect API under /api, the Discord
	// OAuth carve-out under /auth (ADR-0015/0016), and the embedded SPA at /
	// (ADR-0013/0039). The SPA handler is the "/" catch-all; ServeMux's
	// longest-prefix match keeps /api/ and /auth/ ahead of it, so only non-API
	// paths (and client-side deep links) reach the SPA fallback.
	// The presence refresher reconciles the standing gateway after a Discord
	// settings save (#102), so a newly-saved Bot token / Guild registers the
	// slash-command surface without a restart. bg-context so it outlives the RPC
	// request; nil in web-only mode (no presence).
	var presenceRefresh func()
	if pres != nil {
		presenceRefresh = func() {
			if err := pres.Ensure(context.Background()); err != nil {
				log.Warn("presence: refresh after Discord settings save failed; "+
					"will retry on the next save or Voice Session cycle", "err", err)
			}
		}
	}
	mounts := managementMounts(store, cipher, log, mgr, relay, presenceRefresh)
	root := spa.Handler()
	// GLYPHOXA_DEV_MODE opt-out (ADR-0041): seed + auto-authenticate the synthetic
	// operator on every request and pin the bind to loopback, so a dev instance
	// needs no OAuth and a mis-set flag in production is structurally unreachable.
	// Wrapping the mounts + SPA root routes every request through the existing
	// interceptor stack / RequireSession / CSRF gate already authenticated. This
	// replaces the manual DB-session-insert dev flow.
	if dev {
		forced, wrap, err := enableDevMode(ctx, store, store, webAddr, log, time.Now)
		if err != nil {
			return fmt.Errorf("web: %w", err)
		}
		webAddr = forced
		for i := range mounts {
			mounts[i].Handler = wrap(mounts[i].Handler)
		}
		root = wrap(root)
	}
	srv := web.NewServer(web.Config{
		Addr:   webAddr,
		Mounts: mounts,
		Root:   root,
		Logger: log,
	})
	// An open SSE tail never goes idle on its own, so it would stall every
	// graceful shutdown for the full 5s grace period (issue #138). Registering
	// CloseStreams releases the relay's streams the moment Shutdown begins; the
	// browser's EventSource reconnects into the restarted process.
	srv.RegisterOnShutdown(relay.CloseStreams)

	// Sessions are manager-driven (ADR-0039): the loop starts when the Session
	// screen asks, not at boot. Run the web tier until SIGTERM, then stop any
	// active session BEFORE the deferred pool.Close, so a row never stays stuck
	// 'running' and the loop's ended_at write never races a closing pool.
	err = runWebTier(ctx, srv)
	mgr.Shutdown()
	// Close the standing presence AFTER the Manager drains, so a live session
	// releases the shared client before it is torn down (#102).
	if pres != nil {
		pres.Close()
	}
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
func managementMounts(store *storage.Store, cipher *crypto.Cipher, log *slog.Logger, mgr *session.Manager, relay *transcript.Relay, presenceRefresh func()) []web.Mount {
	// OAuth credentials are enforced at boot by requireWebEnv (ADR-0041, issue
	// #112): a non-dev web/all Instance never reaches here without all three set,
	// and GLYPHOXA_DEV_MODE serves an auto-authenticated session that never uses
	// these routes — so the old silent warn-and-continue is gone, replaced by the
	// fatal preflight in runWeb.
	clientID := os.Getenv("DISCORD_OAUTH_CLIENT_ID")
	discord := auth.NewDiscordClient(auth.DiscordConfig{
		ClientID:     clientID,
		ClientSecret: os.Getenv("DISCORD_OAUTH_CLIENT_SECRET"),
		RedirectURL:  os.Getenv("DISCORD_OAUTH_REDIRECT_URL"),
	})
	// GLYPHOXA_OPERATOR_IDS is the mandatory operator allowlist (ADR-0041): the
	// single authorization gate at the OAuth callback. A Discord User whose
	// snowflake is absent is denied a session before any Tenant write.
	allowlist := auth.ParseOperatorAllowlist(os.Getenv("GLYPHOXA_OPERATOR_IDS"))
	oauth := auth.NewOAuth(store, discord, "/", allowlist, log)
	authServer := auth.NewAuthServer(store, log)

	// The store satisfies both Authenticator (AuthenticateSession) and
	// TenantResolver (TenantForUser); GetCurrentUser is the only public procedure.
	stack := auth.NewStack(store, store, managementv1connect.AuthServiceGetCurrentUserProcedure)

	campaignPath, campaignHandler := rpc.NewCampaignServer(store).Handler(stack.HandlerOptions()...)
	authPath, authHandler := authServer.Handler(stack.HandlerOptions()...)
	// VoiceService (#70) serves the live provider data the Configuration +
	// Campaign screens render — the ElevenLabs voice catalog + preview, the Groq
	// model allowlist, and the async provider-health signal — all via the
	// decrypted tenant key (ADR-0004 credential bridge). Its mount stays
	// appended after provider's so the existing mount order is kept.
	voiceSrv := rpc.NewVoiceServer(store, cipher, log)
	// While a session is live, the Discord health check short-circuits to
	// healthy off the manager's Snapshot instead of touching Discord (#150).
	voiceSrv.SetSessions(mgr)
	providerSrv := rpc.NewProviderServer(store, cipher, log)
	// Saving a credential busts the tenant's cached health verdict so the next
	// health call probes with the new key instead of serving a stale Degraded
	// badge for up to the cache TTL (#150).
	providerSrv.SetHealthInvalidator(voiceSrv.InvalidateHealth)
	// Saving Discord settings also reconciles the standing presence so the new
	// token / Guild registers the slash-command surface without a restart (#102).
	if presenceRefresh != nil {
		providerSrv.SetPresenceRefresher(presenceRefresh)
	}
	providerPath, providerHandler := providerSrv.Handler(stack.HandlerOptions()...)
	voicePath, voiceHandler := voiceSrv.Handler(stack.HandlerOptions()...)
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
// graceful Shutdown has returned (issue #138 pinned this: Serve returning is NOT
// the end of the drain), so the caller's mgr.Shutdown and deferred pool.Close
// run after the drain for every handler that finishes within the grace period.
// The guarantee is bounded, not absolute: at the ShutdownGrace deadline the
// drain is abandoned — net/http does not close active connections — so a
// handler slower than the grace can still be running during teardown. Factored
// out so the keyless default-gate test can boot a fake-handler server and assert
// clean boot+shutdown without Postgres or Discord credentials.
func runWebTier(ctx context.Context, srv *web.Server) error {
	if err := srv.Start(ctx); err != nil {
		return fmt.Errorf("web: start server: %w", err)
	}
	srv.Wait()
	return nil
}
