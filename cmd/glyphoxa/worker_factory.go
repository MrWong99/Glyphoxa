package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/MrWong99/glyphoxa/internal/agent"
	"github.com/MrWong99/glyphoxa/internal/agent/orchestrator"
	"github.com/MrWong99/glyphoxa/internal/app"
	"github.com/MrWong99/glyphoxa/internal/config"
	"github.com/MrWong99/glyphoxa/internal/engine"
	gw "github.com/MrWong99/glyphoxa/internal/gateway"
	"github.com/MrWong99/glyphoxa/internal/hotctx"
	"github.com/MrWong99/glyphoxa/internal/mcp"
	"github.com/MrWong99/glyphoxa/internal/mcp/mcphost"
	"github.com/MrWong99/glyphoxa/internal/session"
	"github.com/MrWong99/glyphoxa/internal/transcript"
	"github.com/MrWong99/glyphoxa/internal/transcript/phonetic"
	"github.com/MrWong99/glyphoxa/pkg/audio"
	"github.com/MrWong99/glyphoxa/pkg/audio/discord"
	"github.com/MrWong99/glyphoxa/pkg/audio/grpcbridge"
	audiomixer "github.com/MrWong99/glyphoxa/pkg/audio/mixer"

	pb "github.com/MrWong99/glyphoxa/gen/glyphoxa/v1"
	"github.com/MrWong99/glyphoxa/pkg/memory"
	"github.com/MrWong99/glyphoxa/pkg/memory/postgres"
	"github.com/disgoorg/disgo/voice"

	safedave "github.com/MrWong99/glyphoxa/internal/dave"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure" // used in fallback when no dial opts are configured
)

// workerConsolidationInterval is the consolidation period for worker sessions.
const workerConsolidationInterval = 5 * time.Minute

// workerFactory creates fully wired Runtime instances for the worker.
// It holds shared, long-lived dependencies (providers, config, MCP host)
// that are initialised once at worker startup and reused across sessions.
type workerFactory struct {
	cfg             *config.Config
	providers       *app.Providers
	mcpHost         mcp.Host
	audioBridgeAddr string            // gateway AudioBridgeService address (empty in full mode)
	grpcDialOpts    []grpc.DialOption // shared dial options (TLS, interceptors) for gateway connections
}

