// Command glyphoxa is the main entry point for the Glyphoxa voice AI server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"encoding/json"

	"google.golang.org/grpc"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave/golibdave"
	"github.com/disgoorg/godave/libdave"
	"github.com/jackc/pgx/v5/pgxpool"
	anyllmlib "github.com/mozilla-ai/any-llm-go"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/MrWong99/glyphoxa/internal/agent/orchestrator"
	"github.com/MrWong99/glyphoxa/internal/app"
	"github.com/MrWong99/glyphoxa/internal/config"
	discordbot "github.com/MrWong99/glyphoxa/internal/discord"
	"github.com/MrWong99/glyphoxa/internal/discord/commands"
	"github.com/MrWong99/glyphoxa/internal/entity"
	"github.com/MrWong99/glyphoxa/internal/feedback"
	gw "github.com/MrWong99/glyphoxa/internal/gateway"
	"github.com/MrWong99/glyphoxa/internal/gateway/dispatch"
	"github.com/MrWong99/glyphoxa/internal/gateway/grpctransport"
	"github.com/MrWong99/glyphoxa/internal/gateway/sessionorch"
	"github.com/MrWong99/glyphoxa/internal/gateway/usage"
	gwvault "github.com/MrWong99/glyphoxa/internal/gateway/vault"

	"k8s.io/client-go/kubernetes"
	k8srest "k8s.io/client-go/rest"

	"github.com/MrWong99/glyphoxa/internal/health"
	"github.com/MrWong99/glyphoxa/internal/mcp"
	"github.com/MrWong99/glyphoxa/internal/mcp/mcphost"
	"github.com/MrWong99/glyphoxa/internal/observe"
	"github.com/MrWong99/glyphoxa/internal/session"
	"github.com/MrWong99/glyphoxa/pkg/audio"
	"github.com/MrWong99/glyphoxa/pkg/audio/webrtc"
	"github.com/MrWong99/glyphoxa/pkg/provider/embeddings"
	ollamaembed "github.com/MrWong99/glyphoxa/pkg/provider/embeddings/ollama"
	oaembed "github.com/MrWong99/glyphoxa/pkg/provider/embeddings/openai"
	"github.com/MrWong99/glyphoxa/pkg/provider/llm"
	"github.com/MrWong99/glyphoxa/pkg/provider/llm/anyllm"
	"github.com/MrWong99/glyphoxa/pkg/provider/s2s"
	geminilive "github.com/MrWong99/glyphoxa/pkg/provider/s2s/gemini"
	oais2s "github.com/MrWong99/glyphoxa/pkg/provider/s2s/openai"
	"github.com/MrWong99/glyphoxa/pkg/provider/stt"
	"github.com/MrWong99/glyphoxa/pkg/provider/stt/deepgram"
	elevenlabsstt "github.com/MrWong99/glyphoxa/pkg/provider/stt/elevenlabs"
	"github.com/MrWong99/glyphoxa/pkg/provider/stt/whisper"
	"github.com/MrWong99/glyphoxa/pkg/provider/tts"
	"github.com/MrWong99/glyphoxa/pkg/provider/tts/coqui"
	"github.com/MrWong99/glyphoxa/pkg/provider/tts/elevenlabs"
	"github.com/MrWong99/glyphoxa/pkg/provider/vad"
	energyvad "github.com/MrWong99/glyphoxa/pkg/provider/vad/energy"
	silerovad "github.com/MrWong99/glyphoxa/pkg/provider/vad/silero"
)

func main() {
	os.Exit(run())
}

func run() int {
	// ── CLI flags ──────────────────────────────────────────────────────────────
	configPath := flag.String("config", "config.yaml", "path to the YAML configuration file")
	mode := flag.String("mode", "full", "run mode: full, gateway, worker, mcp-gateway")
	flag.Parse()

	// ── Load configuration ────────────────────────────────────────────────────
	cfg, err := config.Load(*configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "glyphoxa: config file %q not found — copy configs/example.yaml to get started\n", *configPath)
		} else {
			fmt.Fprintf(os.Stderr, "glyphoxa: %v\n", err)
		}
		return 1
	}

	// ── Logger ────────────────────────────────────────────────────────────────
	logger := newLogger(cfg.Server.LogLevel)
	slog.SetDefault(logger)

	if cfg.Server.LogLevel == config.LogDebug {
		// Enable verbose libdave logging for DAVE protocol debugging.
		libdave.SetDefaultLogLoggerLevel(slog.LevelDebug)
	}

	slog.Info("glyphoxa starting",
		"config", *configPath,
		"mode", *mode,
		"listen_addr", cfg.Server.ListenAddr,
		"log_level", cfg.Server.LogLevel,
	)

	// ── Observability ─────────────────────────────────────────────────────────
	otelShutdown, err := observe.InitProvider(context.Background(), observe.ProviderConfig{
		ServiceName: "glyphoxa",
	})
	if err != nil {
		slog.Error("failed to initialise observability provider", "err", err)
		return 1
	}
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := otelShutdown(shutCtx); err != nil {
			slog.Warn("otel shutdown error", "err", err)
		}
	}()

	// ── Dispatch to mode-specific run function ───────────────────────────────
	switch *mode {
	case "full":
		return runFull(cfg)
	case "gateway":
		return runGateway(cfg)
	case "worker":
		return runWorker(cfg)
	case "mcp-gateway":
		return runMCPGateway(cfg)
	default:
		fmt.Fprintf(os.Stderr, "glyphoxa: unknown mode %q (valid: full, gateway, worker, mcp-gateway)\n", *mode)
		return 1
	}
}

