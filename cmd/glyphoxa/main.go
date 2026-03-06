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
	"syscall"
	"time"

	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave/golibdave"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	anyllmlib "github.com/mozilla-ai/any-llm-go"

	"github.com/MrWong99/glyphoxa/internal/app"
	"github.com/MrWong99/glyphoxa/internal/config"
	discordbot "github.com/MrWong99/glyphoxa/internal/discord"
	"github.com/MrWong99/glyphoxa/internal/discord/commands"
	"github.com/MrWong99/glyphoxa/internal/entity"
	"github.com/MrWong99/glyphoxa/internal/feedback"
	"github.com/MrWong99/glyphoxa/internal/health"
	"github.com/MrWong99/glyphoxa/internal/observe"
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

	slog.Info("glyphoxa starting",
		"config", *configPath,
		"listen_addr", cfg.Server.ListenAddr,
		"log_level", cfg.Server.LogLevel,
	)

	// ── Observability ────────────────────────────────────────────────────────
	otelShutdown, err := observe.InitProvider(context.Background(), observe.ProviderConfig{
		ServiceName: "glyphoxa",
	})
	if err != nil {
		slog.Error("failed to initialise observability", "err", err)
		return 1
	}
	defer func() {
		if err := otelShutdown(context.Background()); err != nil {
			slog.Warn("otel shutdown error", "err", err)
		}
	}()

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
	printStartupSummary(cfg)

	application, err := app.New(ctx, cfg, providers)
	if err != nil {
		slog.Error("failed to initialise application", "err", err)
		return 1
	}

	// ── Diagnostics HTTP server (/healthz, /readyz, /metrics) ───────────────
	diagAddr := cfg.Server.DiagnosticsAddr
	if diagAddr == "" {
		diagAddr = ":9090"
	}
	diagMux := http.NewServeMux()

	healthHandler := health.New(application.ReadinessCheckers()...)
	healthHandler.Register(diagMux)
	diagMux.Handle("GET /metrics", promhttp.Handler())

	diagServer := &http.Server{
		Addr:              diagAddr,
		Handler:           observe.Middleware(observe.DefaultMetrics())(diagMux),
		ReadHeaderTimeout: 5 * time.Second,
	}
	diagLn, err := net.Listen("tcp", diagAddr)
	if err != nil {
		slog.Error("failed to listen on diagnostics address", "addr", diagAddr, "err", err)
		return 1
	}
	go func() {
		slog.Info("diagnostics server listening", "addr", diagAddr)
		if err := diagServer.Serve(diagLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("diagnostics server error", "err", err)
		}
	}()

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
		})

		// Session and recap register themselves in the constructor.
		commands.NewSessionCommands(bot, sessionMgr, perms)
		commands.NewRecapCommands(commands.RecapConfig{
			Bot:          bot,
			SessionMgr:   sessionMgr,
			Perms:        perms,
			SessionStore: application.SessionStore(),
		})

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

	slog.Info("server ready — press Ctrl+C to shut down")

	if err := application.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("run error", "err", err)
		return 1
	}

	// ── Graceful shutdown ─────────────────────────────────────────────────────
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	slog.Info("shutdown signal received, stopping…")

	// Shut down the diagnostics server.
	if err := diagServer.Shutdown(shutdownCtx); err != nil {
		slog.Warn("diagnostics server shutdown error", "err", err)
	}

	// Close the Discord bot first (unregister commands, disconnect).
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

func printStartupSummary(cfg *config.Config) {
	fmt.Println("╔═══════════════════════════════════════╗")
	fmt.Println("║         Glyphoxa — startup summary    ║")
	fmt.Println("╠═══════════════════════════════════════╣")
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
	diagDisplay := cfg.Server.DiagnosticsAddr
	if diagDisplay == "" {
		diagDisplay = ":9090"
	}
	fmt.Printf("║  Diagnostics     : %-19s ║\n", diagDisplay)
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