// CreateRuntime builds a fully wired session.Runtime from a StartSessionRequest.
// Each call creates per-session resources (Discord connection, memory store,
// mixer, agents, audio pipeline, consolidator) and registers closers on the
// Runtime for ordered teardown.
func (wf *workerFactory) CreateRuntime(ctx context.Context, req gw.StartSessionRequest) (*session.Runtime, error) {
	// ── 1. Tenant context ────────────────────────────────────────────────────
	licenseTier, err := config.ParseLicenseTier(req.LicenseTier)
	if err != nil {
		licenseTier = config.TierShared
	}
	tenant := config.TenantContext{
		TenantID:    req.TenantID,
		LicenseTier: licenseTier,
		CampaignID:  req.CampaignID,
		GuildID:     req.GuildID,
		SchemaName:  fmt.Sprintf("tenant_%s", req.TenantID),
	}

	sessionCtx := config.WithTenant(ctx, tenant)

	slog.Info("worker: creating runtime",
		"session_id", req.SessionID,
		"tenant_id", req.TenantID,
		"guild_id", req.GuildID,
		"channel_id", req.ChannelID,
		"npcs", len(req.NPCConfigs),
	)

	// ── 2. Per-session memory store ──────────────────────────────────────────
	dsn := os.Getenv("GLYPHOXA_DATABASE_DSN")
	if dsn == "" {
		dsn = wf.cfg.Memory.PostgresDSN
	}
	if dsn != "" {
		dsn = applySSLMode(dsn)
	}

	var sessionStore memory.SessionStore
	var semanticIndex memory.SemanticIndex
	var graph memory.KnowledgeGraph
	var storeCloser func() error

	if dsn != "" {
		dims := wf.cfg.Memory.EmbeddingDimensions
		if dims == 0 {
			dims = 1536
		}
		schema, err := postgres.NewSchemaName(tenant.SchemaName)
		if err != nil {
			return nil, fmt.Errorf("worker: schema name: %w", err)
		}
		campaignID := tenant.CampaignID
		if campaignID == "" {
			campaignID = "default"
		}
		store, err := postgres.NewStore(sessionCtx, dsn, dims, schema, campaignID)
		if err != nil {
			return nil, fmt.Errorf("worker: init memory store: %w", err)
		}
		sessionStore = store.L1()
		semanticIndex = store.L2()
		graph = store
		storeCloser = func() error {
			store.Close()
			return nil
		}
	}

	// ── 3. Voice connection ─────────────────────────────────────────────────
	// In distributed mode the gateway owns the Discord voice connection and
	// streams opus frames via the AudioBridgeService gRPC stream. The worker
	// connects to that stream as its audio.Connection. In full mode the worker
	// opens its own Discord gateway for voice.
	if req.BotToken == "" {
		if storeCloser != nil {
			_ = storeCloser()
		}
		return nil, fmt.Errorf("worker: bot_token required in StartSessionRequest")
	}

	var voicePlatformCloser func() error
	var conn audio.Connection

	if wf.audioBridgeAddr != "" {
		// Distributed mode: connect to the gateway's AudioBridgeService.
		bridgeConn, bridgeConnCloser, err := wf.connectAudioBridge(sessionCtx, req.SessionID)
		if err != nil {
			if storeCloser != nil {
				_ = storeCloser()
			}
			return nil, fmt.Errorf("worker: connect audio bridge: %w", err)
		}
		conn = bridgeConn
		voicePlatformCloser = bridgeConnCloser
	} else {
		// Full mode: open own gateway (existing code path).
		platform, err := discord.NewVoiceOnlyPlatform(sessionCtx, req.BotToken, req.GuildID,
			discord.WithVoiceManagerOpts(voice.WithDaveSessionCreateFunc(safedave.NewSession)),
		)
		if err != nil {
			if storeCloser != nil {
				_ = storeCloser()
			}
			return nil, fmt.Errorf("worker: create voice platform: %w", err)
		}

		conn, err = platform.Connect(sessionCtx, req.ChannelID)
		if err != nil {
			_ = platform.Close()
			if storeCloser != nil {
				_ = storeCloser()
			}
			return nil, fmt.Errorf("worker: connect to voice channel %s: %w", req.ChannelID, err)
		}
		voicePlatformCloser = platform.Close
	}

	// ── 4. Mixer ─────────────────────────────────────────────────────────────
	outStream := conn.OutputStream()
	pm := audiomixer.New(func(frame audio.AudioFrame) {
		outStream <- frame
	})

	// ── 5. Resolve NPC configs ───────────────────────────────────────────────
	npcs := npcConfigsFromRequest(req, wf.cfg)

	if len(npcs) == 0 {
		_ = pm.Close()
		_ = conn.Disconnect()
		_ = voicePlatformCloser()
		if storeCloser != nil {
			_ = storeCloser()
		}
		return nil, fmt.Errorf("worker: no NPC configs provided (gRPC or config.yaml fallback)")
	}

	// ── 6. Hot context assembler ─────────────────────────────────────────────
	var assemblerOpts []hotctx.Option
	if graph != nil {
		pf := hotctx.NewPreFetcher(graph)
		if err := pf.RefreshEntityList(sessionCtx); err != nil {
			slog.Warn("worker: pre-fetcher entity refresh failed", "err", err)
		} else {
			assemblerOpts = append(assemblerOpts, hotctx.WithPreFetcher(pf))
		}
	}
	assembler := hotctx.NewAssembler(sessionStore, graph, assemblerOpts...)

	// ── 7. Load agents ───────────────────────────────────────────────────────
	var loaderOpts []agent.LoaderOption
	if wf.mcpHost != nil {
		loaderOpts = append(loaderOpts, agent.WithMCPHost(wf.mcpHost))
	}
	loaderOpts = append(loaderOpts, agent.WithMixer(pm))

	if wf.providers.TTS != nil {
		loaderOpts = append(loaderOpts, agent.WithTTS(wf.providers.TTS))
		sr, ch := app.TTSFormatFromConfig(wf.cfg.Providers.TTS)
		loaderOpts = append(loaderOpts, agent.WithTTSFormat(sr, ch))
	}

	if sessionStore != nil {
		loaderOpts = append(loaderOpts, agent.WithOnTranscript(func(entry memory.TranscriptEntry) {
			if err := sessionStore.WriteEntry(context.Background(), req.SessionID, entry); err != nil {
				slog.Warn("worker: failed to record puppet transcript",
					"npc", entry.SpeakerName,
					"err", err,
				)
			}
		}))
	}

	loader, err := agent.NewLoader(assembler, req.SessionID, loaderOpts...)
	if err != nil {
		_ = pm.Close()
		_ = conn.Disconnect()
		_ = voicePlatformCloser()
		if storeCloser != nil {
			_ = storeCloser()
		}
		return nil, fmt.Errorf("worker: create agent loader: %w", err)
	}

	var agents []agent.NPCAgent
	var engineClosers []func() error

	for i, npc := range npcs {
		eng, err := app.BuildEngine(wf.providers, npc, wf.cfg.Providers.TTS)
		if err != nil {
			for j := len(engineClosers) - 1; j >= 0; j-- {
				_ = engineClosers[j]()
			}
			_ = pm.Close()
			_ = conn.Disconnect()
			_ = voicePlatformCloser()
			if storeCloser != nil {
				_ = storeCloser()
			}
			return nil, fmt.Errorf("worker: build engine for NPC %q (index %d): %w", npc.Name, i, err)
		}
		engineClosers = append(engineClosers, eng.Close)

		identity := app.IdentityFromConfig(npc)
		npcID := fmt.Sprintf("npc-%d-%s", i, npc.Name)
		tier := app.ConfigBudgetTier(npc.BudgetTier)

		ag, err := loader.Load(npcID, identity, eng, tier)
		if err != nil {
			for j := len(engineClosers) - 1; j >= 0; j-- {
				_ = engineClosers[j]()
			}
			_ = pm.Close()
			_ = conn.Disconnect()
			_ = voicePlatformCloser()
			if storeCloser != nil {
				_ = storeCloser()
			}
			return nil, fmt.Errorf("worker: load agent %q: %w", npc.Name, err)
		}
		agents = append(agents, ag)
		slog.Info("worker: loaded NPC agent", "name", npc.Name, "engine", npc.Engine, "tier", tier)
	}

	// Register NPC entities in the knowledge graph.
	if graph != nil {
		app.RegisterNPCEntities(sessionCtx, graph, npcs)
	}

	// ── 8. Orchestrator ──────────────────────────────────────────────────────
	orch := orchestrator.New(agents, orchestrator.WithMixer(pm))

	// ── 9. Audio pipeline (VAD → STT → routing) ─────────────────────────────
	// Cascade engines require the audio pipeline (VAD → STT) to detect
	// and transcribe player speech. Fail early with a clear error instead
	// of silently running a dead session that receives audio but never
	// processes it.
	needsPipeline := false
	for _, npc := range npcs {
		if npc.Engine == config.EngineCascaded || npc.Engine == config.EngineSentenceCascade {
			needsPipeline = true
			break
		}
	}
	if needsPipeline && (wf.providers.VAD == nil || wf.providers.STT == nil) {
		for j := len(engineClosers) - 1; j >= 0; j-- {
			_ = engineClosers[j]()
		}
		_ = pm.Close()
		_ = conn.Disconnect()
		_ = voicePlatformCloser()
		if storeCloser != nil {
			_ = storeCloser()
		}
		return nil, fmt.Errorf("worker: cascade engine requires VAD and STT providers — configure providers.vad and providers.stt in worker config (vad=%v, stt=%v)",
			wf.providers.VAD != nil, wf.providers.STT != nil)
	}

	var pipelineCloser func() error
	if wf.providers.VAD != nil && wf.providers.STT != nil {
		var correctionPipeline transcript.Pipeline
		if wf.providers.LLM != nil || graph != nil {
			var pipelineOpts []transcript.PipelineOption
			pipelineOpts = append(pipelineOpts, transcript.WithPhoneticMatcher(phonetic.New()))
			correctionPipeline = transcript.NewPipeline(pipelineOpts...)
		}

		var entityNamesFn func() []string
		if graph != nil {
			entityNamesFn = func() []string {
				ents, err := graph.FindEntities(context.Background(), memory.EntityFilter{})
				if err != nil {
					slog.Warn("worker: failed to fetch entity names for correction", "err", err)
					return nil
				}
				names := make([]string, len(ents))
				for i, e := range ents {
					names[i] = e.Name
				}
				return names
			}
		}

		pipeline := app.NewAudioPipeline(app.AudioPipelineConfig{
			Conn:        conn,
			VADEngine:   wf.providers.VAD,
			STTProvider: wf.providers.STT,
			Orch:        orch,
			Mixer:       pm,
			VADCfg:      app.VADConfigFromProvider(wf.cfg.Providers.VAD),
			STTCfg:      app.STTConfigFromProvider(wf.cfg.Providers.STT),
			Ctx:         sessionCtx,
			Pipeline:    correctionPipeline,
			Entities:    entityNamesFn,
		})
		pipeline.Start()
		pipelineCloser = pipeline.Stop
	}

	// ── 10. Consolidator ─────────────────────────────────────────────────────
	var consolid *session.Consolidator
	if sessionStore != nil {
		var summariser = session.NoopSummariser()
		if wf.providers.LLM != nil {
			summariser = session.NewLLMSummariser(wf.providers.LLM)
		}
		ctxMgr := session.NewContextManager(session.ContextManagerConfig{
			MaxTokens:      128000,
			ThresholdRatio: 0.75,
			Summariser:     summariser,
		})
		consolid = session.NewConsolidator(session.ConsolidatorConfig{
			Store:         sessionStore,
			ContextMgr:    ctxMgr,
			SessionID:     req.SessionID,
			Interval:      workerConsolidationInterval,
			SemanticIndex: semanticIndex,
			EmbedProvider: wf.providers.Embeddings,
		})
		consolid.Start(sessionCtx)
	}

	// ── 11. Build Runtime ────────────────────────────────────────────────────
	rt := session.NewRuntime(session.RuntimeConfig{
		SessionID:    req.SessionID,
		Agents:       agents,
		Engines:      enginesFromAgents(agents),
		Orchestrator: orch,
		Mixer:        pm,
		Connection:   conn,
		SessionStore: sessionStore,
	})

	// Register closers in correct teardown order:
	// consolidator → pipeline → engines → mixer → connection → platform → store

	if consolid != nil {
		rt.AddCloser(func() error {
			if err := consolid.ConsolidateNow(context.Background()); err != nil {
				slog.Warn("worker: final consolidation error", "session_id", req.SessionID, "err", err)
			}
			consolid.Stop()
			return nil
		})
	}

	if pipelineCloser != nil {
		rt.AddCloser(pipelineCloser)
	}

	for _, c := range engineClosers {
		rt.AddCloser(c)
	}

	rt.AddCloser(pm.Close)

	rt.AddCloser(func() error {
		return conn.Disconnect()
	})

	rt.AddCloser(voicePlatformCloser)

	if storeCloser != nil {
		rt.AddCloser(storeCloser)
	}

	slog.Info("worker: runtime created",
		"session_id", req.SessionID,
		"npcs", len(agents),
		"has_pipeline", pipelineCloser != nil,
		"has_consolidator", consolid != nil,
	)

	return rt, nil
}