// runFull runs the single-process mode (current alpha behaviour).
// This is the open-source self-hosted deployment path.
func runFull(cfg *config.Config) int {
	// ── Tenant context (full mode) ────────────────────────────────────────────
	tenant := config.LocalTenant(cfg.Campaign.Name)

	// ── Provider registry ─────────────────────────────────────────────────────
	reg := config.NewRegistry()

	// bot is populated by the "discord" audio factory when providers.audio.name
	// is "discord". It provides the Discord gateway session, command router,
	// and permission checker needed for slash commands.
	var bot *discordbot.Bot
	registerBuiltinProviders(reg, &bot)

	// ── Instantiate providers ─────────────────────────────────────────────────
	providers, err := buildProviders(cfg, reg)
	if err != nil {
		slog.Error("failed to build providers", "err", err)
		return 1
	}

	// ── Signal context ────────────────────────────────────────────────────────
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// ── Startup summary ───────────────────────────────────────────────────────
	printStartupSummary(cfg, "full")

	application, err := app.New(ctx, cfg, providers, app.WithTenant(tenant))
	if err != nil {
		slog.Error("failed to initialise application", "err", err)
		return 1
	}

	// ── Observability HTTP server (/healthz, /readyz, /metrics) ───────────
	observeSrv := startObserveServer(cfg, application.ReadinessChecks()...)

	// ── Discord commands (when audio provider is "discord") ──────────────────
	if bot != nil {
		perms := bot.Permissions()

		sessionMgr := app.NewSessionManager(app.SessionManagerConfig{
			Platform:     providers.Audio,
			Config:       cfg,
			Providers:    providers,
			SessionStore: application.SessionStore(),
			Graph:        application.KnowledgeGraph(),
			Semantic:     application.SemanticIndex(),
			MCPHost:      application.MCPHost(),
			Entities:     application.EntityStore(),
			Tenant:       application.Tenant(),
		})

		// Session and recap register themselves in the constructor.
		commands.NewSessionCommands(bot, sessionMgr, perms)
		commands.NewRecapCommands(commands.RecapConfig{
			Bot:          bot,
			SessionMgr:   sessionMgr,
			Perms:        perms,
			SessionStore: application.SessionStore(),
		})

		// Voice recap — requires LLM + TTS.
		if providers.LLM != nil && providers.TTS != nil && application.RecapStore() != nil {
			recapGen := session.NewRecapGenerator(providers.LLM, providers.TTS, application.RecapStore())
			commands.NewVoiceRecapCommands(commands.VoiceRecapConfig{
				Bot:          bot,
				SessionMgr:   sessionMgr,
				Perms:        perms,
				Generator:    recapGen,
				RecapStore:   application.RecapStore(),
				SessionStore: application.SessionStore(),
				NPCs:         cfg.NPCs,
			})
		}

		// Remaining commands need explicit Register() calls.
		npcCmds := commands.NewNPCCommands(perms, sessionMgr.Orchestrator)
		npcCmds.Register(bot.Router())

		entityCmds := commands.NewEntityCommands(perms, func() entity.Store { return application.EntityStore() })
		entityCmds.Register(bot.Router())

		campaignCmds := commands.NewCampaignCommands(
			perms,
			func() entity.Store { return application.EntityStore() },
			func() *config.CampaignConfig { return &cfg.Campaign },
			sessionMgr.IsActive,
		)
		campaignCmds.Register(bot.Router())

		feedbackCmds := commands.NewFeedbackCommands(
			perms,
			feedback.NewFileStore("feedback.jsonl"),
			func() string { return sessionMgr.Info().SessionID },
		)
		feedbackCmds.Register(bot.Router())
	}

	// Start the Discord bot interaction loop in a separate goroutine.
	if bot != nil {
		go func() {
			if err := bot.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				slog.Error("discord bot error", "err", err)
			}
		}()
	}

	slog.Info("server ready — press Ctrl+C to shut down", "mode", "full")

	if err := application.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("run error", "err", err)
		return 1
	}

	// ── Graceful shutdown ─────────────────────────────────────────────────────
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	slog.Info("shutdown signal received, stopping…")

	if err := observeSrv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("observe server shutdown error", "err", err)
	}

	if bot != nil {
		if err := bot.Close(); err != nil {
			slog.Warn("discord bot close error", "err", err)
		}
	}

	if err := application.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown error", "err", err)
		return 1
	}
	slog.Info("goodbye")
	return 0
}

