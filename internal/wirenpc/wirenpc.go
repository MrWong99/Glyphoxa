// Package wirenpc constructs the one hardcoded Character NPC live voice loop for
// the MVP `voice` mode (task #4): it builds the Discord audio Manager, joins a
// voice channel, assembles the orchestrator pipeline with the production Agent
// loop as the ReplyFunc, and runs the audio loop via pkg/voice/wire.
//
// "Hardcoded" means the NPC's Persona, Voice, and provider selection live in
// code here (no DB); task #5 swaps this for a DB-loaded Agent. Credentials are
// runtime-only — the Discord token and provider API keys come from the
// environment, never compiled in.
package wirenpc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/snowflake/v2"

	"github.com/MrWong99/Glyphoxa/pkg/tool"
	gxvoice "github.com/MrWong99/Glyphoxa/pkg/voice"
	"github.com/MrWong99/Glyphoxa/pkg/voice/address"
	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/agenttool"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/gemini"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	stteleven "github.com/MrWong99/Glyphoxa/pkg/voice/stt/elevenlabs"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	ttseleven "github.com/MrWong99/Glyphoxa/pkg/voice/tts/elevenlabs"
	"github.com/MrWong99/Glyphoxa/pkg/voice/vad"
	"github.com/MrWong99/Glyphoxa/pkg/voice/vad/silero"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/wire"
	"github.com/MrWong99/Glyphoxa/pkg/voice/wire/codec"
)

const (
	// vadSampleRate is the PCM rate the VAD/STT stages run at; Silero v5 accepts
	// 8 kHz or 16 kHz, and the STT cassettes were recorded at 16 kHz.
	vadSampleRate = 16000
	// vadFrameMs is the orchestrator frame size; the inbound codec must reframe
	// Discord's 48 kHz Opus to this 16 kHz / 32 ms (512-sample) cadence.
	vadFrameMs = 32

	// npcAgentID is the hardcoded NPC's Agent identifier; the production
	// ReplyFunc answers only routes targeting it.
	npcAgentID = "bart"
	// npcName is the NPC's display name and the address-detection alias.
	npcName = "Bart"

	// elevenGeorgeVoiceID is the ElevenLabs "George" public preset — a neutral
	// stand-in voice for the hardcoded NPC.
	elevenGeorgeVoiceID = "JBFqnCBsd6RMkjVDRZzb"
)

// npcPersona is the hardcoded Character NPC Persona (CONTEXT.md "Persona") for
// the MVP slice. Task #5 replaces this with a DB-loaded Agent record.
const npcPersona = `You are Bart, the gruff but warm-hearted innkeeper of the Prancing Pony.
You speak in short, vivid sentences with a tavern-keeper's cadence. You know the
local rumors, the regulars, and the price of a room. Stay in character; never
mention being an AI.`

// Config configures a [Run] of the live NPC voice loop.
type Config struct {
	// Token is the Discord bot token (from DISCORD_BOT_TOKEN). Required.
	Token string
	// Guild and Channel are the Discord snowflake IDs of the server and voice
	// channel to join. Required.
	Guild   string
	Channel string
	// Logger receives structured logs; nil discards them.
	Logger *slog.Logger
}

