//go:build bench

// The cassette/stub bench rig: a bench-OWNED assembler that wires a real
// orchestrator.Conversation with the latency taps installed, WITHOUT touching
// internal/wirenpc (pipeline's sole-owned surface, D2). It mirrors wirenpc's
// reactive shape — VAD → STT → address → agent reply → Tee-wrapped TTS — but
// constructs everything in this package against the public seams so the bench
// can swap providers (stub / cassette / live) per tier.
//
// `//go:build bench` keeps this out of the default no-CGO PR gate; the cassette
// tier runs it as `-tags "bench opus"` (real silero needs CGO), per the ADR-0033
// addendum.
package voicebench

import (
	"context"

	"github.com/MrWong99/Glyphoxa/pkg/tool"
	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/agenttool"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/wire"

	"github.com/MrWong99/Glyphoxa/internal/observe"
)

// RigConfig assembles one bench Conversation. The VAD/STT come from the caller
// (the cassette tier passes real silero via voicetest.NewVADRig + a cassette
// Recognizer; a keyless test passes a scripted VAD + stub Recognizer). Provider
// and Synth are the swappable network seams: stub for the orchestration-floor
// run, cassette/live otherwise.
type RigConfig struct {
	Bus     *voiceevent.Bus
	VAD     *orchestrator.VAD
	STT     *orchestrator.STT
	Persona agent.Persona
	// Provider is the LLM behind the agent reply loop.
	Provider llm.Provider
	// Synth is the inner TTS the Tee wraps; the Tee publishes FirstAudio.
	Synth tts.Synthesizer
	// Detector routes STTFinal→AddressRouted; nil routes nothing (the rig
	// installs a single-NPC detector that always routes to Persona).
	Detector *orchestrator.AddressDetector
	// Recorder captures the agenttool-adapter spans (llm_round/provider_*).
	Recorder observe.StageRecorder
}

// BuildConversation wires the rig's Conversation: the agent reply loop over
// Provider (dice tool granted, matching wirenpc's grant) feeding a Tee-wrapped
// Synth that publishes FirstAudio on Bus. Returns the Conversation ready for
// Register/Feed. The Tee's sink is a drain-only PlaybackSink — the bench measures
// the FirstAudio boundary, not real playback.
func BuildConversation(cfg RigConfig) *orchestrator.Conversation {
	rec := cfg.Recorder
	if rec == nil {
		rec = observe.Discard{}
	}

	reg := tool.NewRegistry()
	reg.MustRegister(tool.NewDice())
	grants := tool.NewGrantSet(reg, tool.Grant{ToolName: "dice"})
	engine := agenttool.NewEngine(cfg.Provider, grants, "", 0, 0,
		agenttool.WithMetrics(rec, observe.ProviderGemini))

	replier := agent.NewReplier(agent.Config{
		Persona:      cfg.Persona,
		Engine:       engine,
		Synthesizer:  cfg.Synth,
		HistoryTurns: 16,
	})

	// Tee the synth so the first AudioChunk per sentence publishes FirstAudio on
	// the bus (A3 hook 1 — the headline SLO boundary). A drain-only sink stands
	// in for the real PlaybackPump: the bench measures when audio reaches the
	// pump boundary, not the audio itself.
	tee := wire.NewTeeSynthesizer(cfg.Synth, drainSink{}, cfg.Bus)
	ttsStage := orchestrator.NewTTS(cfg.Bus, tee)

	opts := []orchestrator.Option{
		orchestrator.WithReply(replier.Reply()),
		orchestrator.WithBargeIn(0),
	}
	if cfg.Detector != nil {
		opts = append(opts, orchestrator.WithDetector(cfg.Detector))
	}
	return orchestrator.NewConversation(cfg.Bus, cfg.VAD, cfg.STT, ttsStage, opts...)
}

// drainSink is a PlaybackSink that drains each sentence's chunks and drops them
// — the bench needs the Tee→sink boundary to fire FirstAudio, not real audio.
type drainSink struct{}

func (drainSink) HandleSentence(ctx context.Context, chunks <-chan tts.AudioChunk) {
	go func() {
		for range chunks {
		}
	}()
}