// runGateway runs the gateway mode: Discord gateway connections, slash command
// routing, session orchestration, internal admin API, and gRPC server for
// communicating with workers.
func runGateway(cfg *config.Config) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	printStartupSummary(cfg, "gateway")

	// ── Database ──────────────────────────────────────────────────────────────
	dsn := os.Getenv("GLYPHOXA_DATABASE_DSN")
	if dsn == "" {
		slog.Error("GLYPHOXA_DATABASE_DSN not set — database is required for gateway mode")
		return 1
	}
	pool, err := openGatewayPool(ctx, dsn)
	if err != nil {
		slog.Error("failed to connect to database", "err", err)
		return 1
	}
	defer pool.Close()

	// ── Admin API ─────────────────────────────────────────────────────────────
	adminKey := os.Getenv("GLYPHOXA_ADMIN_KEY")
	if adminKey == "" {
		slog.Warn("GLYPHOXA_ADMIN_KEY not set — admin API will reject all requests")
		adminKey = "__unset__"
	}

	// ── Session Orchestrator ─────────────────────────────────────────────────
	orch, err := sessionorch.NewPostgresOrchestrator(ctx, pool)
	if err != nil {
		slog.Error("failed to create postgres orchestrator", "err", err)
		return 1
	}
	orchAdapter := sessionorch.NewOrchestratorAdapter(orch)

	// ── K8s Dispatcher (optional — only when GLYPHOXA_K8S_NAMESPACE is set) ─
	var dispatcher *dispatch.Dispatcher
	k8sNamespace := os.Getenv("GLYPHOXA_K8S_NAMESPACE")
	if k8sNamespace != "" {
		k8sCfg, k8sErr := k8srest.InClusterConfig()
		if k8sErr != nil {
			slog.Warn("gateway: K8s in-cluster config not available, dispatcher disabled", "err", k8sErr)
		} else {
			clientset, csErr := kubernetes.NewForConfig(k8sCfg)
			if csErr != nil {
				slog.Error("gateway: failed to create K8s clientset", "err", csErr)
				return 1
			}
			cmName := os.Getenv("GLYPHOXA_JOB_TEMPLATE_CM")
			if cmName == "" {
				cmName = "glyphoxa-worker-job"
			}
			jobTemplate, tmplErr := dispatch.LoadJobTemplate(ctx, clientset, k8sNamespace, cmName)
			if tmplErr != nil {
				slog.Error("gateway: failed to load job template", "err", tmplErr)
				return 1
			}
			dispatcher = dispatch.NewDispatcher(clientset, k8sNamespace, jobTemplate)
			slog.Info("gateway: K8s dispatcher initialized",
				"namespace", k8sNamespace, "configmap", cmName)
		}
	}

	// ── Bot Manager + Connector ──────────────────────────────────────────────
	botMgr := gw.NewBotManager()
	botConnector := gw.NewDiscordBotConnector(botMgr)

	// Configure per-tenant slash command wiring.
	botConnector.SetCommandSetup(func(gwBot *gw.GatewayBot, tenant gw.Tenant) {
		router := gwBot.Router()
		perms := gwBot.Permissions()

		// Build NPC configs from the loaded config.
		var npcMsgs []gw.NPCConfigMsg
		for _, npc := range cfg.NPCs {
			npcMsgs = append(npcMsgs, gw.NPCConfigMsg{
				Name:           npc.Name,
				Personality:    npc.Personality,
				Engine:         string(npc.Engine),
				VoiceID:        npc.Voice.VoiceID,
				KnowledgeScope: npc.KnowledgeScope,
				BudgetTier:     string(npc.BudgetTier),
				GMHelper:       npc.GMHelper,
				AddressOnly:    npc.AddressOnly,
			})
		}

		// Per-tenant session controller.
		sessionCtrl := gw.NewGatewaySessionController(
			orchAdapter, dispatcher,
			tenant.ID, tenant.CampaignID, tenant.LicenseTier,
			gw.WithBotToken(tenant.BotToken),
			gw.WithNPCConfigs(npcMsgs),
			gw.WithWorkerDialer(func(addr string) (gw.WorkerClient, error) {
				return grpctransport.NewClient(addr)
			}),
		)

		// Session start/stop commands.
		commands.NewGatewaySessionCommands(gwBot, sessionCtrl, perms)

		// NPC commands — not yet available in gateway mode (requires gRPC
		// extensions); handlers return "no active session" gracefully.
		npcCmds := commands.NewNPCCommands(perms, func() *orchestrator.Orchestrator { return nil })
		npcCmds.Register(router)

		// Entity commands.
		entityCmds := commands.NewEntityCommands(perms, func() entity.Store { return nil })
		entityCmds.Register(router)

		// Campaign commands.
		campaignCmds := commands.NewCampaignCommands(
			perms,
			func() entity.Store { return nil },
			func() *config.CampaignConfig { return nil },
			func() bool { return sessionCtrl.IsActive("") },
		)
		campaignCmds.Register(router)

		// Feedback commands.
		feedbackCmds := commands.NewFeedbackCommands(
			perms,
			feedback.NewFileStore("feedback.jsonl"),
			func() string { return "" },
		)
		feedbackCmds.Register(router)
	})

	// ── Vault Transit encryption (optional) ─────────────────────────────────
	var tokenEncryptor gwvault.TokenEncryptor
	if vaultAddr := os.Getenv("VAULT_ADDR"); vaultAddr != "" {
		vaultToken := os.Getenv("VAULT_TOKEN")
		if vaultToken == "" {
			slog.Warn("VAULT_ADDR set but VAULT_TOKEN missing — bot token encryption disabled")
		} else {
			tc := gwvault.NewTransitClient(vaultAddr, vaultToken, "glyphoxa-bot-tokens")
			if err := tc.Ping(ctx); err != nil {
				slog.Warn("vault: health check failed — bot token encryption disabled (graceful degradation)", "err", err)
			} else {
				slog.Info("vault: transit encryption enabled for bot tokens", "addr", vaultAddr)
			}
			tokenEncryptor = tc
		}
	}

	adminStore, err := gw.NewPostgresAdminStore(ctx, pool, tokenEncryptor)
	if err != nil {
		slog.Error("failed to create postgres admin store", "err", err)
		return 1
	}
	adminAPI := gw.NewAdminAPI(adminStore, adminKey, botConnector)

	// ── Reconnect bots for existing tenants ──────────────────────────────────
	adminAPI.ReconnectAllBots(ctx)

	// ── Orphaned job cleanup on startup ──────────────────────────────────────
	if dispatcher != nil {
		activeSessions, orchErr := orch.ActiveSessions(ctx, "")
		if orchErr != nil {
			slog.Warn("gateway: failed to get active sessions for orphan cleanup", "err", orchErr)
		} else {
			activeSet := make(map[string]struct{}, len(activeSessions))
			for _, s := range activeSessions {
				activeSet[s.ID] = struct{}{}
			}
			if orphanErr := dispatcher.CleanupOrphanedJobs(ctx, activeSet); orphanErr != nil {
				slog.Warn("gateway: orphaned job cleanup error", "err", orphanErr)
			}
		}
	}

	adminAddr := cfg.Server.ListenAddr
	if adminAddr == "" {
		adminAddr = ":8081"
	}
	adminSrv := &http.Server{
		Addr:    adminAddr,
		Handler: adminAPI.Handler(),
	}
	var grpcReady, adminReady atomic.Bool

	go func() {
		ln, err := net.Listen("tcp", adminAddr)
		if err != nil {
			slog.Error("admin API listen failed", "addr", adminAddr, "err", err)
			return
		}
		adminReady.Store(true)
		slog.Info("admin API started", "addr", ln.Addr().String())
		if err := adminSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("admin API error", "err", err)
		}
	}()

	// ── Usage tracking + quota enforcement ────────────────────────────────────
	usageStore := usage.NewPostgresStore(pool)
	quotaLookup := func(ctx context.Context, tenantID string) (float64, error) {
		t, err := adminStore.GetTenant(ctx, tenantID)
		if err != nil {
			return 0, err
		}
		return t.MonthlySessionHours, nil
	}
	guardedOrch := usage.NewQuotaGuard(orch, usageStore, quotaLookup)

	callbackBridge := sessionorch.NewCallbackBridge(guardedOrch)
	recordingBridge := usage.NewRecordingBridge(callbackBridge, guardedOrch, usageStore)

	// ── gRPC server (receives worker callbacks) ──────────────────────────────
	grpcAddr := os.Getenv("GLYPHOXA_GRPC_ADDR")
	if grpcAddr == "" {
		grpcAddr = ":50051"
	}

	grpcOpts := observe.GRPCServerOptions()
	if tlsCred, err := observe.GRPCServerCredentials(); err != nil {
		slog.Error("failed to load gRPC TLS credentials", "err", err)
		return 1
	} else if tlsCred != nil {
		grpcOpts = append(grpcOpts, tlsCred)
	}

	gwGRPCServer := grpc.NewServer(grpcOpts...)
	grpctransport.NewGatewayServer(recordingBridge).Register(gwGRPCServer)

	go func() {
		ln, err := net.Listen("tcp", grpcAddr)
		if err != nil {
			slog.Error("gateway gRPC listen failed", "addr", grpcAddr, "err", err)
			return
		}
		grpcReady.Store(true)
		slog.Info("gateway gRPC server started", "addr", ln.Addr().String())
		if err := gwGRPCServer.Serve(ln); err != nil {
			slog.Error("gateway gRPC server error", "err", err)
		}
	}()

	// ── Observability ─────────────────────────────────────────────────────────
	observeSrv := startObserveServer(cfg,
		health.Checker{
			Name: "grpc",
			Check: func(_ context.Context) error {
				if !grpcReady.Load() {
					return fmt.Errorf("gRPC server not listening")
				}
				return nil
			},
		},
		health.Checker{
			Name: "admin_api",
			Check: func(_ context.Context) error {
				if !adminReady.Load() {
					return fmt.Errorf("admin API not listening")
				}
				return nil
			},
		},
	)

	// ── Zombie session + orphan job cleanup ──────────────────────────────────
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if n, err := orch.CleanupZombies(ctx, 45*time.Second); err != nil {
					slog.Warn("zombie cleanup error", "err", err)
				} else if n > 0 {
					slog.Info("cleaned up zombie sessions", "count", n)
				}
				// Clean up sessions stuck in 'pending' beyond the dispatch
				// timeout + buffer. These are sessions where dispatch failed
				// but the transition to 'ended' was missed.
				if n, err := orch.CleanupStalePending(ctx, 3*time.Minute); err != nil {
					slog.Warn("stale pending cleanup error", "err", err)
				} else if n > 0 {
					slog.Info("cleaned up stale pending sessions", "count", n)
				}
				// Clean up orphaned K8s Jobs alongside zombie sessions.
				if dispatcher != nil {
					activeSessions, orchErr := orch.ActiveSessions(ctx, "")
					if orchErr == nil {
						activeSet := make(map[string]struct{}, len(activeSessions))
						for _, s := range activeSessions {
							activeSet[s.ID] = struct{}{}
						}
						if orphanErr := dispatcher.CleanupOrphanedJobs(ctx, activeSet); orphanErr != nil {
							slog.Warn("orphaned job cleanup error", "err", orphanErr)
						}
					}
				}
			}
		}
	}()

	slog.Info("gateway ready — press Ctrl+C to shut down",
		"mode", "gateway",
		"admin_addr", adminAddr,
		"grpc_addr", grpcAddr,
		"dispatcher", dispatcher != nil,
	)

	<-ctx.Done()

	// ── Graceful shutdown ─────────────────────────────────────────────────────
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	slog.Info("shutdown signal received, stopping…")

	if dispatcher != nil {
		if err := dispatcher.Cleanup(shutdownCtx); err != nil {
			slog.Warn("dispatcher cleanup error", "err", err)
		}
	}

	gwGRPCServer.GracefulStop()
	if err := adminSrv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("admin API shutdown error", "err", err)
	}
	if err := observeSrv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("observe server shutdown error", "err", err)
	}

	botMgr.Close()

	slog.Info("goodbye")
	return 0
}