// Run builds and runs the live NPC voice loop until ctx is cancelled. It joins
// the configured voice channel, wires the orchestrator pipeline with the
// production Agent loop, and pumps audio through [wire.Pipeline] in both
// directions: inbound Opus → DecodeInbound → VAD/STT (hear), and synthesized TTS
// → tee → serial playback → Opus → Session.Play (speak).
//
// Audio requires the real Opus↔PCM [codec]; it is compiled in only under
// -tags opus (system libopus). A default build links the codec stub, so Run
// still connects and constructs the whole pipeline but the audio loop fails fast
// with [wire.ErrCodecUnavailable] on the first inbound frame — the binary is
// runnable and the wiring complete without the native dependency. Build with
// -tags "opus dave nolibopusfile" for a hearing, speaking, encrypted NPC.
func Run(ctx context.Context, cfg Config) error {
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.NewTextHandler(discard{}, nil))
	}

	guild, err := snowflake.Parse(cfg.Guild)
	if err != nil {
		return fmt.Errorf("wirenpc: parse guild ID %q: %w", cfg.Guild, err)
	}
	channel, err := snowflake.Parse(cfg.Channel)
	if err != nil {
		return fmt.Errorf("wirenpc: parse channel ID %q: %w", cfg.Channel, err)
	}

	// Discord client: DAVE/MLS is wired at construction (it cannot be enabled
	// after disgo builds its VoiceManager). DaveOption() is a no-op stub unless
	// the binary was built with -tags dave; NewManager(WithDave(true)) then warns
	// if encryption was expected but unavailable.
	client, err := disgo.New(cfg.Token,
		bot.WithDefaultGateway(),
		gxvoice.DaveOption(),
	)
	if err != nil {
		return fmt.Errorf("wirenpc: build Discord client: %w", err)
	}
	defer client.Close(context.Background())

	if err := client.OpenGateway(ctx); err != nil {
		return fmt.Errorf("wirenpc: open gateway: %w", err)
	}

	mgr := gxvoice.NewManager(client,
		gxvoice.WithLogger(log),
		gxvoice.WithDave(gxvoice.DaveAvailable()),
	)
	defer mgr.Close()

	sess, err := mgr.Open(ctx, guild, channel)
	if err != nil {
		return fmt.Errorf("wirenpc: join voice channel: %w", err)
	}
	defer sess.Close()
	log.Info("joined voice channel", "guild", guild, "channel", channel, "npc", npcName)

	// One Codec instance serves both directions: DecodeInbound (called from the
	// single Pipeline.Run goroutine) and PlaybackSource (called from the playback
	// worker) — the codec documents this split as concurrency-safe. codec.New()
	// is the real Opus transcoder under -tags opus and a fail-fast stub
	// (ErrCodecUnavailable) otherwise, so this binary needs no build-tag
	// knowledge: a default build still constructs and runs, just deaf+mute.
	cdc := codec.New()

	// Outbound (speak): a serial playback sink drives the codec's PlaybackSource
	// onto the Session, one sentence at a time (Session.Play auto-interrupts, so
	// overlapping playback would clip sentences). The TeeSynthesizer wraps the
	// real ElevenLabs synthesizer and tees each synthesized chunk to this sink
	// while the orchestrator's TTS stage keeps draining-and-dropping it (ADR-0021
	// intact). ctx scopes the worker so it unwinds on shutdown.
	sink := wire.NewSequentialSink(ctx, wire.NewSessionPlayer(sess, cdc), func(err error) {
		log.Warn("sentence playback failed", "npc", npcName, "err", err)
	})
	teeSynth := wire.NewTeeSynthesizer(ttseleven.New(""), sink)

	conv, err := buildConversation(log, teeSynth)
	if err != nil {
		return fmt.Errorf("wirenpc: build pipeline: %w", err)
	}

	// Inbound (hear): the pipeline pumps Session.Inbound through the same Codec's
	// DecodeInbound into the orchestrator.
	pipe := wire.NewPipeline(conv, cdc, log)
	return pipe.Run(ctx, sess)
}

// npcMatcher builds the Address Detection matcher for the hardcoded NPC. This
// Campaign has one Character NPC and no Butler in this slice, so it uses the
// scoring Matcher (ADR-0024): Bart gets a name/alias match AND the single-NPC
// fallback, so both a named utterance ("Bart, …") and an unnamed one route to
// him — a non-Address-Only lone NPC catches unaddressed speech. The whole-word
// matcher is deliberately not used: it requires a Butler as its unconditional
// fallback, which this slice does not have, and would leave Bart silent on
// every unnamed utterance.
func npcMatcher() *address.Matcher {
	return address.NewMatcher(address.Config{Language: "en"},
		address.Agent{
			Target: voiceevent.AddressTarget{
				AgentID:   npcAgentID,
				AgentRole: "character",
				Name:      npcName,
			},
			Aliases: []string{"innkeeper", "barkeep"},
		},
	)
}