// connectAudioBridge dials the gateway's AudioBridgeService and returns a
// grpcbridge.Connection for the given session plus a closer that releases the
// underlying gRPC client connection. The caller must arrange for the closer to
// be called when the session ends (typically via Runtime.AddCloser).
func (wf *workerFactory) connectAudioBridge(ctx context.Context, sessionID string) (*grpcbridge.Connection, func() error, error) {
	// Use the same dial options (TLS, interceptors) as the gateway callback
	// connection. Falling back to insecure when no options are configured
	// keeps the existing behaviour for non-TLS deployments.
	dialOpts := wf.grpcDialOpts
	if len(dialOpts) == 0 {
		dialOpts = []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	}

	conn, err := grpc.NewClient(wf.audioBridgeAddr, dialOpts...)
	if err != nil {
		return nil, nil, fmt.Errorf("worker: dial audio bridge at %s: %w", wf.audioBridgeAddr, err)
	}

	bridgeClient := pb.NewAudioBridgeServiceClient(conn)
	// Use context.Background() because the stream must outlive the
	// StartSession RPC whose server-side context is canceled when the
	// handler returns. Passing the RPC context here would kill the stream
	// within milliseconds of StartSession completing.
	stream, err := bridgeClient.StreamAudio(context.Background())
	if err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("worker: open audio stream: %w", err)
	}

	bridgeConn, err := grpcbridge.New(sessionID, stream)
	if err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("worker: create grpc bridge connection: %w", err)
	}

	slog.Info("worker: connected to audio bridge",
		"session_id", sessionID,
		"bridge_addr", wf.audioBridgeAddr,
	)
	return bridgeConn, conn.Close, nil
}