// openGatewayPool creates a shared pgxpool.Pool for the gateway database.
func openGatewayPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("gateway: parse database DSN: %w", err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("gateway: create connection pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("gateway: ping database: %w", err)
	}

	slog.Info("database connection established")
	return pool, nil
}

// runWorker runs the worker mode: voice pipeline execution, receiving
// session commands via gRPC from the gateway.
func runWorker(cfg *config.Config) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	printStartupSummary(cfg, "worker")

	// ── Provider registry + instantiation ─────────────────────────────────────
	reg := config.NewRegistry()
	var bot *discordbot.Bot
	registerBuiltinProviders(reg, &bot)

	providers, err := buildProviders(cfg, reg)
	if err != nil {
		slog.Error("failed to build providers", "err", err)
		return 1
	}

	// ── GatewayCallback (worker → gateway heartbeats and state reports) ───────
	gwAddr := os.Getenv("GLYPHOXA_GATEWAY_ADDR")
	var callback gw.GatewayCallback
	if gwAddr != "" {
		clientCred, err := observe.GRPCClientCredentials()
		if err != nil {
			slog.Error("failed to load gRPC client TLS credentials", "err", err)
			return 1
		}
		dialOpts := append(observe.GRPCDialOptions(), clientCred)
		gwConn, err := grpc.NewClient(gwAddr, dialOpts...)
		if err != nil {
			slog.Error("failed to connect to gateway", "addr", gwAddr, "err", err)
			return 1
		}
		defer gwConn.Close()
		callback = grpctransport.NewGatewayClient(gwConn)
		slog.Info("connected to gateway", "addr", gwAddr)
	} else {
		slog.Warn("GLYPHOXA_GATEWAY_ADDR not set — worker will not send heartbeats")
	}

	// ── MCP Host (shared across sessions) ─────────────────────────────────────
	mcpHost, err := initWorkerMCPHost(ctx, cfg)
	if err != nil {
		slog.Error("failed to initialise MCP host", "err", err)
		return 1
	}
	defer func() {
		if err := mcpHost.Close(); err != nil {
			slog.Warn("MCP host close error", "err", err)
		}
	}()

	// ── WorkerHandler ─────────────────────────────────────────────────────────
	wf := &workerFactory{
		cfg:       cfg,
		providers: providers,
		mcpHost:   mcpHost,
	}

	handler := session.NewWorkerHandler(wf.CreateRuntime, callback)

	// ── gRPC server (receives gateway commands) ──────────────────────────────
	grpcAddr := os.Getenv("GLYPHOXA_GRPC_ADDR")
	if grpcAddr == "" {
		grpcAddr = ":50051"
	}
	// Track server readiness with an atomic flag for health checks.
	var workerGRPCReady atomic.Bool

	wkGRPCOpts := observe.GRPCServerOptions()
	if tlsCred, err := observe.GRPCServerCredentials(); err != nil {
		slog.Error("failed to load gRPC TLS credentials", "err", err)
		return 1
	} else if tlsCred != nil {
		wkGRPCOpts = append(wkGRPCOpts, tlsCred)
	}

	wkGRPCServer := grpc.NewServer(wkGRPCOpts...)
	grpctransport.NewWorkerServer(handler).Register(wkGRPCServer)

	go func() {
		ln, err := net.Listen("tcp", grpcAddr)
		if err != nil {
			slog.Error("worker gRPC listen failed", "addr", grpcAddr, "err", err)
			return
		}
		workerGRPCReady.Store(true)
		slog.Info("worker gRPC server started", "addr", ln.Addr().String())
		if err := wkGRPCServer.Serve(ln); err != nil {
			slog.Error("worker gRPC server error", "err", err)
		}
	}()

	// ── Heartbeat goroutine ──────────────────────────────────────────────────
	if callback != nil {
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					for _, id := range handler.ActiveSessionIDs() {
						if err := callback.Heartbeat(ctx, id); err != nil {
							slog.Warn("heartbeat failed", "session_id", id, "err", err)
						}
					}
				}
			}
		}()
	}

	// ── Observability ─────────────────────────────────────────────────────────
	observeSrv := startObserveServer(cfg,
		health.Checker{
			Name: "grpc",
			Check: func(_ context.Context) error {
				if !workerGRPCReady.Load() {
					return fmt.Errorf("gRPC server not listening")
				}
				return nil
			},
		},
	)

	slog.Info("worker ready — waiting for gRPC commands",
		"mode", "worker",
		"grpc_addr", grpcAddr,
	)

	<-ctx.Done()

	// ── Graceful shutdown ─────────────────────────────────────────────────────
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	slog.Info("shutdown signal received, stopping…")

	wkGRPCServer.GracefulStop()
	handler.StopAll(shutdownCtx)

	if err := observeSrv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("observe server shutdown error", "err", err)
	}

	slog.Info("goodbye")
	return 0
}

