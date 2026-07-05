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
	"encoding/json"
	"fmt"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/tool"
	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/agenttool"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	ttseleven "github.com/MrWong99/Glyphoxa/pkg/voice/tts/elevenlabs"
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
// Register/Feed. The Tee's sink is a drain-only PlaybackSink that also publishes
// FirstOpus on each sentence's first chunk — the bench's audible-on-wire
// boundary, where response_latency now ends (no real playback happens).
func BuildConversation(cfg RigConfig) *orchestrator.Conversation {
	rec := cfg.Recorder
	if rec == nil {
		rec = observe.Discard{}
	}

	reg := tool.BuiltinRegistry()
	grants := tool.NewGrantSet(reg, tool.Grant{ToolName: "dice"})
	engine := agenttool.NewEngine(cfg.Provider, grants, "", 0, 0,
		agenttool.WithMetrics(rec, observe.ProviderGroq))

	replier := agent.NewReplier(agent.Config{
		Persona:      cfg.Persona,
		Engine:       engine,
		Synthesizer:  cfg.Synth,
		HistoryTurns: 16,
	})

	// Tee the synth so the first AudioChunk per sentence publishes FirstAudio on
	// the bus (A3 hook 1 — tts_ttfb + the lifecycle success signal). The drain
	// sink stands in for the real PlaybackPump AND for the wire boundary: it
	// publishes FirstOpus on the first chunk per sentence the way prod's
	// firstOpusSource does on the first frame the Discord sender pulls — the
	// headline response_latency now ends there (task #7), so without it the
	// bench records no response_latency samples at all. The bench has no codec
	// /sender, so chunk-reaches-sink is its audible-on-wire moment.
	tee := wire.NewTeeSynthesizer(cfg.Synth, drainSink{bus: cfg.Bus}, cfg.Bus)
	ttsStage := orchestrator.NewTTS(cfg.Bus, tee)

	// Install observe's StageSubscriber on the bus so the bus-derived headline
	// stages (response_latency / address_detect / tts_ttfb) are recorded onto the
	// SAME tap the agenttool adapter records llm_round onto — reusing observe's
	// per-TurnID grouping is the can't-drift guarantee (the bench reads the exact
	// derivation prod's Prometheus subscriber uses, not a parallel extractor).
	if cfg.Recorder != nil {
		sub := observe.NewStageSubscriber(cfg.Recorder)
		sub.Subscribe(cfg.Bus)
	}

	opts := []orchestrator.Option{
		// WithReplyStream (NOT WithReply): the streaming reply dispatches each
		// sentence to TTS the moment it's ready (B1) — one TTSInvoked + one
		// FirstAudio per sentence. WithReply's non-streaming turn() dispatches the
		// whole completion as a SINGLE Reply, collapsing a multi-sentence reply to
		// one TTS dispatch and never exercising the per-sentence Tee→FirstAudio
		// path the response_latency / tts_ttfb spans measure. The bench must drive
		// the same streaming path prod runs.
		orchestrator.WithReplyStream(replier.ReplyStream()),
	}
	if cfg.Detector != nil {
		opts = append(opts, orchestrator.WithDetector(cfg.Detector))
	}
	return orchestrator.NewConversation(cfg.Bus, cfg.VAD, cfg.STT, ttsStage, opts...)
}

// benchPersonaMarkdown mirrors the prod NPC persona's latency-relevant shape
// (internal/wirenpc.BartPersona) without importing the pipeline's sole-owned
// internal/wirenpc, exactly as benchVoice() mirrors npcVoice(). The decisive
// trait for the live tier is the "short, vivid sentences" instruction: prod
// tells the model to keep replies tight, so a stub persona that omits it (the
// old "You are Bart, the innkeeper.") lets the model ramble toward the 1024-token
// max_tokens cap — inflating both llm_round and the response_latency the SLO
// gate asserts with a reply LENGTH prod never produces. Pinning the brevity here
// keeps the live number representative of the shipped config, not an artifact of
// an under-specified bench prompt.
const benchPersonaMarkdown = `You are Bart, the gruff but warm-hearted innkeeper of the Prancing Pony.
You speak in short, vivid sentences with a tavern-keeper's cadence. You know the
local rumors, the regulars, and the price of a room. Stay in character; never
mention being an AI.`

// benchPersona returns the bench Persona for "Bart": the prod-representative
// persona markdown (see [benchPersonaMarkdown]) bound to the bench voice. ONLY
// the live tier uses it — that is the only tier whose reply is generated by a
// real model, so it is the only one whose latency the persona's brevity shapes.
// The cassette tier must NOT use this: its llm_round is canned replay and its
// prompt_hash bakes in the exact persona it was recorded against, so swapping
// the persona there would miss every cassette (re-record territory, ADR-0021).
func benchPersona() agent.Persona {
	return agent.Persona{AgentID: "bart", Markdown: benchPersonaMarkdown, Voice: benchVoice()}
}

// benchVoice mirrors wirenpc's npcVoice() (the prod NPC voice: ElevenLabs
// "George" public preset, eleven_v3 defaults, pcm_48000) without importing the
// pipeline's sole-owned internal/wirenpc. The live tier NEEDS it — a real
// ElevenLabs Synthesize rejects an empty VoiceID, so a voiceless Persona makes
// every live turn die silently with no audio (the exact 0-sample failure the
// nightly hit on its first real run). The cassette tier's stub ignores the
// VoiceID but the voice still shapes the system prompt via AudioMarkupPrompt,
// so both tiers set it for prompt parity with prod.
func benchVoice() tts.Voice {
	settings := ttseleven.DefaultV3Settings()
	settings.OutputFormat = "pcm_48000"
	raw, err := json.Marshal(settings)
	if err != nil {
		panic(fmt.Sprintf("voicebench.benchVoice: marshal voice settings: %v", err))
	}
	return tts.Voice{
		ProviderID: ttseleven.ProviderID,
		// ElevenLabs "George" public preset — same ID wirenpc pins for Bart.
		VoiceID:  "JBFqnCBsd6RMkjVDRZzb",
		Name:     "Bart",
		Language: "en",
		Settings: raw,
	}
}

// drainSink is a PlaybackSink that drains each sentence's chunks and drops them.
// It publishes FirstOpus on the first chunk of each sentence (TurnID from the
// per-turn ctx, mirroring prod's firstOpusSource; the subscriber dedupes to the
// first per turn) — the bench's stand-in for the audible-on-wire boundary that
// ends response_latency. A sentence cancelled before any chunk arrives publishes
// nothing, matching prod.
type drainSink struct{ bus *voiceevent.Bus }

func (s drainSink) HandleSentence(ctx context.Context, chunks <-chan tts.AudioChunk) {
	turnID := voiceevent.TurnIDFrom(ctx)
	go func() {
		first := true
		for range chunks {
			if first {
				first = false
				if s.bus != nil && turnID != "" {
					s.bus.Publish(voiceevent.FirstOpus{At: time.Now(), TurnID: turnID})
				}
			}
		}
	}()
}