// npcConfigsFromRequest converts gRPC NPCConfigMsg entries to config.NPCConfig.
// Falls back to config.yaml NPCs if the request carries none.
func npcConfigsFromRequest(req gw.StartSessionRequest, cfg *config.Config) []config.NPCConfig {
	if len(req.NPCConfigs) == 0 {
		slog.Info("worker: no NPCs in gRPC request, falling back to config.yaml",
			"config_npcs", len(cfg.NPCs))
		return cfg.NPCs
	}

	npcs := make([]config.NPCConfig, len(req.NPCConfigs))
	for i, msg := range req.NPCConfigs {
		engine := config.Engine(msg.Engine)
		if !engine.IsValid() {
			engine = config.EngineCascaded
		}
		npcs[i] = config.NPCConfig{
			Name:           msg.Name,
			Personality:    msg.Personality,
			Engine:         engine,
			KnowledgeScope: msg.KnowledgeScope,
			BudgetTier:     config.BudgetTier(msg.BudgetTier),
			GMHelper:       msg.GMHelper,
			AddressOnly:    msg.AddressOnly,
			Voice: config.VoiceConfig{
				VoiceID: msg.VoiceID,
			},
		}
	}
	return npcs
}

// enginesFromAgents extracts the VoiceEngine from each NPCAgent.
func enginesFromAgents(agents []agent.NPCAgent) []engine.VoiceEngine {
	engines := make([]engine.VoiceEngine, len(agents))
	for i, ag := range agents {
		engines[i] = ag.Engine()
	}
	return engines
}