// runMCPGateway runs the MCP gateway mode: a shared MCP server over Streamable
// HTTP that hosts stateless tools (dice, rules, external MCP servers) for all
// worker pods.
func runMCPGateway(cfg *config.Config) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	printStartupSummary(cfg, "mcp-gateway")

	// ── MCP Host with stateless tools ────────────────────────────────────────
	host := mcphost.New()

	if err := mcphost.RegisterStatelessTools(host); err != nil {
		slog.Error("failed to register stateless tools", "err", err)
		return 1
	}

	// Register external MCP servers from config.
	for _, srv := range cfg.MCP.Servers {
		serverCfg := mcp.ServerConfig{
			Name:      srv.Name,
			Transport: srv.Transport,
			Command:   srv.Command,
			URL:       srv.URL,
			Env:       srv.Env,
		}
		if err := host.RegisterServer(ctx, serverCfg); err != nil {
			slog.Error("failed to register MCP server", "name", srv.Name, "err", err)
			return 1
		}
		slog.Info("registered MCP server", "name", srv.Name)
	}

	if err := host.Calibrate(ctx); err != nil {
		slog.Warn("MCP calibration failed, using declared latencies", "err", err)
	}

	// ── MCP SDK Server ───────────────────────────────────────────────────────
	mcpServer := mcpsdk.NewServer(
		&mcpsdk.Implementation{Name: "glyphoxa-mcp-gateway", Version: "1.0.0"},
		nil,
	)

	// Register each tool from the host on the MCP SDK server.
	allTools := host.AvailableTools(mcp.BudgetDeep)
	for _, toolDef := range allTools {
		toolName := toolDef.Name
		mcpTool := &mcpsdk.Tool{
			Name:        toolDef.Name,
			Description: toolDef.Description,
			InputSchema: toolDef.Parameters,
		}
		if mcpTool.InputSchema == nil {
			mcpTool.InputSchema = map[string]any{"type": "object"}
		}

		mcpServer.AddTool(mcpTool, func(ctx context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			argsJSON := "{}"
			if req.Params.Arguments != nil {
				data, err := json.Marshal(req.Params.Arguments)
				if err != nil {
					return nil, fmt.Errorf("mcp-gateway: marshal tool args: %w", err)
				}
				argsJSON = string(data)
			}

			result, err := host.ExecuteTool(ctx, toolName, argsJSON)
			if err != nil {
				return nil, err
			}

			callResult := &mcpsdk.CallToolResult{
				Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: result.Content}},
				IsError: result.IsError,
			}
			return callResult, nil
		})
	}

	slog.Info("registered MCP tools on gateway", "count", len(allTools))

	// ── HTTP Server (MCP Streamable HTTP) ────────────────────────────────────
	mcpHandler := mcpsdk.NewStreamableHTTPHandler(func(_ *http.Request) *mcpsdk.Server {
		return mcpServer
	}, nil)

	mcpAddr := os.Getenv("GLYPHOXA_MCP_ADDR")
	if mcpAddr == "" {
		mcpAddr = ":8080"
	}

	mcpMux := http.NewServeMux()
	mcpMux.Handle("/mcp", mcpHandler)

	mcpSrv := &http.Server{
		Addr:    mcpAddr,
		Handler: mcpMux,
	}
	go func() {
		ln, err := net.Listen("tcp", mcpAddr)
		if err != nil {
			slog.Error("MCP HTTP listen failed", "addr", mcpAddr, "err", err)
			return
		}
		slog.Info("MCP gateway started", "addr", ln.Addr().String())
		if err := mcpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("MCP HTTP server error", "err", err)
		}
	}()

	// ── Observability ────────────────────────────────────────────────────────
	observeSrv := startObserveServer(cfg)

	slog.Info("mcp-gateway ready — press Ctrl+C to shut down",
		"mode", "mcp-gateway",
		"mcp_addr", mcpAddr,
	)

	<-ctx.Done()

	// ── Graceful shutdown ────────────────────────────────────────────────────
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	slog.Info("shutdown signal received, stopping…")

	if err := mcpSrv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("MCP HTTP server shutdown error", "err", err)
	}
	if err := observeSrv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("observe server shutdown error", "err", err)
	}
	if err := host.Close(); err != nil {
		slog.Warn("MCP host close error", "err", err)
	}

	slog.Info("goodbye")
	return 0
}

