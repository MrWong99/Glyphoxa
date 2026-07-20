// Command glyphoxa is the Glyphoxa v2 binary. In v1.0 it runs one Mode at a
// time; this MVP slice ships the `voice` mode that joins a Discord voice
// channel and gives one Character NPC a live voice loop (issue #1–#5), plus the
// `migrate` subcommand (ADR-0031) that applies the schema migrations.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/disgoorg/snowflake/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/assist"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/blob"
	"github.com/MrWong99/Glyphoxa/internal/bundle"
	"github.com/MrWong99/Glyphoxa/internal/embedworker"
	"github.com/MrWong99/Glyphoxa/internal/highlight"
	"github.com/MrWong99/Glyphoxa/internal/imagegen"
	"github.com/MrWong99/Glyphoxa/internal/jobs"
	"github.com/MrWong99/Glyphoxa/internal/kgfacts"
	"github.com/MrWong99/Glyphoxa/internal/knowledge"
	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/internal/mixdown"
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/presence"
	"github.com/MrWong99/Glyphoxa/internal/recall"
	"github.com/MrWong99/Glyphoxa/internal/recap"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/spa"
	"github.com/MrWong99/Glyphoxa/internal/speaker"
	"github.com/MrWong99/Glyphoxa/internal/spend"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
	"github.com/MrWong99/Glyphoxa/internal/transcript"
	"github.com/MrWong99/Glyphoxa/internal/web"
	"github.com/MrWong99/Glyphoxa/internal/wirenpc"
	"github.com/MrWong99/Glyphoxa/pkg/tool"
	"github.com/MrWong99/Glyphoxa/pkg/voice/embeddings"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// defaultMode is the process Mode when no -mode flag is given. Per ADR-0005 and
