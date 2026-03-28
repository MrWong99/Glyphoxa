// This file exports package-level helpers so they can be reused by the worker
// RuntimeFactory (cmd/glyphoxa runWorker) without duplicating logic. The
// unexported originals remain for backward compatibility within the package.

package app

import (
	"context"

	"github.com/MrWong99/glyphoxa/internal/agent"
	"github.com/MrWong99/glyphoxa/internal/agent/orchestrator"
	"github.com/MrWong99/glyphoxa/internal/config"
	"github.com/MrWong99/glyphoxa/internal/engine"
	"github.com/MrWong99/glyphoxa/internal/mcp"
	"github.com/MrWong99/glyphoxa/internal/transcript"
	"github.com/MrWong99/glyphoxa/pkg/audio"
	"github.com/MrWong99/glyphoxa/pkg/memory"
	"github.com/MrWong99/glyphoxa/pkg/provider/stt"
	"github.com/MrWong99/glyphoxa/pkg/provider/vad"
)

// BuildEngine constructs the appropriate VoiceEngine for an NPC config.
// Exported wrapper around buildEngine for use by the worker RuntimeFactory.
func BuildEngine(providers *Providers, npc config.NPCConfig, ttsEntry config.ProviderEntry) (engine.VoiceEngine, error) {
	return buildEngine(providers, npc, ttsEntry)
}

// IdentityFromConfig converts a config.NPCConfig to an agent.NPCIdentity.
// Exported wrapper around identityFromConfig.
func IdentityFromConfig(npc config.NPCConfig) agent.NPCIdentity {
	return identityFromConfig(npc)
}

// ConfigBudgetTier converts a config.BudgetTier string to mcp.BudgetTier.
// Exported wrapper around configBudgetTier.
func ConfigBudgetTier(tier config.BudgetTier) mcp.BudgetTier {
	return configBudgetTier(tier)
}

// TTSFormatFromConfig returns the TTS output sample rate and channel count
// from a TTS provider config entry.
// Exported wrapper around ttsFormatFromConfig.
func TTSFormatFromConfig(entry config.ProviderEntry) (sampleRate, channels int) {
	return ttsFormatFromConfig(entry)
}

// RegisterNPCEntities upserts NPC entities in the knowledge graph.
// Exported wrapper around registerNPCEntities.
func RegisterNPCEntities(ctx context.Context, graph memory.KnowledgeGraph, npcs []config.NPCConfig) {
	registerNPCEntities(ctx, graph, npcs)
}

// VADConfigFromProvider extracts VAD session parameters from a provider config.
// Exported wrapper around vadConfigFromProvider.
func VADConfigFromProvider(entry config.ProviderEntry) vad.Config {
	return vadConfigFromProvider(entry)
}

// STTConfigFromProvider extracts STT stream parameters from a provider config.
// Exported wrapper around sttConfigFromProvider.
func STTConfigFromProvider(entry config.ProviderEntry) stt.StreamConfig {
	return sttConfigFromProvider(entry)
}

// AudioPipelineConfig holds all dependencies for an [AudioPipeline].
// Exported version of audioPipelineConfig for use by the worker RuntimeFactory.
type AudioPipelineConfig struct {
	Conn        audio.Connection
	VADEngine   vad.Engine
	STTProvider stt.Provider
	Orch        *orchestrator.Orchestrator
	Mixer       audio.Mixer
	VADCfg      vad.Config
	STTCfg      stt.StreamConfig
	Ctx         context.Context
	Pipeline    transcript.Pipeline // may be nil — correction is skipped when nil
	Entities    func() []string     // returns current entity names; may be nil
	BotUserID   string              // bot's own user ID — workers for this ID are skipped
}

// AudioPipeline is the exported type alias for the audio pipeline.
// Use [NewAudioPipeline] to create one.
type AudioPipeline = audioPipeline

// NewAudioPipeline creates an AudioPipeline from the given exported config.
// Call Start to begin processing and Stop to tear down.
func NewAudioPipeline(cfg AudioPipelineConfig) *AudioPipeline {
	return newAudioPipeline(audioPipelineConfig{
		conn:        cfg.Conn,
		vadEngine:   cfg.VADEngine,
		sttProvider: cfg.STTProvider,
		orch:        cfg.Orch,
		mixer:       cfg.Mixer,
		vadCfg:      cfg.VADCfg,
		sttCfg:      cfg.STTCfg,
		ctx:         cfg.Ctx,
		pipeline:    cfg.Pipeline,
		entities:    cfg.Entities,
		botUserID:   cfg.BotUserID,
	})
}