// startObserveServer creates and starts the observability HTTP server
// (/healthz, /readyz, /metrics) on a background goroutine. Returns the
// server for graceful shutdown.
func startObserveServer(cfg *config.Config, checks ...health.Checker) *http.Server {
	observeAddr := cfg.Server.ObserveAddr
	if observeAddr == "" {
		observeAddr = ":9090"
	}
	observeMux := http.NewServeMux()

	healthHandler := health.New(checks...)
	healthHandler.Register(observeMux)

	observeMux.Handle("GET /metrics", promhttp.Handler())

	srv := &http.Server{
		Addr:    observeAddr,
		Handler: observeMux,
	}
	go func() {
		ln, err := net.Listen("tcp", observeAddr)
		if err != nil {
			slog.Error("observe server listen failed", "addr", observeAddr, "err", err)
			return
		}
		slog.Info("observe server started", "addr", ln.Addr().String())
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("observe server error", "err", err)
		}
	}()
	return srv
}

// ── Provider wiring ───────────────────────────────────────────────────────────

// registerBuiltinProviders wires all built-in provider factories into reg.
// Each factory receives a config.ProviderEntry and constructs the appropriate
// provider from the real implementation packages.
//
// bot is populated by the "discord" audio factory when it creates a Discord bot.
// The caller can check *bot != nil after buildProviders to determine whether
// Discord slash commands should be registered.
func registerBuiltinProviders(reg *config.Registry, bot **discordbot.Bot) {
	// ── LLM ───────────────────────────────────────────────────────────────────
	// openai, anthropic, gemini, deepseek, mistral, groq, llamacpp, llamafile
	// all share the same pattern: optional APIKey + optional BaseURL.
	for _, providerName := range []string{
		"openai", "anthropic", "gemini",
		"deepseek", "mistral", "groq", "llamacpp", "llamafile",
	} {
		reg.RegisterLLM(providerName, func(entry config.ProviderEntry) (llm.Provider, error) {
			var opts []anyllmlib.Option
			if entry.APIKey != "" {
				opts = append(opts, anyllmlib.WithAPIKey(entry.APIKey))
			}
			if entry.BaseURL != "" {
				opts = append(opts, anyllmlib.WithBaseURL(entry.BaseURL))
			}
			p, err := anyllm.New(providerName, entry.Model, opts...)
			if err != nil {
				return nil, err
			}
			return p, nil
		})
	}

	// ollama is a local server; it uses BaseURL for the address, not an API key.
	reg.RegisterLLM("ollama", func(entry config.ProviderEntry) (llm.Provider, error) {
		var opts []anyllmlib.Option
		if entry.BaseURL != "" {
			opts = append(opts, anyllmlib.WithBaseURL(entry.BaseURL))
		}
		p, err := anyllm.New("ollama", entry.Model, opts...)
		if err != nil {
			return nil, err
		}
		return p, nil
	})

	// ── STT ───────────────────────────────────────────────────────────────────

	reg.RegisterSTT("deepgram", func(entry config.ProviderEntry) (stt.Provider, error) {
		var opts []deepgram.Option
		if entry.Model != "" {
			opts = append(opts, deepgram.WithModel(entry.Model))
		}
		if lang := optString(entry.Options, "language"); lang != "" {
			opts = append(opts, deepgram.WithLanguage(lang))
		}
		return deepgram.New(entry.APIKey, opts...)
	})

	reg.RegisterSTT("whisper", func(entry config.ProviderEntry) (stt.Provider, error) {
		var opts []whisper.Option
		if entry.Model != "" {
			opts = append(opts, whisper.WithModel(entry.Model))
		}
		if lang := optString(entry.Options, "language"); lang != "" {
			opts = append(opts, whisper.WithLanguage(lang))
		}
		return whisper.New(entry.BaseURL, opts...)
	})

	reg.RegisterSTT("whisper-native", func(entry config.ProviderEntry) (stt.Provider, error) {
		modelPath := entry.Model
		if modelPath == "" {
			modelPath = optString(entry.Options, "model_path")
		}
		var opts []whisper.NativeOption
		if lang := optString(entry.Options, "language"); lang != "" {
			opts = append(opts, whisper.WithNativeLanguage(lang))
		}
		return whisper.NewNative(modelPath, opts...)
	})

	reg.RegisterSTT("elevenlabs", func(entry config.ProviderEntry) (stt.Provider, error) {
		var opts []elevenlabsstt.Option
		if entry.Model != "" {
			opts = append(opts, elevenlabsstt.WithModel(entry.Model))
		}
		if lang := optString(entry.Options, "language"); lang != "" {
			opts = append(opts, elevenlabsstt.WithLanguage(lang))
		}
		return elevenlabsstt.New(entry.APIKey, opts...)
	})

	// ── TTS ───────────────────────────────────────────────────────────────────

	reg.RegisterTTS("elevenlabs", func(entry config.ProviderEntry) (tts.Provider, error) {
		var opts []elevenlabs.Option
		if entry.Model != "" {
			opts = append(opts, elevenlabs.WithModel(entry.Model))
		}
		if outputFmt := optString(entry.Options, "output_format"); outputFmt != "" {
			opts = append(opts, elevenlabs.WithOutputFormat(outputFmt))
		}
		return elevenlabs.New(entry.APIKey, opts...)
	})

	reg.RegisterTTS("coqui", func(entry config.ProviderEntry) (tts.Provider, error) {
		var opts []coqui.Option
		if lang := optString(entry.Options, "language"); lang != "" {
			opts = append(opts, coqui.WithLanguage(lang))
		}
		if mode := optString(entry.Options, "api_mode"); mode != "" {
			opts = append(opts, coqui.WithAPIMode(coqui.APIMode(mode)))
		}
		return coqui.New(entry.BaseURL, opts...)
	})

	// ── Embeddings ────────────────────────────────────────────────────────────

	reg.RegisterEmbeddings("openai", func(entry config.ProviderEntry) (embeddings.Provider, error) {
		var opts []oaembed.Option
		if entry.BaseURL != "" {
			opts = append(opts, oaembed.WithBaseURL(entry.BaseURL))
		}
		return oaembed.New(entry.APIKey, entry.Model, opts...)
	})

	reg.RegisterEmbeddings("ollama", func(entry config.ProviderEntry) (embeddings.Provider, error) {
		return ollamaembed.New(entry.BaseURL, entry.Model)
	})

	// ── S2S ───────────────────────────────────────────────────────────────────

	reg.RegisterS2S("openai-realtime", func(entry config.ProviderEntry) (s2s.Provider, error) {
		var opts []oais2s.Option
		if entry.Model != "" {
			opts = append(opts, oais2s.WithModel(entry.Model))
		}
		if entry.BaseURL != "" {
			opts = append(opts, oais2s.WithBaseURL(entry.BaseURL))
		}
		return oais2s.New(entry.APIKey, opts...), nil
	})

	reg.RegisterS2S("gemini-live", func(entry config.ProviderEntry) (s2s.Provider, error) {
		var opts []geminilive.Option
		if entry.Model != "" {
			opts = append(opts, geminilive.WithModel(entry.Model))
		}
		if entry.BaseURL != "" {
			opts = append(opts, geminilive.WithBaseURL(entry.BaseURL))
		}
		return geminilive.New(entry.APIKey, opts...), nil
	})

	// ── VAD ───────────────────────────────────────────────────────────────────

	reg.RegisterVAD("energy", func(entry config.ProviderEntry) (vad.Engine, error) {
		var opts []energyvad.Option
		if n := optInt(entry.Options, "min_speech_frames"); n > 0 {
			opts = append(opts, energyvad.WithMinSpeechFrames(n))
		}
		if n := optInt(entry.Options, "min_silence_frames"); n > 0 {
			opts = append(opts, energyvad.WithMinSilenceFrames(n))
		}
		if n := optInt(entry.Options, "min_speech_duration_frames"); n > 0 {
			opts = append(opts, energyvad.WithMinSpeechDurationFrames(n))
		}
		if f := optFloat(entry.Options, "smoothing_factor"); f > 0 {
			opts = append(opts, energyvad.WithSmoothingFactor(f))
		}
		return energyvad.New(opts...), nil
	})

	reg.RegisterVAD("silero", func(entry config.ProviderEntry) (vad.Engine, error) {
		modelPath := entry.Model
		if modelPath == "" {
			modelPath = optString(entry.Options, "model_path")
		}
		if modelPath == "" {
			return nil, fmt.Errorf("silero VAD requires model (path to silero_vad.onnx)")
		}
		var opts []silerovad.Option
		if n := optInt(entry.Options, "min_speech_frames"); n > 0 {
			opts = append(opts, silerovad.WithMinSpeechFrames(n))
		}
		if n := optInt(entry.Options, "min_silence_frames"); n > 0 {
			opts = append(opts, silerovad.WithMinSilenceFrames(n))
		}
		if p := optString(entry.Options, "onnx_lib_path"); p != "" {
			opts = append(opts, silerovad.WithONNXLibPath(p))
		}
		return silerovad.New(modelPath, opts...)
	})

	// ── Audio ─────────────────────────────────────────────────────────────────

	reg.RegisterAudio("discord", func(entry config.ProviderEntry) (audio.Platform, error) {
		token := entry.APIKey
		if token == "" {
			return nil, fmt.Errorf("discord audio provider requires api_key (bot token)")
		}
		guildID := optString(entry.Options, "guild_id")
		if guildID == "" {
			return nil, fmt.Errorf("discord audio provider requires options.guild_id")
		}
		dmRoleID := optString(entry.Options, "dm_role_id")

		b, err := discordbot.New(context.Background(), discordbot.Config{
			Token:    token,
			GuildID:  guildID,
			DMRoleID: dmRoleID,
			VoiceOpts: []voice.ManagerConfigOpt{
				voice.WithDaveSessionCreateFunc(golibdave.NewSession),
			},
		})
		if err != nil {
			return nil, err
		}
		*bot = b
		slog.Info("discord bot connected", "guild_id", guildID)
		return b.Platform(), nil
	})

	reg.RegisterAudio("webrtc", func(entry config.ProviderEntry) (audio.Platform, error) {
		var opts []webrtc.Option
		if servers := optStringSlice(entry.Options, "stun_servers"); len(servers) > 0 {
			opts = append(opts, webrtc.WithSTUNServers(servers...))
		}
		if rate := optInt(entry.Options, "sample_rate"); rate > 0 {
			opts = append(opts, webrtc.WithSampleRate(rate))
		}
		return webrtc.New(opts...), nil
	})

	// Debug log of all registered providers.
	for kind, names := range config.ValidProviderNames {
		for _, name := range names {
			slog.Debug("registered provider", "kind", kind, "name", name)
		}
	}
}