// initWorkerMCPHost creates an MCP host for the worker, registering config
// servers and the shared MCP gateway. This is called once at worker startup;
// the returned host is shared across all sessions.
func initWorkerMCPHost(ctx context.Context, cfg *config.Config) (mcp.Host, error) {
	host := mcphost.New()

	for _, srv := range cfg.MCP.Servers {
		serverCfg := mcp.ServerConfig{
			Name:      srv.Name,
			Transport: srv.Transport,
			Command:   srv.Command,
			URL:       srv.URL,
			Env:       srv.Env,
		}
		if err := host.RegisterServer(ctx, serverCfg); err != nil {
			_ = host.Close()
			return nil, fmt.Errorf("register MCP server %q: %w", srv.Name, err)
		}
		slog.Info("registered MCP server", "name", srv.Name)
	}

	if mcpGatewayURL := os.Getenv("GLYPHOXA_MCP_GATEWAY_URL"); mcpGatewayURL != "" {
		gwCfg := mcp.ServerConfig{
			Name:      "mcp-gateway",
			Transport: mcp.TransportStreamableHTTP,
			URL:       mcpGatewayURL,
		}
		if err := host.RegisterServer(ctx, gwCfg); err != nil {
			_ = host.Close()
			return nil, fmt.Errorf("register mcp-gateway at %s: %w", mcpGatewayURL, err)
		}
		slog.Info("registered shared MCP gateway", "url", mcpGatewayURL)
	}

	if err := host.Calibrate(ctx); err != nil {
		slog.Warn("MCP calibration failed, using declared latencies", "err", err)
	}

	return host, nil
}