// ADR-0034 the self-host default is `all` (web + in-process voice) with startup
// auto-migrate: the operator's whole story is "point it at Postgres + provider
// keys, start it." An explicit -mode voice|web still overrides it (issue #282).
const defaultMode = "all"

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

	// Gateway IDENTIFY-budget observer (#486): registers the identify/resume
	// counters on the metrics registry and warns when one bot application's 24h
	// IDENTIFYs cross the configured threshold (default 500, below Discord's
	// 1000/token/24h token-reset limit). Attached to the standing presence client
	// and to per-cycle voice clients below.
	gatewayBudget := observe.NewGatewayBudget(metrics.Registry(), gatewayIdentifyWarnThreshold(os.Getenv), log)

	// `migrate` and `seed` are subcommands with their own argument grammar,
	// dispatched before flag parsing. `voice`, `web`, and `all` are the Modes
	// (ADR-0005). The default Mode is now `all` (ADR-0005/ADR-0034 self-host
	// target, issue #282): a bare `glyphoxa` boots web + the in-process voice
	// loop with startup auto-migrate. An explicit -mode voice|web still wins, and
	// voice mode continues to demand -guild/-channel (requireVoiceTarget).
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
		case "export":
			if err := RunExport(context.Background(), os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		case "billing":
			if err := RunBilling(context.Background(), os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		case "user":
			if err := RunUser(context.Background(), os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		}
	}

	mode := flag.String("mode", defaultMode, "process mode: voice|web|all")
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

	// Instrument per-cycle (owned) voice clients with the IDENTIFY-budget observer
	// (#486). In `all` mode the voice loop borrows the standing presence client
	// (instrumented in runWeb), so this only bites in standalone `voice` mode where
	// each cycle dials its own client; the borrow path leaves it unused.
	cfg.GatewayBudget = gatewayBudget

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
	// #491 (ADR-0057): -mode voice WITHOUT -guild/-channel and WITH a database is
	// the claim-plane worker — DB-driven assignment, no static target flags. WITH
	// -guild/-channel (or -hardcoded) it stays the legacy standalone node below,
	// byte-for-byte unchanged.
	if !hardcoded && cfg.Guild == "" && cfg.Channel == "" && databaseURL() != "" {
		return runVoiceWorker(log, cfg, metrics, metricsAddr)
	}

	cfg.Token = os.Getenv("DISCORD_BOT_TOKEN")
	if cfg.Token == "" {
		return fmt.Errorf("DISCORD_BOT_TOKEN is not set")
	}
	if err := requireVoiceTarget(cfg); err != nil {
		return err
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

	// ADR-0031 fail-fast: verify the schema is current BEFORE any other DB query.
	// The campaign resolution below is now the first query this entrypoint makes, so
	// the stale-schema guard has to run ahead of it — otherwise a fresh, unmigrated
	// DB yields a raw "relation campaign does not exist" instead of the actionable
	// `migrate up` message. RunFromDB re-checks (it is the invariant for all its
	// callers, incl. the in-proc all-mode runner), but this early check keeps the
	// ordering correct for the standalone path (#323).
	if err := wirenpc.EnsureSchemaCurrent(ctx, dsn); err != nil {
		return err
	}

	// Resolve the campaign this standalone voice node voices BEFORE handing off to
	// RunFromDB (#323): the loop is campaign-scoped now, so it needs the bound
	// Active Campaign in cfg.CampaignID. This node has no logged-in operator ctx, so
	// it applies the same durable→recent policy the web tier's resolveActiveCampaign
	// uses (minus the live-session step, which no standalone boot has): the sole
	// operator's /glyphoxa use selection first, else the most-recently-created
	// campaign — so the standalone node and all-mode voice the SAME campaign. A
	// fresh, empty DB fails loudly with an actionable message.
	st := storage.New(pool)
	active, err := resolveStandaloneCampaign(ctx, st)
	if err != nil {
		return err
	}
	cfg.CampaignID = active.ID

	// Butler GM-only voice-address gate (#280, ADR-0024): arm the previously
	// nil/fail-open standalone gate from the shared GM identity (ADR-0055) —
	// see [armVoiceGMGate]. Note one deliberate side effect of arming: an
	// UNATTRIBUTED (empty-SpeakerID) utterance naming the Butler no longer
	// routes — the armed gate fails closed on an empty SpeakerID (ADR-0024),
	// where the old nil gate let it through.
	cfg.GMSpeaker = armVoiceGMGate(ctx, st, os.Getenv, log)

	// Standalone voice mode wires no knowledge-Tool sources (cfg.ToolDeps stays
	// zero): the transcript_search / kg_query built-ins are still registered and
	// grantable, but a call reports "unavailable in this mode" rather than reading
	// the DB (#296). Only the web/all boot builds the adapter (over the session
	// Manager). Log it once so an operator who granted an NPC a knowledge Tool and
	// runs a pure `-mode voice` node knows why it stays silent.
	log.Info("standalone voice mode: knowledge Tools (transcript_search, kg_query) are unavailable; run -mode all or web to enable them")

	return wirenpc.RunFromDB(ctx, cfg, pool, cipher)
}

// runVoiceWorker is the -mode voice claim-plane worker (#491, ADR-0057): instead
// of a single static guild/channel it polls the voice_session_intents table,
// claims the oldest pending intent (FOR UPDATE SKIP LOCKED, ADR-0049), runs it
// through the tenant-aware Manager (#488) over the per-Tenant Discord client
// registry (#489), heartbeats while live, and finishes the row on end. No
// mid-session takeover (ADR-0006): a stale heartbeat marks the claim dead and the
// Tenant restarts. SIGTERM stops claiming and drains live sessions cleanly within
// the window. The voice role reads $GLYPHOXA_SECRET to decrypt BYOK Tenant tokens
// (ADR-0057 (d)).
//
// Interactions are dispatched by exactly ONE elected presence owner (#492,
// ADR-0057 (c)): every gateway session on the shared central token receives every
// INTERACTION_CREATE (P5), so with replicas > 1 each worker would otherwise handle
// the same slash command. The presence Registry boots INACTIVE and an OwnerElector,
// running beside the ClaimLoop on this same instanceID, flips it active only while
// this Instance holds the singleton presence_owner claim; a non-owner drops the
// duplicate events it still receives. Voice itself needs no such election — a pod
// holding no connection for a guild simply ignores that guild's voice events (P6).
func runVoiceWorker(log *slog.Logger, cfg wirenpc.Config, metrics *observe.PrometheusRecorder, metricsAddr string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg.Logger = log
	cfg.Metrics = metrics
	cfg.StageMetrics = metrics
	cfg.Token = os.Getenv("DISCORD_BOT_TOKEN") // central-token fallback; BYOK tenants override per-session

	dsn := databaseURL()
	// ADR-0031: the worker never migrates (only -mode all does); verify the schema
	// is current and fail loud with the actionable message if behind.
	if err := wirenpc.EnsureSchemaCurrent(ctx, dsn); err != nil {
		return err
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("voice worker: open db pool: %w", err)
	}
	defer pool.Close()
	store := storage.New(pool)
	blobStore := blob.NewPostgres(pool)

	// The BYOK credential cipher (ADR-0004/0057 (d)): the voice role now decrypts
	// per-Tenant Bot tokens itself. Best-effort — a worker with no $GLYPHOXA_SECRET
	// still serves central-token Tenants on the env fallback.
	cipher, err := appCipher()
	if err != nil {
		log.Warn("provider credential decryption disabled; the worker serves only env-token tenants until $GLYPHOXA_SECRET is set", "err", err)
		cipher = nil
	}

	if metricsAddr != "" {
		observe.NewMetricsServer(metricsAddr, metrics, observe.ReadinessProbe(pool.Ping), log).Start(ctx)
	}

	// Worker-scoped boot reconciliation (#491, reviewer-flagged): a plain
	// ReconcileOrphanedVoiceSessions is process-blind — two workers booting would
	// close each other's live 'running' rows. Close ONLY rows whose owning intent
	// went terminal (a crashed worker's leftovers), never one an intent still holds
	// live. Required before replicas > 1 (#492).
	if n, err := store.ReconcileWorkerOrphanedVoiceSessions(ctx); err != nil {
		return fmt.Errorf("voice worker: reconcile orphaned sessions: %w", err)
	} else if n > 0 {
		log.Warn("voice worker: closed orphaned voice sessions behind terminal intents", "count", n)
	}

	instanceID := newVoiceInstanceID()
	log.Info("voice worker starting", "instance", instanceID)

	// ONE process bus; each session gets its own bus Forwarded onto this (#487).
	eventBus := voiceevent.NewBus()
	cfg.Bus = eventBus
	sessions := session.NewRegistry()

	// GM identity (ADR-0055): the per-Tenant Butler voice-address gate + transcript
	// GM labels. A failed load degrades to the env allowlist alone, never a boot
	// failure.
	allow := auth.ParseOperatorAllowlist(os.Getenv("GLYPHOXA_OPERATOR_IDS"))
	gmID := auth.NewGMIdentity(store, allow, log)
	if err := gmID.Refresh(ctx); err != nil {
		log.Warn("voice worker: loading tenant-operator GM bindings failed; falling back to the allowlist", "err", err)
	}
	cfg.GMSpeaker = gmSpeakerGate(false, gmID.IsGM)

	// Effective Admission Mode (ADR-0055) drives the plan-allowance + platform-key
	// entitlement gates the session Manager applies at Start — the worker READS the
	// posture the web tier records; it does not record it.
	admission := auth.AdmissionAllowlist
	if res, aerr := resolveAdmissionMode(ctx, store, os.Getenv); aerr != nil {
		log.Warn("voice worker: resolve admission mode; defaulting to allowlist gates", "err", aerr)
	} else {
		admission = res.Effective
	}
	keyEnt := entitlementForMode(admission, store)
	cfg.KeyEntitlement = keyEnt

	// Per-Tenant Discord client registry (#489): the standing client keyed by each
	// Tenant's resolved Bot token, plus the tenant command surface. The Registry
	// boots INACTIVE (#492): the OwnerElector below flips it active only while this
	// Instance wins the presence_owner election, so exactly one worker on the shared
	// central token dispatches each interaction (ADR-0057 (c)).
	gate := presence.NewGate(gmID, presence.NewStorageTenantResolver(store))
	reg := presence.NewRegistry(gate, log)
	reg.SetActive(false)
	reg.Register(presence.RollCommand(tool.NewDice()))
	reg.RegisterComponentHandler(presence.NewConsentButtons(store, sessions, log).HandleComponent)
	clients := presence.NewClients(store, cipher, reg, cfg.Token, log)
	clients.SetGatewayBudget(cfg.GatewayBudget)

	// The Manager's persistence deps (the buildVoiceDeps set, #491): the relay's
	// headless line writer/finalizer, the chunk writer, the Highlight saver, NPC
	// recall + KG-facts, the knowledge Tools, the durable Usage ledger, the
	// plan-allowance gate, and the per-Campaign speaker resolver. The relay is
	// headless here — no SSE mounts (ADR-0014 Hop-A stays deferred on the voice
	// tier); it exists only to persist transcript lines and finalize them at Stop.
	// The set is built by the SAME helper the web/all boot uses (#483) so the two
	// paths cannot drift; the worker has no dev mode, so dev is false.
	vd := buildVoiceDeps(store, cipher, metrics, log, keyEnt, admission, eventBus, sessions, blobStore, clients, gmID, false)
	recapEngine, deps := vd.recapEngine, vd.deps

	// NPC memory recall (#122): resolved once over the shared embeddings provider.
	// An unavailable provider leaves recall off (loud-but-non-fatal), exactly as in
	// the web/all boot.
	if provider, _, err := embedworker.ResolveProvider(ctx, store); err != nil {
		log.Error("voice worker: embeddings provider unavailable; NPC memory recall disabled", "err", err)
	} else {
		recaller := recall.New(provider, store, sessions, eventBus, metrics, log, recall.Config{})
		context.AfterFunc(ctx, recaller.Close)
		deps.Memory = recaller
	}

	runner := func(rctx context.Context, c wirenpc.Config) error {
		return wirenpc.RunFromDB(rctx, c, pool, cipher)
	}
	mgr := session.NewManager(store, runner, cfg, cipher, log, true, deps)

	// The claim-plane session control (#491 review item 1): in WORKER mode
	// /glyphoxa start and end must NOT drive the Manager directly — that would run a
	// slash-started session with NO intent row (no heartbeat, the one-live-per-tenant
	// invariant false, IntentControl/archive guards blind, an unreconcilable crash
	// row). Route them through IntentControl so /glyphoxa start writes an intent this
	// same worker's loop claims (typically within one poll) and /glyphoxa end
	// requests the stop the loop honors — exactly the plane the web tier uses. The
	// live controls (mute/say) still drive the LOCAL Manager, which holds a live
	// session only when THIS worker is running it: at replicas > 1 the presence
	// owner dispatching the interaction may not be the session's host, so those
	// commands take intentControl as their PoolSession and reply the split-mode
	// limitation (#483) when the session is live on another worker — the cross-pod
	// control plane is #503. search and recap resolve their Active Campaign through
	// intentControl (the pool-wide Active), so they work regardless of which worker
	// hosts the session; a `voiced` recap still degrades to public text when the
	// Butler isn't in THIS worker's session (decision 6a).
	intentControl := session.NewIntentControl(store, log, voiceIntentControlConfig(os.Getenv))

	// Register the full tenant command surface (start/end/search/mute/say/recap),
	// then seed the standing clients so the commands appear with no session live.
	reg.Register(
		presence.UseCommand(store),
		presence.StartCommand(store, intentControl),
		presence.EndCommand(intentControl),
		presence.SearchCommand(store, intentControl),
		presence.RecapCommand(store, intentControl, recapEngine, mgr),
		presence.MuteCommand(mgr, store, intentControl),
		presence.MuteAllCommand(mgr, store, intentControl),
		presence.SayCommand(mgr, store, intentControl),
	)
	if err := clients.EnsureAll(ctx); err != nil {
		log.Warn("voice worker: initial presence seed failed; retries on the next Discord settings save", "err", err)
	}
	go clients.Run(ctx)

	// Presence-owner election (#492, ADR-0057 (c)): runs beside the claim loop on the
	// SAME instanceID, flipping the Registry active only while this Instance owns the
	// singleton presence_owner row. It runs on its OWN context so the drain can
	// sequence its Release AFTER the Manager finishes its rows (below) — releasing
	// the owner claim is the LAST coordination write, so a survivor takes over
	// interaction dispatch only once this instance has cleanly wound its sessions
	// down.
	electorCfg := voicePresenceElectorConfig(os.Getenv)
	warnElectorCadence(electorCfg, log)
	elector := presence.NewOwnerElector(store, instanceID, reg.SetActive, log, electorCfg)
	electorCtx, electorStop := context.WithCancel(context.Background())
	electorDone := make(chan struct{})
	go func() { defer close(electorDone); elector.Run(electorCtx) }()

	// Run the claim loop until SIGTERM. It stops claiming on ctx cancel and drains
	// its live sessions cleanly (AC5). Drain order (ADR-0006/0057): stop claiming →
	// Manager Shutdown (Finish the live rows) → release the presence-owner claim →
	// close the standing clients. The Manager finishes its rows BEFORE the owner
	// claim is released so no survivor starts dispatching for this instance's guilds
	// mid-teardown.
	claimCfg := voiceClaimLoopConfig(os.Getenv)
	warnClaimCadence(claimCfg, log)
	loop := session.NewClaimLoop(store, mgr, instanceID, log, claimCfg)
	drainVoiceWorker(
		func() { loop.Run(ctx) },
		mgr.Shutdown,
		func() { electorStop(); <-electorDone },
		clients.Close,
	)
	return nil
}

// voiceDeps is the buildVoiceDeps set (#491): the session Manager's
// construction-time Deps plus the collaborators the caller still wires
// individually — the recap engine (slash command + RPC consumers), the speaker
// resolver (Character CRUD invalidation hook), and the relay (SSE mounts +
// shutdown hook on the web tier).
type voiceDeps struct {
	recapEngine     *recap.Engine
	speakerResolver *speaker.Resolver
	relay           *transcript.Relay
	deps            session.Deps
}

// buildVoiceDeps assembles the Manager's shared persistence collaborators — the
// recap engine, the per-Campaign speaker resolver (#281/#488), the headless
// transcript relay + chunk writer (#487), the Highlight saver (#308), KG-facts
// recall (#126), the knowledge Tools (#296), the Usage ledger (ADR-0054) and
// the plan-allowance gate (ADR-0055) — IDENTICALLY for the -mode voice worker
// and the web/all boot (#483), so the two paths cannot drift.
//
// Mode-specific seams stay with the caller: the web tier adds the relay's
// tenant scope + SSE mounts and the boot gauge seed, and each caller resolves
// Deps.Memory against its own embeddings-provider posture. clients may be nil
// (web-only mode): then no guild-name fallback, and Deps.Clients /
// Deps.GMSpeakerForTenant stay true nils — a typed-nil *presence.Clients would
// make the Manager panic at Start (#489), and web-only starts no sessions
// anyway. dev admits every speaker in the per-Tenant Butler GM overlay (#490,
// mirrors dev auto-auth); the worker has no dev mode and passes false.
func buildVoiceDeps(store *storage.Store, cipher *crypto.Cipher, metrics *observe.PrometheusRecorder, log *slog.Logger, keyEnt llmbuild.PlatformKeyEntitlement, admission auth.AdmissionMode, eventBus *voiceevent.Bus, sessions *session.Registry, blobStore blob.Store, clients *presence.Clients, gmID *auth.GMIdentity, dev bool) voiceDeps {
	// The one-shot recap Engine (#272), constructed ONCE so its consumers share
	// it: the /glyphoxa recap slash command (#273), the GenerateRecap RPC (#274,
	// web tier), and the recap knowledge Tool (Deps.Tools below). It reads
	// transcripts via the store, decrypts a BYOK LLM key with cipher, meters
	// usage into the process metrics — but never persists a recap (ADR-0040).
	recapEngine := recap.NewEngine(store, cipher, metrics, log, recap.WithKeyEntitlement(keyEnt))

	// The speaker resolver (#281, E4) resolves a Speaker Lane snowflake to its
	// Character/GM display name for the relay + chunk prefix, falling back to the
	// Discord guild display name via the standing presence for an unmapped
	// speaker (web-only replicas have no presence: a true nil namer, generic
	// label). GM is the shared GM-identity checker (ADR-0055).
	var namer speaker.MemberNamer
	if clients != nil {
		namer = clients
	}
	speakerResolver := speaker.NewResolver(store, namer, gmID, log)

	// The transcript relay (issue #73, ADR-0040) subscribes to the process bus
	// once and reads each event's session from the session Registry (#487); the
	// Manager finalizes its writer queue on Stop (Deps.Transcript).
	relay := transcript.NewRelay(eventBus, sessions, store, log)
	relay.SetResolver(speakerResolver) // #281: resolve who/GM per line (nil-safe, off if unset)

	// The Transcript Chunk writer (#104, ADR-0011) folds utterances into
	// 3–6-utterance chunks written with embedding NULL (the async pipeline #116
	// fills them later). This CHUNK grain is independent of the relay's line
	// grain (ADR-0040).
	chunker := transcript.NewChunker(eventBus, sessions, store, metrics, log, transcript.ChunkerConfig{})
	chunker.SetResolver(speakerResolver) // #281: resolved name as each human line's chunk prefix

	// Knowledge Tools' read sources (#296, S1): storage-backed, UNCONDITIONAL —
	// no embeddings provider needed, so a keyless deployment still lets a granted
	// NPC recall the transcript and its own Node neighbourhood. SearchFacts drops
	// gm_private (ADR-0008).
	knowledgeAdapter := knowledge.New(store, store.PromptKG())

	deps := session.Deps{
		Registry:   sessions,
		Transcript: relay,
		Chunker:    chunker,
		// Session Highlights persistence (#308, ADR-0051): #307's detector Sink,
		// Begun/Finalized per session by the Manager.
		Highlights: highlight.NewSaver(store, blobStore, jobEnqueuer{store}, log),
		// Per-Campaign Speaker-Lane attribution (#488): the Manager rebinds
		// cfg.SpeakerName per Start with the session's Campaign so N concurrent
		// sessions each attribute user lines against their own roster.
		SpeakerNameForCampaign: func(campaignID uuid.UUID, speakerID string) string {
			return speakerResolver.Lookup(campaignID, speakerID).Name
		},
		// Process-wide cap on concurrent Voice Sessions (#488, ADR-0057 K).
		// Default 1; raising it >1 is soak-gated (#493, DAVE).
		MaxSessions: maxVoiceSessions(os.Getenv),
		// Durable Usage Ledger (ADR-0054): attribution only; gating stays with the
		// spend meter (ADR-0046).
		Usage: store,
		// Monthly plan-allowance gate (ADR-0055 gate (b)): store-backed only in
		// `open` Admission Mode; nil (a no-op) in allowlist mode.
		Allowance: allowanceForMode(admission, store),
		// NPC KG-facts recall (#126, ADR-0008): UNCONDITIONAL — needs only the
		// process store, never the embeddings provider.
		Facts: kgfacts.New(store.PromptKG(), metrics, log, kgfacts.Config{}),
		Tools: tool.Deps{
			Transcripts: knowledgeAdapter,
			KG:          knowledgeAdapter,
			KGW:         knowledgeAdapter,
			Recap:       knowledge.NewRecap(recapEngine, store),
		},
	}
	if clients != nil {
		// Per-tenant Discord client registry (#489): every manager-started Voice
		// Session borrows the standing client keyed by its own Tenant's resolved
		// Bot token.
		deps.Clients = clients
		// Per-Tenant Butler GM-address gate (#490, ADR-0055): the Manager overlays
		// cfg.GMSpeaker per Start with the session's Tenant, so a Tenant A operator
		// is not GM in a Tenant B session.
		deps.GMSpeakerForTenant = func(tenantID uuid.UUID, discordUserID string) bool {
			if dev {
				return true
			}
			return discordUserID != "" && gmID.IsGMInTenant(tenantID, discordUserID)
		}
	}
	return voiceDeps{recapEngine: recapEngine, speakerResolver: speakerResolver, relay: relay, deps: deps}
}

// drainVoiceWorker runs the -mode voice worker to SIGTERM and then tears it down in
// the ONE correct order (#492, ADR-0006/0057): run blocks claiming the plane and
// serving until ctx cancel; then stopClaimingAndFinish (the Manager's Shutdown)
// Finishes every live intent row; then releaseOwner drops the presence-owner claim;
// then closeClients tears the standing Discord clients down. The owner claim is the
// LAST coordination write before the clients go — a survivor must not begin
// dispatching interactions for this instance's guilds until its sessions are wound
// down, so releaseOwner strictly follows stopClaimingAndFinish. Extracted from
// runVoiceWorker so this ordering is unit-testable without a live DB or Discord.
func drainVoiceWorker(run, stopClaimingAndFinish, releaseOwner, closeClients func()) {
	run()
	stopClaimingAndFinish()
	releaseOwner()
	closeClients()
}

// ensureSchemaReady runs the boot schema preflight for the web/all entrypoint
// (ADR-0031). In `all` Mode (withVoice) it auto-applies the embedded migrations
// under the advisory lock — the self-host "start it and go" story (ADR-0034). A
// web-only replica (withVoice false) never migrates: it only verifies the schema
// is current and returns the actionable `migrate up` error if behind, so N web
// replicas can never race the migration and a behind DB fails the boot loudly
// instead of surfacing raw "relation does not exist" at first query.
func ensureSchemaReady(ctx context.Context, dsn string, withVoice bool) error {
	if withVoice {
		return autoMigrate(ctx, dsn)
	}
	// Web-only: verify, never migrate. The wrap keeps a stable checkpoint marker
	// while preserving the storage layer's verbatim actionable `migrate up`
	// message via %w.
	if err := wirenpc.EnsureSchemaCurrent(ctx, dsn); err != nil {
		return fmt.Errorf("schema preflight: %w", err)
	}
	return nil
}

// requireVoiceTarget enforces that explicit voice mode names BOTH a Discord
// guild and a voice channel (issue #282, AC3): the default Mode flipped to `all`,
// but a standalone voice node still has no way to pick a channel on its own, so
// -guild and -channel remain mandatory. `all` mode sources them per-session from
// the saved deployment config instead and never calls this.
func requireVoiceTarget(cfg wirenpc.Config) error {
	if cfg.Guild == "" || cfg.Channel == "" {
		return fmt.Errorf("-guild and -channel are required for voice mode")
	}
	return nil
}

// standaloneCampaignResolver is the narrow store surface the standalone voice
// node's Active-Campaign resolution reads (#323): the single operator's durable
// /glyphoxa use selection, and the most-recently-created campaign fallback.
// *storage.Store satisfies it; the ordering test injects a fake.
type standaloneCampaignResolver interface {
	GetOperatorActiveCampaign(ctx context.Context) (storage.Campaign, error)
	GetActiveCampaign(ctx context.Context) (storage.Campaign, error)
}

// resolveStandaloneCampaign applies the durable→recent Active-Campaign policy for
// a boot with no logged-in operator context (the standalone voice node, #323). It
// mirrors the web tier's resolveActiveCampaign (internal/rpc) minus the live
// Voice Session step — a standalone boot has none: the sole operator's durable
// selection wins, else the most-recently-created campaign, else an actionable
// no-campaign error (never a silent fall back to the seed roster).
func resolveStandaloneCampaign(ctx context.Context, store standaloneCampaignResolver) (storage.Campaign, error) {
	c, err := store.GetOperatorActiveCampaign(ctx)
	if err == nil {
		return c, nil
	}
	if !errors.Is(err, storage.ErrNotFound) {
		return storage.Campaign{}, fmt.Errorf("voice: resolve durable active campaign: %w", err)
	}
	c, err = store.GetActiveCampaign(ctx)
	if err == nil {
		return c, nil
	}
	if errors.Is(err, storage.ErrNotFound) {
		return storage.Campaign{}, fmt.Errorf("voice mode: no Active Campaign to voice; create a campaign in the web UI, select one with /glyphoxa use, or run `glyphoxa seed` to install the demo campaign")
	}
	return storage.Campaign{}, fmt.Errorf("voice: resolve active campaign: %w", err)
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
	// The env half of the Admission Mode switch (ADR-0055) parses BEFORE the
	// preflight: an unparsable posture refuses to boot even in dev mode (a
	// posture typo should never ship dark), and the preflight relaxes its
	// allowlist branches only on an EXPLICIT env `open`. The effective posture
	// (env, falling back to the DB record) resolves after the store opens.
	envAdmission, envAdmissionSet, err := admissionModeEnv(os.Getenv)
	if err != nil {
		return err
	}
	if !dev {
		if err := requireWebEnv(os.Getenv, envAdmissionSet && envAdmission == auth.AdmissionOpen); err != nil {
			return err
		}
	}

	dsn := databaseURL()
	if dsn == "" {
		return fmt.Errorf("web/all modes require a database; set $GLYPHOXA_DATABASE_URL (or $DATABASE_URL)")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Boot schema preflight (ADR-0031/ADR-0034): the self-host default `all` Mode
	// applies pending migrations at boot under the advisory lock, so a bare
	// `glyphoxa -mode all` — what `docker compose up` and `systemctl start` run —
	// reaches a current schema with no manual `migrate up` step (issue #282). A
	// web-only replica does NOT auto-migrate: it verifies the schema is current and
	// fails fast with an actionable message if behind, so N web replicas never race
	// the migration — the migrate hook / all-Mode owns it. Runs BEFORE the boot
	// sweep + any query below, which all need the schema present.
	if err := ensureSchemaReady(ctx, dsn, withVoice); err != nil {
		return fmt.Errorf("web: %w", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("web: open db pool: %w", err)
	}
	defer pool.Close()
	store := storage.New(pool)
	// The blob seam (ADR-0048): the v1 Postgres bytea backend, shared by the Voice
	// process (Session Highlight clip writes) and the Web process (clip serve). Both
	// meet only through Postgres, never shared memory (#308).
	blobStore := blob.NewPostgres(pool)

	// Highlight voice replay (#310, Epic 8, ADR-0051): the clip loader the ClipReplay
	// reactor uses to resolve a ReplayRequested's blob KEY back into playable chunks
	// (ADR-0005 — the event never carries audio). It rides the base voice config the
	// Manager copies per session, so a live replay plays the promoted clip through the
	// session's outbound pump. Web-only mode never starts a session, so it stays inert.
	cfg.ClipReplayLoader = func(ctx context.Context, clipKey string) ([]tts.AudioChunk, error) {
		rc, _, err := blobStore.Get(ctx, clipKey)
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		data, rerr := io.ReadAll(rc)
		if rerr != nil {
			return nil, rerr
		}
		return mixdown.DecodeWAV(data)
	}

	// The operator allowlist is parsed ONCE and shared by the boot sweep and the
	// GM-identity fallback below (one source of truth; dev mode usually has none).
	allow := auth.ParseOperatorAllowlist(os.Getenv("GLYPHOXA_OPERATOR_IDS"))

	// The EFFECTIVE Admission Mode (ADR-0055): env switch, falling back to the
	// DB-persisted posture, recorded back for visibility. GLYPHOXA_DEV_MODE
	// preempts Admission Mode entirely (dev auto-auth + loopback force are the
	// backstop) — the dev boot neither reads nor records the posture.
	admission := auth.AdmissionAllowlist
	var admissionRes admissionResolution
	if !dev {
		admissionRes, err = resolveAdmissionMode(ctx, store, os.Getenv)
		if err != nil {
			return err
		}
		admission = admissionRes.Effective
	} else if envAdmissionSet {
		log.Info("GLYPHOXA_DEV_MODE preempts Admission Mode: the admission switch is ignored on a dev instance (ADR-0055)")
	}
	if admission == auth.AdmissionOpen && allow.Len() == 0 {
		log.Warn("open admission with an EMPTY allowlist: this deployment has NO platform admins — " +
			"billing CLI parity and lock-down still work, but no web identity is platform-privileged (ADR-0055)")
	}

	// Boot-time session sweep (ADR-0041 amendment, issue #184): the allowlist
	// gates only NEW logins at the OAuth callback, so sessions issued before the
	// gate existed — or before a snowflake was removed — would stay valid for up
	// to 30 days. The allowlist is parsed at boot, so a restart is exactly when
	// a grant change takes effect: revoke every session whose owner is no longer
	// allowlisted (including leftover GLYPHOXA_DEV_MODE sessions). Dev mode has
	// no allowlist and skips the sweep. The sweep SPLITS by Admission Mode
	// (ADR-0055): in `open` mode it must not run — it would log out every
	// signup on every restart — and revocation is suspension-based instead
	// (users.suspended_at, enforced per-request by AuthenticateSession).
	// Flipping open -> allowlist plus a restart therefore evicts every signup:
	// the deliberate lock-down escape hatch.
	if err := sweepAllowlistSessions(ctx, store, dev, admission, allow, log); err != nil {
		return err
	}

	// Signup default plan (ADR-0055): resolved once here, preflighted FATALLY in
	// `open` mode — a bad slug must fail the boot, not every signup after OAuth.
	signupPlanSlug := strings.TrimSpace(os.Getenv("GLYPHOXA_SIGNUP_PLAN_SLUG"))
	if !dev && admission == auth.AdmissionOpen {
		if err := signupPlanPreflight(ctx, store, signupPlanSlug); err != nil {
			return err
		}
	}
	// Record the posture only now — AFTER the open-mode preflights — so a flip
	// to `open` that refuses to boot never becomes the deployment's recorded
	// state (a config revert then restores service without a manual override).
	if !dev {
		if err := admissionRes.record(ctx, store, log); err != nil {
			return err
		}
	}

	// GM identity (ADR-0055, amending ADR-0050's allowlist-membership clause):
	// the tenant-operator binding union the env allowlist, snapshot-cached so the
	// three GM consumers below (Butler voice gate, slash-command gate, transcript
	// GM labels) never block on the DB. A failed boot load degrades to the
	// allowlist alone — never a boot failure.
	gmID := auth.NewGMIdentity(store, allow, log)
	if err := gmID.Refresh(ctx); err != nil {
		log.Warn("web: loading tenant-operator GM bindings failed; GM identity falls back to GLYPHOXA_OPERATOR_IDS alone until the next refresh", "err", err)
	}

	// Platform-key entitlement (ADR-0054 seam (a), ADR-0055): the ONE
	// construction point every tenant-facing key resolution shares (voice
	// sessions, recap, image enrichment, and the RPC tier's provider-key
	// resolution via managementMounts). `allowlist` Admission Mode grants every
	// tenant the env fallback — the ADR-0039 hybrid policy unchanged. `open`
	// mode swaps in the subscription gate: only a tenant with an active
	// platform-key-source subscription may spend the deployment's env keys.
	keyEnt := entitlementForMode(admission, store)
	cfg.KeyEntitlement = keyEnt

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
	// The process-wide session Registry (#487, replacing the #448 View): the
	// process-wide bus consumers (relay, chunker, recall speculation) Resolve a
	// stamped event's session through it, the presence tape-consent buttons route
	// into a live session's bus by Campaign through it, and every Manager registers
	// itself in it (Deps.Registry). Hoisted above the presence block because the
	// ConsentButtons need it as their publisher. Pre-constructible (no Manager yet),
	// which is what lets the Manager's collaborators be built before the Manager.
	sessions := session.NewRegistry()

	var clients *presence.Clients
	var reg *presence.Registry
	if withVoice {
		// Butler GM-only voice-address gate (#280, ADR-0024): the per-session Butler
		// gate is now TENANT-scoped (#490) — the Manager overlays cfg.GMSpeaker per
		// Start from Deps.GMSpeakerForTenant (wired below), so a Tenant A operator is
		// not a GM in a Tenant B session. The base cfg.GMSpeaker stays the
		// deployment-wide label-only gate as a fallback for any non-manager path.
		// Dev mode admits every speaker (mirrors dev auto-auth).
		cfg.GMSpeaker = gmSpeakerGate(dev, gmID.IsGM)
		if !dev && gmID.Empty() {
			log.Warn("butler voice-address gate armed with no GM identity source (no tenant-operator binding, empty allowlist); Butler unaddressable by voice")
		}
		// Server-side interaction Gate (#490, ADR-0010): it resolves each interaction's
		// owning Tenant from its Guild (storage GetTenantIDByGuildID; since #483 a
		// guild_id has a single first-registrar-wins owner, the SAME authority the
		// member picker uses) and then
		// applies the per-Tenant GM rule, replacing #489's interim "any known Guild"
		// seam. A DM or an unknown Guild is cleanly rejected.
		gate := presence.NewGate(gmID, presence.NewStorageTenantResolver(store))
		reg = presence.NewRegistry(gate, log)
		reg.Register(presence.RollCommand(tool.NewDice()))
		// Rollover-tape consent buttons (#306, ADR-0051): the disclosure message's
		// Consent/Revoke buttons write the presser's consent row and publish
		// TapeConsentChanged on the SAME process-wide bus the session Manager uses, so
		// a live tape arms or clears that Speaker's lane.
		// Rollover-tape consent buttons route into the live session's own bus via the
		// session Registry (#487): NewConsentButtons' publisher is the Registry, so a
		// press reaches the session running the button's Campaign (and, via Forward,
		// the process bus stamped) rather than broadcasting on the process bus.
		reg.RegisterComponentHandler(presence.NewConsentButtons(store, sessions, log).HandleComponent)
		clients = presence.NewClients(store, cipher, reg, cfg.Token, log)
		// Instrument EVERY standing client the registry builds — one per distinct Bot
		// token, plus any rebuild — for the IDENTIFY-budget metrics (#486). Set before
		// the first EnsureAll (below) so the initial gateway opens already carry the
		// identify/resume listeners. The voice-cycle clients borrow these same
		// instrumented clients, so they need no per-cycle listeners (cfg.GatewayBudget
		// stays used only on the owned bench path).
		clients.SetGatewayBudget(cfg.GatewayBudget)
		// The voice loop borrows the Tenant's standing client from the registry via
		// the session Manager's Deps.Clients (wired below), NOT a single shared
		// ClientProvider on the base config — a per-session start resolves the client
		// keyed by its own Tenant's Bot token. EnsureAll is deferred until after the
		// Manager is built: the GM session commands (#108), /glyphoxa search (#120)
		// and /glyphoxa mute/muteall (#211) all need the Manager, so they register
		// below and the single EnsureAll then registers the FULL command surface.
	}

	runner := func(rctx context.Context, c wirenpc.Config) error {
		return wirenpc.RunFromDB(rctx, c, pool, cipher)
	}
	// The Manager's collaborators below (transcript projectors, highlight saver,
	// recallers, knowledge Tools adapter) are its construction-time deps (#448),
	// built FIRST against the pre-constructed session Registry (declared above): the
	// bus consumers Resolve each event's session through it, and the Manager
	// registers itself in it at construction (Deps.Registry). A Manager cannot exist
	// without its deps already wired.

	// The Manager's construction-time deps (#448) and their shared collaborators
	// (the buildVoiceDeps set, #491) are assembled by the SAME helper the -mode
	// voice worker uses (#483) so the two boot paths cannot drift. Web-only seams
	// land right below: the relay's tenant scope (its SSE mounts are web-only),
	// the boot gauge seed, and the assist engine.
	vd := buildVoiceDeps(store, cipher, metrics, log, keyEnt, admission, eventBus, sessions, blobStore, clients, gmID, dev)
	recapEngine, speakerResolver, relay, deps := vd.recapEngine, vd.speakerResolver, vd.relay, vd.deps

	// The on-demand campaign-creation assist Engine (#479) drafts NPC Personas
	// and linked Knowledge Graph entries from a GM prompt — strictly on button
	// press, preview-first. Same posture as the recap engine: BYOK key via
	// cipher, env fallback gated by keyEnt, usage metered but never cap-gated.
	assistEngine := assist.NewEngine(store, cipher, metrics, log, assist.WithKeyEntitlement(keyEnt))

	// #439: the snapshot/SSE mounts are tenant-scoped — a session outside the
	// caller's Tenant is 404 (session → campaign → tenant), enforced before the
	// SSE stream opens. The guarded mount table below declares TenantRequired
	// to inject the tenant this check reads. Web-only: the worker mounts no SSE.
	relay.SetTenantScope(store.VoiceSessionInTenant)

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
	// embedProvider is hoisted out of the resolve branch so the Knowledge Proposal
	// review surface's similarity hint (#300) can share the SAME resolved provider:
	// nil (an unsupported provider or a config-read error) leaves the hint on its
	// fulltext fallback, exactly as the backfill worker stalls loudly.
	var embedProvider embeddings.Provider
	if provider, model, err := embedworker.ResolveProvider(ctx, store); err != nil {
		log.Error("embeddings provider unavailable; embedding backfill and NPC memory recall disabled", "err", err)
	} else {
		embedProvider = provider
		// Backfill worker (#116, ADR-0011): claims chunks written with embedding NULL,
		// embeds their text, and UPDATEs each row — draining the gauge toward zero and
		// making the chunks returnable by embedding-filtered retrieval. It needs only
		// the DB + provider (not the voice loop), so it runs in web AND all mode. It
		// rides the process signal ctx, so SIGTERM stops it and any in-flight provider
		// call aborts with the same context.
		go embedworker.New(store, provider, model, metrics, log, embedworker.Config{}).Run(ctx)

		// NPC memory recall (#122, ADR-0011/0042): one recaller over the shared
		// provider + the process store (ANN retriever, #119) + the session Registry
		// (the active Campaign) + the process bus (STTPartial speculation). Wired as
		// Deps.Memory so every manager-started session's Agent loops fill the
		// reserved Hot Context memory slot; a slow/unavailable path degrades
		// to no-memory within the turn budget. Bound to the run ctx — AfterFunc drops
		// the bus subscription and stops the speculator on shutdown. In web-only mode
		// the Manager starts no sessions, so the recaller stays dormant (bus idle).
		// Set only in this branch so a keyless deployment leaves Memory a true nil
		// (recall off), never a typed-nil interface.
		recaller := recall.New(provider, store, sessions, eventBus, metrics, log, recall.Config{})
		context.AfterFunc(ctx, recaller.Close)
		deps.Memory = recaller
	}

	mgr := session.NewManager(store, runner, cfg, cipher, log, withVoice, deps)
	// Mixed-deployment guard (#483 M3): -mode all must never share a database a
	// split (-mode web + -mode voice) deployment is driving — the broad reconcile
	// below would close live workers' rows and intent-less sessions would break
	// the one-live-per-tenant claim invariant. Read-only check, fatal on a hit;
	// BEFORE ReconcileOrphans so no live worker's row is ever touched.
	if withVoice {
		if err := guardAllModeMixedDeployment(ctx, store); err != nil {
			return err
		}
	}
	// Boot-time reconciliation (#143): close voice_sessions rows a previous run
	// left 'running' (crash / failed end-write). No loop is live yet, so every
	// such row is an orphan; done before the web tier serves so GetSession never
	// reports a dead session as live. Web-only mode skips inside (it owns no
	// rows). A failure here is a broken DB — fail the boot loudly.
	if err := mgr.ReconcileOrphans(ctx); err != nil {
		return fmt.Errorf("web: %w", err)
	}

	// Web-tier Voice-Session control (#491, ADR-0057): -mode all drives the
	// in-process Manager directly (byte-identical to today — NO intent rows). A
	// split -mode web tier instead drives the Postgres claim plane through
	// IntentControl: its StartSession writes a voice_session_intents row a -mode
	// voice worker claims and runs, and the live-only controls (mute/say/replay/
	// spend) degrade with CodeFailedPrecondition because that state lives in the
	// worker, not here (the ADR-0057 consequence). The choice pivots on withVoice:
	// an all-mode process owns the loop; a web-only one delegates to the plane.
	var sessionCtl sessionControl = mgr
	if !withVoice {
		sessionCtl = session.NewIntentControl(store, log, voiceIntentControlConfig(os.Getenv))
	}

	// Background job runner (#286, ADR-0049): one DB-backed generic runner over the
	// `job` table, safe across web/all replicas by construction (the claim is a
	// FOR UPDATE SKIP LOCKED, ADR-0039). It runs in web AND all mode (the
	// embedworker precedent), not standalone voice, and rides the process signal
	// ctx so SIGTERM stops it. No production kind is registered yet — Epic 8
	// Highlight enrichment is the first consumer (ADR-0049); embedworker stays
	// bespoke and recap/import stay synchronous RPCs. With an empty registry the
	// runner idles without touching the DB.
	jobRunner := jobs.New(store, metrics, log, jobs.Config{})
	// Session Highlights candidate purge (#308, ADR-0051/0049): the 7-day sweep of a
	// session's still-candidate highlights. Idempotent + at-least-once — it drops
	// each clip through the blob seam FIRST, then the rows. Registered in web AND all
	// mode (the sweep needs only the DB + blob backend, not the voice loop).
	jobRunner.Register(highlight.JobKindPurgeCandidates, highlight.PurgeHandler(store, blobStore, log))
	// Session Highlight campaign-clip sweep (#308, ADR-0048/0049): drops a hard-deleted
	// Campaign's clip blobs, enqueued in the delete's own transaction (idempotent).
	jobRunner.Register(highlight.JobKindSweepCampaignClips, highlight.CampaignSweepHandler(blobStore, log))
	// Session Highlight AI image enrichment (#311, ADR-0004 amendment/0049): a
	// promoted Highlight gets a Gemini-generated scene that lands through the blob
	// seam. The factory resolves the tenant's image BYOK key under the hybrid policy
	// (ADR-0039): a saved key needs the cipher (else a loud error, never a silent
	// env fallback), no row falls back to GEMINI_API_KEY, and neither present is
	// ErrImageNotConfigured (the handler leaves the Highlight intact without media).
	imageFactory := func(fctx context.Context, tenantID uuid.UUID) (imagegen.Generator, string, error) {
		var cfgPtr *storage.ProviderConfig
		cfg, cerr := store.GetProviderConfigByComponent(fctx, tenantID, storage.ComponentImage)
		if cerr == nil {
			cfgPtr = &cfg
		} else if !errors.Is(cerr, storage.ErrNotFound) {
			return nil, "", cerr
		}
		// Gated resolve (ADR-0054 seam (a)): an entitlement refusal errors HERE,
		// before the env fallback below can spend the deployment's GEMINI key.
		key, kerr := llmbuild.ResolveKeyGated(fctx, keyEnt, tenantID, cipher, cfgPtr, storage.ComponentImage)
		if kerr != nil {
			return nil, "", kerr // saved key without cipher = loud error (ADR-0039)
		}
		if key == "" {
			key = os.Getenv(imagegen.APIKeyEnv)
		}
		if key == "" {
			return nil, "", highlight.ErrImageNotConfigured
		}
		model := imagegen.DefaultModel
		if cfgPtr != nil && cfgPtr.Model != "" {
			model = cfgPtr.Model
		}
		return imagegen.NewGemini(key, imagegen.WithModel(model)), model, nil
	}
	jobRunner.Register(highlight.JobKindEnrichImage, highlight.EnrichImageHandler(store, blobStore, imageFactory, metrics, log))
	go jobRunner.Run(ctx)

	// Boot-time retention backstop (#308, ADR-0051, the ReconcileOrphans/#184 spirit):
	// a crash between a session ending and the Saver scheduling its 7-day candidate
	// purge would strand those candidates. At boot — AFTER ReconcileOrphans above, so
	// crash-orphaned sessions count as ended and their candidates are swept THIS
	// boot, not the next — enqueue a purge for every ended session that has
	// candidates but no live purge job. Loud-but-non-fatal: a failure logs and boot
	// continues (the next boot retries).
	if err := highlight.SweepMissingCandidatePurges(ctx, store, jobEnqueuer{store}, log); err != nil {
		log.Warn("highlight purge backstop sweep failed at boot", "err", err)
	}

	// Boot-time enrichment reconciliation (#406, ADR-0043/0048/0049): the same
	// rows-are-the-source-of-truth backstop for image enrichment. It re-enqueues
	// enrichment for every promoted Highlight left imageless with no live enrich job
	// (recovering a crash between promote-commit and the enqueue), and drops image
	// blobs whose Highlight row is gone (the delete-vs-enrich orphan the RPC's
	// re-read window only shrinks). Loud-but-non-fatal: a failure logs and boot
	// continues, exactly like the purge backstop above.
	if err := highlight.SweepEnrichmentReconciliation(ctx, store, blobStore, jobEnqueuer{store}, log); err != nil {
		log.Warn("highlight enrichment reconciliation sweep failed at boot", "err", err)
	}

	if withVoice {
		// The GM admin/session commands (#108, ADR-0010): /glyphoxa use sets the
		// durable Active Campaign; start/end drive the SAME in-process Manager the
		// web Session screen uses, so the two surfaces share one session record and
		// never diverge (AC4). Registered here (not in the presence block above)
		// because they need the Manager, and BEFORE Ensure so they land in the one
		// per-Guild registration alongside /roll. /glyphoxa search (#120) joins them:
		// it resolves the Active Campaign through the SAME shared slash resolver
		// (resolveActiveCampaign over the store + Manager), so it can never diverge
		// from /glyphoxa start.
		reg.Register(
			presence.UseCommand(store),
			presence.StartCommand(store, mgr),
			presence.EndCommand(mgr),
			presence.SearchCommand(store, mgr),
			// /glyphoxa recap (#273): recaps the Active Campaign's latest ended Voice
			// Session via the SAME shared slash resolver, delivered per the invoker's
			// choice (voiced/public/ephemeral, #271). The Manager is the ButlerVoicer
			// (#365): the now-voiced Butler (ADR-0009 #299) speaks a `voiced` recap via
			// SpeakAsButler → SayAs, so it lands as a KindButler transcript line; with no
			// live session OR a voiceless Butler a voiced request degrades to public text.
			presence.RecapCommand(store, mgr, recapEngine, mgr),
			// /glyphoxa mute <npc> + muteall (#211): the Manager is their SessionMuter
			// and the mute view the live loop reads (NewManager wired cfg.Mutes = mgr).
			// The nil PoolSession is deliberate: -mode all hosts every session in this
			// one process, so the local Manager read is already the whole truth (#483).
			presence.MuteCommand(mgr, store, nil),
			presence.MuteAllCommand(mgr, store, nil),
			// /say <text> as:<agent> (#295, ADR-0010): GM puppeteering. The Manager is the
			// SayControl (its SayAs publishes SpeakRequested on the shared bus, which the
			// live loop's DirectSpeech reactor renders in the NPC's Voice); store lists the
			// voiced roster for the resolver + autocomplete.
			presence.SayCommand(mgr, store, nil),
		)
		// Seed the per-tenant registry at boot (AC: the commands appear with no Voice
		// Session), one standing client per distinct Bot token (#489, ADR-0039
		// presence-before-request). Non-fatal per Tenant: a bad or absent token
		// leaves that Tenant in the wait-state and the RPC refresher retries on its
		// next save, without blocking the others. Then a background poll reconciles
		// out-of-band changes (a raw DB write / a missed refresher) on the interval.
		if err := clients.EnsureAll(ctx); err != nil {
			log.Warn("presence: initial seed failed; the slash-command surface "+
				"will retry when Discord settings are next saved", "err", err)
		}
		go clients.Run(ctx)
	}

	// The web tier serves the auth-guarded Connect API under /api, the Discord
	// OAuth carve-out under /auth (ADR-0015/0016), and the embedded SPA at /
	// (ADR-0013/0039). The SPA handler is the "/" catch-all; ServeMux's
	// longest-prefix match keeps /api/ and /auth/ ahead of it, so only non-API
	// paths (and client-side deep links) reach the SPA fallback.
	// The presence refresher reconciles the standing gateway after a Discord
	// settings save (#102), so a newly-saved Bot token / Guild registers the
	// slash-command surface without a restart. bg-context so it outlives the RPC
	// request; nil in web-only mode (no presence).
	var presenceRefresh func(tenantID uuid.UUID)
	if clients != nil {
		presenceRefresh = func(tenantID uuid.UUID) {
			// Bound the ensure: EnsureTenant holds the registry's ensureMu across
			// gateway + REST I/O, so a Tenant whose Discord endpoint hangs would
			// otherwise pin the lock and stall EVERY other Tenant's save + Voice
			// Session start behind it (finding 4). 60s is well past a healthy open.
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if err := clients.EnsureTenant(ctx, tenantID); err != nil {
				log.Warn("presence: refresh after Discord settings save failed; "+
					"will retry on the next save or Voice Session cycle", "tenant", tenantID, "err", err)
			}
		}
	}
	// Per-tenant Discord integration health for the Configuration read (#489):
	// "ok"/"waiting"/"failed" + detail for the request Tenant's standing client.
	// nil in web-only mode (no presence).
	var integrationStatus func(tenantID uuid.UUID) (string, string)
	if clients != nil {
		integrationStatus = func(tenantID uuid.UUID) (string, string) {
			st := clients.IntegrationStatus(tenantID)
			return st.State, st.Detail
		}
	}
	// The Players panel member picker (#279) resolves the REQUEST Tenant's voice
	// channel from its deployment config (#489 — tenant-scoped) and reads its
	// occupants off that Tenant's standing client's voice-state cache. nil in
	// web-only mode (no presence) so the RPC serves empty.
	var memberLister func(context.Context) ([]presence.Member, error)
	if clients != nil {
		clients := clients
		memberLister = func(ctx context.Context) ([]presence.Member, error) {
			tenantID, ok := auth.TenantID(ctx)
			if !ok {
				return nil, fmt.Errorf("resolve voice channel: no tenant in context")
			}
			dep, err := store.GetDeploymentConfig(ctx, tenantID)
			if err != nil {
				return nil, fmt.Errorf("resolve voice channel: %w", err)
			}
			channelID, err := snowflake.Parse(dep.VoiceChannelID)
			if err != nil {
				return nil, fmt.Errorf("parse voice channel id %q: %w", dep.VoiceChannelID, err)
			}
			return clients.VoiceChannelMembers(ctx, tenantID, channelID)
		}
	}
	mounts := managementMounts(store, blobStore, cipher, metrics, log, sessionCtl, relay, speakerResolver, recapEngine, assistEngine, presenceRefresh, integrationStatus, memberLister, embedProvider, admission, signupPlanSlug, keyEnt)
	root := spa.Handler()
	// GLYPHOXA_DEV_MODE opt-out (ADR-0041): seed + auto-authenticate the synthetic
	// operator on every request and pin the bind to loopback, so a dev instance
	// needs no OAuth and a mis-set flag in production is structurally unreachable.
	// Wrapping the mounts + SPA root routes every request through the existing
	// policy gate (the Connect interceptor stack and the guarded mount table,
	// #446) already authenticated. This
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
	// Close every standing client AFTER the Manager drains, so a live session
	// releases its borrowed client before it is torn down (#489).
	if clients != nil {
		clients.Close()
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
// jobEnqueuer adapts *storage.Store to the highlight.JobEnqueuer seam (#308): it
// JSON-marshals the payload and enqueues a job with an explicit future run_after
// (the 7-day candidate purge horizon), which plain EnqueueJob cannot express.
type jobEnqueuer struct{ store *storage.Store }

func (e jobEnqueuer) Enqueue(ctx context.Context, kind string, payload any, runAfter time.Time) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal %s payload: %w", kind, err)
	}
	_, err = e.store.EnqueueJobAt(ctx, kind, b, 0, runAfter)
	return err
}

// highlightClipSweeper adapts *storage.Store + blob.Store to the RPC campaign
// hard-delete's clip sweep (#308, ADR-0048): list a campaign's highlight clip
// keys, then drop each blob through the seam.
type highlightClipSweeper struct {
	store *storage.Store
	blobs blob.Store
}

func (s highlightClipSweeper) CampaignClipKeys(ctx context.Context, campaignID uuid.UUID) ([]string, error) {
	return s.store.ListCampaignHighlightClipKeys(ctx, campaignID)
}

func (s highlightClipSweeper) DeleteClip(ctx context.Context, key string) error {
	return s.blobs.Delete(ctx, key)
}

// plainMountPolicy is the declarative auth table for every plain (non-Connect)
// mount (#446): each pattern's tenant posture, enforced by auth.MustGuardMounts
// with the same auth.Policy the Connect stack runs. Every row is operator-gated
// (session, ADR-0041) and CSRF derives from the method (POST ⇒ double-submit,
// ADR-0016), so the tenant mode is the one explicit per-mount decision.
//
// TestPlainMountPolicy pins these declarations: downgrading a byte mount's
// posture (the #408 regression shape — e.g. clip losing TenantRequired) fails
// the suite, so a change here must be made deliberately, in both places.
var plainMountPolicy = map[string]auth.TenantMode{
	// The SSE relay + snapshot and the byte streams (clip/image/export) all
	// need the caller's Tenant to scope their reads (#439): session AND tenant.
	"GET /api/v1/sessions/{id}/events":  auth.TenantRequired,
	"GET /api/v1/sessions/{id}":         auth.TenantRequired,
	"GET /api/v1/highlights/{id}/clip":  auth.TenantRequired,
	"GET /api/v1/highlights/{id}/image": auth.TenantRequired,
	"GET /api/v1/campaigns/{id}/export": auth.TenantRequired,
	// TenantNone: ServeImport resolves the tenant off the session itself
	// (#291); the POST method already makes the guard demand the CSRF pair.
	"POST /api/v1/campaigns/import": auth.TenantNone,
}

// relay serves the live transcript over SSE + a JSON snapshot (#73, ADR-0014):
// its two plain net/http reads mount OUTSIDE the Connect /api prefix at
// /api/v1/sessions/{id}[/events], each a row in the guarded mount table
// (#446 — the Connect interceptor chain does not cover them).
// sessionControl is the web tier's Voice-Session control surface (#491): the RPC
// SessionManager plus the campaign/health/replay seams the mounts wire. BOTH
// *session.Manager (in-process, -mode all) and *session.IntentControl (claim-plane
// driven, -mode web of a split deployment) satisfy it, so managementMounts is
// mode-agnostic — runWeb picks which one to pass.
type sessionControl interface {
	Start(ctx context.Context, tenantID, campaignID uuid.UUID) (storage.VoiceSession, error)
	Stop(ctx context.Context, tenantID uuid.UUID) (storage.VoiceSession, error)
	Active(ctx context.Context, tenantID uuid.UUID) (storage.VoiceSession, bool, error)
	SetAgentMute(ctx context.Context, tenantID uuid.UUID, agentID string, muted bool) ([]string, error)
	SetAllMute(ctx context.Context, tenantID uuid.UUID, muted bool) ([]string, error)
	MutedAgentIDs(tenantID uuid.UUID) []string
	Spend(tenantID uuid.UUID) spend.Status
	IsCampaignLive(campaignID uuid.UUID) bool
	AnyLive() bool
	ReplayHighlight(ctx context.Context, tenantID uuid.UUID, clipKey string) error
}

func managementMounts(store *storage.Store, blobStore blob.Store, cipher *crypto.Cipher, metrics observe.StageRecorder, log *slog.Logger, mgr sessionControl, relay *transcript.Relay, speakerResolver *speaker.Resolver, recapEngine *recap.Engine, assistEngine *assist.Engine, presenceRefresh func(tenantID uuid.UUID), integrationStatus func(tenantID uuid.UUID) (string, string), memberLister func(context.Context) ([]presence.Member, error), embedProvider embeddings.Provider, admission auth.AdmissionMode, signupPlanSlug string, keyEnt llmbuild.PlatformKeyEntitlement) []web.Mount {
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
	// The OAuth callback's admission policy (ADR-0041 / ADR-0055). In allowlist
	// mode the GLYPHOXA_OPERATOR_IDS allowlist is the single authorization gate:
	// a Discord User whose snowflake is absent is denied a session before any
	// Tenant write. In `open` mode a stranger is instead admitted through the
	// create-only signup transaction (the boot preflighted the plan slug);
	// allowlisted Users keep claim-or-create in both modes.
	allowlist := auth.ParseOperatorAllowlist(os.Getenv("GLYPHOXA_OPERATOR_IDS"))
	oauth := auth.NewOAuth(store, discord, "/", auth.Admission{
		Mode:           admission,
		Allowlist:      allowlist,
		SignupPlanSlug: signupPlanSlug,
		Signup:         store,
	}, log)
	authServer := auth.NewAuthServer(store, store, store, admission, log)

	// The store satisfies both Authenticator (AuthenticateSession) and
	// TenantResolver (TenantForUser). ONE policy gates both transports (#446):
	// the Connect interceptor stack and the plain-mount table below are thin
	// adapters over the same auth.Policy, so the session/CSRF/tenant gate
	// cannot drift between them again (#408). GetCurrentUser and
	// GetAdmissionMode are the only public procedures — the SPA's boot probe
	// and the login screen's posture probe (ADR-0055), both sessionless by
	// nature.
	policy := auth.NewPolicy(store, store)
	stack := policy.Stack(
		managementv1connect.AuthServiceGetCurrentUserProcedure,
		managementv1connect.AuthServiceGetAdmissionModeProcedure,
	)

	campaignSrv := rpc.NewCampaignServer(store)
	// While a session is live, the roster/mute panel scopes to that session's
	// campaign so the GM mutes the NPCs actually in the channel, not a durable
	// selection changed mid-session (#222).
	campaignSrv.SetSessions(mgr)
	// The Knowledge Proposal review surface's similarity hint (#300, ADR-0052) shares
	// the resolved embeddings provider: nil (keyless / unsupported) leaves the hint on
	// its fulltext fallback rather than disabling review.
	if embedProvider != nil {
		campaignSrv.SetEmbedder(embedProvider)
	}
	// The Players panel's member picker (#279) lists the Discord Users currently in
	// the operator's configured voice channel, resolved from the deployment config
	// and read off the standing presence's voice-state cache (no privileged intent).
	// Wired only when a standing presence exists (memberLister non-nil); without it
	// the RPC serves an empty list and the picker falls back to free-text entry.
	if memberLister != nil {
		campaignSrv.SetMemberLister(memberLister)
	}
	// A Character mutation invalidates the campaign's cached speaker resolutions so
	// the live relay re-resolves future lines with the new mapping (#281, ADR-0039
	// in-proc direct-method invalidation).
	campaignSrv.SetSpeakerInvalidator(speakerResolver)
	// A campaign hard delete sweeps its Session Highlight clips out of blob storage
	// (#308, ADR-0048): the highlight rows cascade with the campaign, but their clip
	// blobs have no FK and must be dropped through the seam.
	campaignSrv.SetHighlightClipSweeper(highlightClipSweeper{store: store, blobs: blobStore})
	// The on-demand campaign-creation assist engine (#479): persona drafting and
	// knowledge-draft generation, strictly on GM button press.
	campaignSrv.SetAssist(assistEngine)
	campaignPath, campaignHandler := campaignSrv.Handler(stack.HandlerOptions()...)
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
	// The same entitlement the voice/recap/image consumers share gates the RPC
	// tier's provider-key resolution (ADR-0054 seam (a) — the closed phase-B
	// gap): health pings, model/voice catalogs, and the TTS preview refuse the
	// env fallback for an unentitled tenant in `open` mode.
	voiceSrv.SetKeyEntitlement(keyEnt)
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
	// The Configuration read surfaces THIS Tenant's Discord integration health
	// (#489): ok / waiting / failed (+ detail) for its standing client.
	if integrationStatus != nil {
		providerSrv.SetIntegrationStatusSource(integrationStatus)
	}
	// The OAuth client id is the Discord application id; ListProviderConfigs
	// echoes it so the Configuration screen builds the bot-authorization URL
	// (#110). Empty when DISCORD_OAUTH_CLIENT_ID is unset — the screen's disabled
	// fallback.
	providerSrv.SetDiscordApplicationID(clientID)
	providerPath, providerHandler := providerSrv.Handler(stack.HandlerOptions()...)
	voicePath, voiceHandler := voiceSrv.Handler(stack.HandlerOptions()...)
	// The recap engine regenerates Butler-flavoured Session recaps on demand
	// (#272/#274, gate #271: never persisted). It spends provider money per call
	// and meters usage, so SessionService.GenerateRecap is CSRF-guarded like a
	// mutation. It is constructed ONCE in runWeb and passed in, so the GenerateRecap
	// RPC (#274) and the /glyphoxa recap slash command (#273) share one instance.
	sessionSrv := rpc.NewSessionServer(mgr, store, recapEngine, log)
	// Session Highlights read/mutate (#308): List/Get/Promote/Delete over the same
	// store + blob seam the Voice process writes through. Wired here so the many
	// NewSessionServer call sites keep their signature.
	sessionSrv.SetHighlights(store, blobStore, jobEnqueuer{store})
	// Highlight Discord delivery (#310, Epic 8, ADR-0051): the GM shares a promoted
	// Highlight as a file to a text channel (DeploymentSharer resolves the Bot token +
	// guild from deployment_config via the cipher, then plain net/http Discord REST —
	// ADR-0047) or replays it into the live voice channel (the session Manager). The
	// Campaign's last-chosen channel is remembered through the store.
	sessionSrv.SetSharing(rpc.NewDeploymentSharer(store, cipher, log), mgr, store)
	sessionPath, sessionHandler := sessionSrv.Handler(stack.HandlerOptions()...)

	// Session Highlight clip serve (#308/#309): GET /api/v1/highlights/{id}/clip, a
	// plain net/http byte stream (ADR-0015) beside the SSE relay, operator-gated
	// via the guarded mount table. Tenant-scoped row load + blob.Get +
	// http.ServeContent (Range → scrub).
	// The clip route scopes to the Active Campaign server-side (#308), sharing the
	// SessionServer's read-side resolution so a foreign-campaign clip id is 404 just
	// like the Highlight RPCs.
	clipServer := highlight.NewClipServer(store, blobStore, sessionSrv.ResolveActiveCampaign, log)

	// The campaign-bundle transport (#290, ADR-0053) is a PLAIN net/http mount
	// beside the SSE relay, not a Connect service (ADR-0015): a streamed gzip
	// download does not fit Connect's message model. Operator-only via the
	// guarded mount table, the same gate the relay reads (ADR-0041). The GET
	// export (#290) and the POST import (#291) share this handler.
	bundleHandler := &bundle.Handler{Store: store, Log: log}

	// The plain (non-Connect) mounts bind their handlers here; their auth
	// posture is declared separately in [plainMountPolicy] (#446), and the two
	// maps are zipped below. MustGuardMounts enforces the result with the SAME
	// policy the Connect stack runs and panics the boot on an under-declared
	// row — a mount can no longer silently compose the wrapper subset that
	// shipped #408.
	plainHandlers := map[string]http.Handler{
		// The SSE relay + snapshot are PLAIN mounts (not web.APIMount): they want
		// the full /api/v1/... path, not the /api-stripped Connect method path.
		// Go 1.22 method+wildcard patterns keep them off the Connect mounts
		// (/api/glyphoxa.management.v1.*) and the SPA root. Session AND tenant
		// (#439, the post-#408 discipline) — the relay's TenantScope (wired
		// above) 404s a session outside the caller's Tenant before the SSE
		// stream opens.
		"GET /api/v1/sessions/{id}/events": http.HandlerFunc(relay.ServeEvents),
		"GET /api/v1/sessions/{id}":        http.HandlerFunc(relay.ServeSnapshot),
		// Session Highlight clip (#308): operator-gated audio byte stream with
		// Range. ServeClip/ServeImage require the tenant (#408), resolved
		// server-side off the operator; without it TenantID always missed and
		// every request 401'd.
		"GET /api/v1/highlights/{id}/clip": http.HandlerFunc(clipServer.ServeClip),
		// Session Highlight AI image (#311): operator-gated image byte stream, same
		// tenant + Active-Campaign 404 posture as the clip; no image yet → 404.
		"GET /api/v1/highlights/{id}/image": http.HandlerFunc(clipServer.ServeImage),
		// Campaign bundle export (#290): streamed gzip download, operator-gated,
		// session AND tenant (#439) — a foreign-tenant campaign id is 404.
		"GET /api/v1/campaigns/{id}/export": http.HandlerFunc(bundleHandler.ServeExport),
		// Campaign bundle import (#291): multipart upload, operator-gated; the
		// POST method makes the guard demand the CSRF double-submit (ADR-0016)
		// the SPA satisfies with the script-readable glyphoxa_csrf cookie.
		"POST /api/v1/campaigns/import": http.HandlerFunc(bundleHandler.ServeImport),
	}
	if len(plainHandlers) != len(plainMountPolicy) {
		panic(fmt.Sprintf("plain mounts and plainMountPolicy disagree: %d handlers, %d policy rows — every plain mount must declare its posture (#446)",
			len(plainHandlers), len(plainMountPolicy)))
	}
	rows := make([]auth.GuardedMount, 0, len(plainHandlers))
	for _, pattern := range slices.Sorted(maps.Keys(plainHandlers)) {
		// A handler missing from plainMountPolicy yields the zero TenantMode,
		// which MustGuardMounts rejects loudly.
		rows = append(rows, auth.GuardedMount{
			Pattern: pattern,
			Tenant:  plainMountPolicy[pattern],
			Handler: plainHandlers[pattern],
		})
	}
	guarded := auth.MustGuardMounts(policy, rows)

	mounts := []web.Mount{
		web.APIMount(campaignPath, campaignHandler),
		web.APIMount(authPath, authHandler),
		web.APIMount(providerPath, providerHandler),
		web.APIMount(voicePath, voiceHandler),
		web.APIMount(sessionPath, sessionHandler),
	}
	for _, g := range guarded {
		mounts = append(mounts, web.Mount{Path: g.Pattern, Handler: g.Handler})
	}
	return append(mounts,
		// The OAuth redirects are the login carve-out (ADR-0015): public by
		// design, no session to require yet.
		web.Mount{Path: "/auth/discord/login", Handler: http.HandlerFunc(oauth.Login)},
		web.Mount{Path: "/auth/discord/callback", Handler: http.HandlerFunc(oauth.Callback)},
	)
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