// buildProviders instantiates all providers named in cfg using the registry
// and returns them in an [app.Providers] struct for the application to consume.
func buildProviders(cfg *config.Config, reg *config.Registry) (*app.Providers, error) {
	ps := &app.Providers{}

	if name := cfg.Providers.LLM.Name; name != "" {
		p, err := reg.CreateLLM(cfg.Providers.LLM)
		if errors.Is(err, config.ErrProviderNotRegistered) {
			slog.Debug("provider not yet implemented — skipping", "kind", "llm", "name", name)
		} else if err != nil {
			return nil, fmt.Errorf("create llm provider %q: %w", name, err)
		} else {
			ps.LLM = p
			slog.Info("provider created", "kind", "llm", "name", name)
		}
	}

	if name := cfg.Providers.STT.Name; name != "" {
		p, err := reg.CreateSTT(cfg.Providers.STT)
		if errors.Is(err, config.ErrProviderNotRegistered) {
			slog.Debug("provider not yet implemented — skipping", "kind", "stt", "name", name)
		} else if err != nil {
			return nil, fmt.Errorf("create stt provider %q: %w", name, err)
		} else {
			ps.STT = p
			slog.Info("provider created", "kind", "stt", "name", name)
		}
	}

	if name := cfg.Providers.TTS.Name; name != "" {
		p, err := reg.CreateTTS(cfg.Providers.TTS)
		if errors.Is(err, config.ErrProviderNotRegistered) {
			slog.Debug("provider not yet implemented — skipping", "kind", "tts", "name", name)
		} else if err != nil {
			return nil, fmt.Errorf("create tts provider %q: %w", name, err)
		} else {
			ps.TTS = p
			slog.Info("provider created", "kind", "tts", "name", name)
		}
	}

	if name := cfg.Providers.S2S.Name; name != "" {
		p, err := reg.CreateS2S(cfg.Providers.S2S)
		if errors.Is(err, config.ErrProviderNotRegistered) {
			slog.Debug("provider not yet implemented — skipping", "kind", "s2s", "name", name)
		} else if err != nil {
			return nil, fmt.Errorf("create s2s provider %q: %w", name, err)
		} else {
			ps.S2S = p
			slog.Info("provider created", "kind", "s2s", "name", name)
		}
	}

	if name := cfg.Providers.Embeddings.Name; name != "" {
		p, err := reg.CreateEmbeddings(cfg.Providers.Embeddings)
		if errors.Is(err, config.ErrProviderNotRegistered) {
			slog.Debug("provider not yet implemented — skipping", "kind", "embeddings", "name", name)
		} else if err != nil {
			return nil, fmt.Errorf("create embeddings provider %q: %w", name, err)
		} else {
			ps.Embeddings = p
			slog.Info("provider created", "kind", "embeddings", "name", name)
		}
	}

	if name := cfg.Providers.VAD.Name; name != "" {
		p, err := reg.CreateVAD(cfg.Providers.VAD)
		if errors.Is(err, config.ErrProviderNotRegistered) {
			slog.Debug("provider not yet implemented — skipping", "kind", "vad", "name", name)
		} else if err != nil {
			return nil, fmt.Errorf("create vad provider %q: %w", name, err)
		} else {
			ps.VAD = p
			slog.Info("provider created", "kind", "vad", "name", name)
		}
	}

	if name := cfg.Providers.Audio.Name; name != "" {
		p, err := reg.CreateAudio(cfg.Providers.Audio)
		if errors.Is(err, config.ErrProviderNotRegistered) {
			slog.Debug("provider not yet implemented — skipping", "kind", "audio", "name", name)
		} else if err != nil {
			return nil, fmt.Errorf("create audio provider %q: %w", name, err)
		} else {
			ps.Audio = p
			slog.Info("provider created", "kind", "audio", "name", name)
		}
	}

	return ps, nil
}