// npcVoice is the hardcoded NPC's TTS Voice.
//
// Settings overrides the ElevenLabs output format to pcm_48000 (keeping the rest
// of the conversational eleven_v3 defaults). Discord's Opus encoder runs at
// 48 kHz, so emitting 48 kHz PCM makes the outbound codec path encode-only — no
// resampling on the live demo, which removes a resampler quality/artefact risk.
// The codec still resamples arbitrary AudioChunk.SampleRate for tests and other
// voices; this voice simply does not exercise it.
func npcVoice() tts.Voice {
	settings := ttseleven.DefaultV3Settings()
	settings.OutputFormat = "pcm_48000"
	raw, err := json.Marshal(settings)
	if err != nil {
		// DefaultV3Settings is a fixed, marshalable struct; a failure here is a
		// programming error, not a runtime condition.
		panic(fmt.Sprintf("wirenpc.npcVoice: marshal voice settings: %v", err))
	}
	return tts.Voice{
		ProviderID: ttseleven.ProviderID,
		VoiceID:    elevenGeorgeVoiceID,
		Name:       npcName,
		Language:   "en",
		Settings:   raw,
	}
}

// buildConversation assembles the orchestrator reactive pipeline: VAD (Silero)
// → STT (ElevenLabs) → Address Detection → production Reply (the Agent loop over
// Gemini, with the dice Tool granted via the tool-use loop) → TTS (synth).
// Provider API keys are read by each adapter from its own env var at request
// time (BYOK, ADR-0004), so construction here needs no secrets.
//
// synth is the [tts.Synthesizer] the TTS stage drives. [Run] passes a
// [wire.TeeSynthesizer] wrapping the real ElevenLabs synthesizer so the
// synthesized audio is tee'd to the playback path while the orchestrator keeps
// draining-and-dropping it (ADR-0021); a bare ElevenLabs synthesizer also works
// (no audio is played). It must not be nil.
func buildConversation(log *slog.Logger, synth tts.Synthesizer) (*orchestrator.Conversation, error) {
	bus := voiceevent.NewBus()

	engine, err := silero.New()
	if err != nil {
		return nil, fmt.Errorf("init Silero VAD: %w", err)
	}
	vadSession, err := engine.NewSession(vad.Config{
		SampleRate:       vadSampleRate,
		FrameSizeMs:      vadFrameMs,
		SpeechThreshold:  0.5,
		SilenceThreshold: 0.35,
	})
	if err != nil {
		return nil, fmt.Errorf("open VAD session: %w", err)
	}
	vadStage := orchestrator.NewVAD(bus, vadSession)

	sttStage := orchestrator.NewSTT(bus, stteleven.New(""))
	ttsStage := orchestrator.NewTTS(bus, synth)

	detector := orchestrator.NewAddressDetector(npcMatcher())

	// Production ReplyFunc: the Agent loop. The tool-use loop (with the dice
	// Tool granted) is the Engine, so the NPC can roll dice; an Agent with no
	// grants would degrade to a single completion through the same path.
	//
	// Gemini is the live LLM provider — it matches the actual deployment
	// (providers.llm.name "gemini", model gemini-2.5-flash; there is no
	// Anthropic key). The adapter reads GEMINI_API_KEY at request time (BYOK,
	// ADR-0004); export it from the keyring before a live run (see
	// docs/agents/live-npc-run.md). The keyless cassette tests still use the
	// anthropic adapter behind the same llm.Provider interface, so this swap is
	// provider-only — no rework of the loop, bridge, or persona path.
	provider := gemini.New("")
	reg := tool.NewRegistry()
	reg.MustRegister(tool.NewDice())
	grants := tool.NewGrantSet(reg, tool.Grant{ToolName: "dice"})
	toolEngine := agenttool.NewEngine(provider, grants, gemini.DefaultModel, 0, 0)

	replier := agent.NewReplier(agent.Config{
		Persona: agent.Persona{
			AgentID:  npcAgentID,
			Markdown: npcPersona,
			Voice:    npcVoice(),
		},
		Engine:       toolEngine,
		Synthesizer:  ttseleven.New(""),
		HistoryTurns: 16,
		OnError: func(err error) {
			log.Warn("agent reply failed", "npc", npcName, "err", err)
		},
	})

	conv := orchestrator.NewConversation(bus, vadStage, sttStage, ttsStage,
		orchestrator.WithDetector(detector),
		orchestrator.WithReply(replier.Reply()),
		orchestrator.WithErrorHandler(func(err error) {
			log.Warn("reply dispatch failed", "err", err)
		}),
	)
	return conv, nil
}

// discard is an io.Writer sink for the fallback logger.
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