// ── Startup summary ───────────────────────────────────────────────────────────

func printStartupSummary(cfg *config.Config, mode string) {
	fmt.Println("╔═══════════════════════════════════════╗")
	fmt.Println("║         Glyphoxa — startup summary    ║")
	fmt.Println("╠═══════════════════════════════════════╣")
	fmt.Printf("║  Mode           : %-19s ║\n", mode)
	printProvider("LLM", cfg.Providers.LLM.Name, cfg.Providers.LLM.Model)
	printProvider("STT", cfg.Providers.STT.Name, cfg.Providers.STT.Model)
	printProvider("TTS", cfg.Providers.TTS.Name, cfg.Providers.TTS.Model)
	printProvider("S2S", cfg.Providers.S2S.Name, cfg.Providers.S2S.Model)
	printProvider("Embeddings", cfg.Providers.Embeddings.Name, cfg.Providers.Embeddings.Model)
	printProvider("VAD", cfg.Providers.VAD.Name, "")
	printProvider("Audio", cfg.Providers.Audio.Name, "")
	fmt.Printf("║  NPCs configured : %-19d ║\n", len(cfg.NPCs))
	fmt.Printf("║  MCP servers     : %-19d ║\n", len(cfg.MCP.Servers))
	if cfg.Server.ListenAddr != "" {
		fmt.Printf("║  Listen addr     : %-19s ║\n", cfg.Server.ListenAddr)
	}
	fmt.Println("╚═══════════════════════════════════════╝")
}

func printProvider(kind, name, model string) {
	value := name
	if value == "" {
		value = "(not configured)"
	} else if model != "" {
		value = name + " / " + model
	}
	if len(value) > 19 {
		value = value[:16] + "…"
	}
	fmt.Printf("║  %-12s    : %-19s ║\n", kind, value)
}

// ── Logger ─────────────────────────────────────────────────────────────────────

func newLogger(level config.LogLevel) *slog.Logger {
	var lvl slog.Level
	switch level {
	case config.LogDebug:
		lvl = slog.LevelDebug
	case config.LogWarn:
		lvl = slog.LevelWarn
	case config.LogError:
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// optString extracts a string value from a provider Options map[string]any.
// Returns "" if the map is nil, the key is absent, or the value is not a string.
func optString(opts map[string]any, key string) string {
	if opts == nil {
		return ""
	}
	v, ok := opts[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

// optInt extracts an integer option from the provider Options map.
// Returns 0 if the key is absent or the value is not numeric.
func optInt(opts map[string]any, key string) int {
	if opts == nil {
		return 0
	}
	v, ok := opts[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n) // YAML numbers decode as float64
	default:
		return 0
	}
}

// optStringSlice extracts a []string option from the provider Options map.
// YAML sequences of strings are decoded as []any; each element is asserted to string.
// Returns nil if the key is absent, the value is not a slice, or any element is not a string.
func optStringSlice(opts map[string]any, key string) []string {
	if opts == nil {
		return nil
	}
	v, ok := opts[key]
	if !ok {
		return nil
	}
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, elem := range raw {
		s, ok := elem.(string)
		if !ok {
			return nil
		}
		out = append(out, s)
	}
	return out
}

// optFloat extracts a float64 option from the provider Options map.
// Returns 0 if the key is absent or the value is not numeric.
func optFloat(opts map[string]any, key string) float64 {
	if opts == nil {
		return 0
	}
	v, ok := opts[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	default:
		return 0
	}
}
