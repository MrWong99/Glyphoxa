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
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/snowflake/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/storage"
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
	// vadMinSilenceFrames is the consecutive sub-threshold frames Silero needs to
	// leave the speaking state — the end-of-speech hangover, a fixed cost on every
	// turn before STT can start (B3). Lowered from silero's default 15 (480 ms) to
	// 12 (384 ms), a ~96 ms per-turn win.
	//
	// The plan proposed 8 (256 ms), but the corpus validation
	// (TestB3_HangoverTuning_CorpusSegmentation) refuted it: at 8 the purpose-built
	// two-utterance-test clip splits a single utterance at an internal pause
	// (3 segments instead of its designed 2) — the exact clipped-tail / premature-
	// cut failure mode the task warned against. That clip only recovers its correct
	// count at 11; 12 keeps it correct with one frame of margin against real-mic
	// variation. The longer natural ttrpg intros have inter-sentence pauses that
	// any value below 15 splits, so they are excluded from the equality gate — that
	// is a (benign) extra turn boundary at a real pause, not a mid-word cut.
	vadMinSilenceFrames = 12

	// elevenGeorgeVoiceID is the ElevenLabs "George" public preset — a neutral
	// stand-in voice for the NPC.
	elevenGeorgeVoiceID = "JBFqnCBsd6RMkjVDRZzb"
)

// BartPersona is the Character NPC Persona (CONTEXT.md "Persona") for the MVP
// slice. Exported so the `seed` command writes the same Persona text the in-code
// NPC used, and the DB-load equivalence test can compare against it.
const BartPersona = `You are Bart, the gruff but warm-hearted innkeeper of the Prancing Pony.
You speak in short, vivid sentences with a tavern-keeper's cadence. You know the
local rumors, the regulars, and the price of a room. Stay in character; never
mention being an AI.`

// npcSpec is everything needed to bring one Character NPC to life: its
// addressable identity, Persona, Voice, and aliases. The hardcoded slice (#4)
// built this from consts; task #5 loads it from the DB (see agentspec.go), and
// both paths produce the same Conversation.
type npcSpec struct {
	agentID string
	name    string
	persona string
	voice   tts.Voice
	aliases []string
}

// hardcodedNPC is the original in-code "Bart" definition. It is the seed source
// for the DB row (the `seed` command) and the equivalence target for the
// DB-load path: loading Bart from a seeded DB must reproduce exactly this.
func hardcodedNPC() npcSpec {
	return npcSpec{
		agentID: "bart",
		name:    "Bart",
		persona: BartPersona,
		// npcVoice() carries pcm_48000 plus the conversational eleven_v3 defaults
		// (DefaultV3Settings: stability/similarity/speaker-boost). It is both the
		// seed source for the DB row and the #14 live-voice value, so the outbound
		// codec path is encode-only (Discord Opus is 48 kHz — no resampler).
		voice:   npcVoice(),
		aliases: []string{"innkeeper", "barkeep"},
	}
}

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
	// Metrics receives the hot-path voice counters/gauges (A2): inbound frame
	// drops/undecodables and the sessions gauge. nil discards them. The same
	// recorder is handed to the audio Manager (Session open/close, playback) and
	// the inbound [wire.Pipeline] (undecodable frames) so one run's counters share
	// a single sink. The orchestrator-sibling latency recorder (the SLO
	// histograms) is wired separately off the bus — see buildConversation.
	Metrics gxvoice.MetricsRecorder
	// StageMetrics receives the orchestrator-side per-stage / per-turn latency
	// spans (A3): the per-LLM-round span from the agenttool adapter, and (once
	// the bus subscriber lands) the derived stage histograms. nil records
	// nothing. The live binary injects the Prometheus adapter; the benchmark
	// injects its own; the keyless default is the no-op recorder.
	StageMetrics observe.StageRecorder
	// npc is the Character NPC this loop voices. Run resolves it; RunFromDB
	// loads it from storage, the env-only Run path uses the hardcoded NPC.
	npc npcSpec
}

// RunFromDB loads the seeded Character NPC from Postgres (via the task-#8
// storage layer) and runs the live voice loop with it, instead of the in-code
// NPC. dsn is the Postgres connection string. This is the task-#5 DB-load path:
// the only thing it changes versus [Run] is the *source* of the NPC's Persona/
// Voice/identity — the assembled pipeline is identical.
func RunFromDB(ctx context.Context, cfg Config, dsn string) error {
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("wirenpc: open DB pool: %w", err)
	}
	defer pool.Close()

	npc, err := loadSeededNPC(ctx, storage.New(pool))
	if err != nil {
		return err
	}
	log.Info("loaded NPC from DB", "npc", npc.name, "agentID", npc.agentID)

	cfg.npc = npc
	return Run(ctx, cfg)
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
	if cfg.npc.agentID == "" {
		cfg.npc = hardcodedNPC()
	}

	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
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
		// Own disgo's logger explicitly (A1): route it through the same filtered
		// app logger so the benign DAVE-decrypt noise is tamed even if disgo ever
		// stops reading slog.Default().
		bot.WithLogger(log),
		bot.WithDefaultGateway(),
		// disgo's default intents are IntentsNone, so the bot never receives its
		// own VoiceStateUpdate — leaving the voice conn's ChannelID nil and
		// segfaulting disgo's voice gateway on the VoiceServerUpdate join path.
		// GuildVoiceStates (+Guilds) is the minimum to populate that state.
		bot.WithGatewayConfigOpts(gateway.WithIntents(
			gateway.IntentGuilds|gateway.IntentGuildVoiceStates,
		)),
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
		gxvoice.WithMetrics(cfg.Metrics),
	)
	defer mgr.Close()

	sess, err := mgr.Open(ctx, guild, channel)
	if err != nil {
		return fmt.Errorf("wirenpc: join voice channel: %w", err)
	}
	defer sess.Close()
	log.Info("joined voice channel", "guild", guild, "channel", channel, "npc", cfg.npc.name)

	// One Codec instance serves both directions: DecodeInbound (called from the
	// single Pipeline.Run goroutine) and PlaybackSource (called from the playback
	// worker) — the codec documents this split as concurrency-safe. codec.New()
	// is the real Opus transcoder under -tags opus and a fail-fast stub
	// (ErrCodecUnavailable) otherwise, so this binary needs no build-tag
	// knowledge: a default build still constructs and runs, just deaf+mute.
	// Living in the shared Run core, this audio path covers BOTH the hardcoded
	// and the RunFromDB paths (RunFromDB resolves the NPC then delegates here).
	cdc := codec.New()

	// Outbound (speak): the PlaybackPump drives the codec's PlaybackSource onto
	// the Session, one sentence at a time (Session.Play auto-interrupts, so
	// overlapping playback would clip sentences). The TeeSynthesizer wraps the
	// real ElevenLabs synthesizer and tees each synthesized chunk to the pump
	// while the orchestrator's TTS stage keeps draining-and-dropping it (ADR-0021
	// intact).
	//
	// pump.Close() blocks until the playback worker has exited; the deferred
	// Close must run BEFORE sess.Close() so a mid-flight Play cannot race the
	// Session teardown. defers run LIFO and this registers after sess.Close()
	// (line above), and pipe.Run's own deferred cancel stops the Conversation
	// first — so teardown order is conv-stop → pump.Close() → sess.Close(), which
	// is the deterministic ordering the pump's Close() contract requires.
	pump := wire.NewPlaybackPump(sess, cdc)
	defer pump.Close()

	// The orchestrator bus is created here (not inside buildConversation) so the
	// tee can publish FirstAudio (A3 hook 1) onto the same bus the conversation's
	// stages publish on and the metrics subscriber reads.
	bus := voiceevent.NewBus()
	teeSynth := wire.NewTeeSynthesizer(ttseleven.New(""), pump, bus)

	// Attach the orchestrator-sibling latency subscriber (A2/#10): it derives the
	// SLO histograms (response_latency, address_detect, per-sentence tts_ttfb) from
	// the turn-correlated bus events and feeds cfg.StageMetrics. Subscribe wires
	// the handlers (deferred unsubscribe); Start runs the TTL sweep for the run's
	// lifetime so abandoned/barged turns don't leak per-turn state. A nil
	// StageMetrics (keyless) makes the subscriber a no-op via observe.Discard.
	stageSub := observe.NewStageSubscriber(cfg.StageMetrics)
	defer stageSub.Subscribe(bus)()
	stageSub.Start(ctx)

	conv, err := buildConversation(bus, log, cfg.npc, teeSynth, cfg.StageMetrics)
	if err != nil {
		return fmt.Errorf("wirenpc: build pipeline: %w", err)
	}

	// Inbound (hear): the pipeline pumps Session.Inbound through the same Codec's
	// DecodeInbound into the orchestrator. It tags its inbound counters (A2) with
	// the guild and shares the run's MetricsRecorder.
	pipe := wire.NewPipeline(conv, cdc, log, cfg.Guild, cfg.Metrics)
	return pipe.Run(ctx, sess)
}

// npcMatcher builds the Address Detection matcher for the NPC. This Campaign has
// one Character NPC and no addressable Butler in this slice, so it uses the
// scoring Matcher (ADR-0024): the NPC gets a name/alias match AND the single-NPC
// fallback, so both a named utterance ("Bart, …") and an unnamed one route to
// him — a non-Address-Only lone NPC catches unaddressed speech. The whole-word
// matcher is deliberately not used: it requires a Butler as its unconditional
// fallback, which this slice does not have, and would leave the NPC silent on
// every unnamed utterance.
func npcMatcher(npc npcSpec) *address.Matcher {
	return address.NewMatcher(address.Config{Language: "en"},
		address.Agent{
			Target: voiceevent.AddressTarget{
				AgentID:   npc.agentID,
				AgentRole: "character",
				Name:      npc.name,
			},
			Aliases: npc.aliases,
		},
	)
}

// npcVoice is the hardcoded NPC's TTS Voice (the [hardcodedNPC] seed source).
// The DB-loaded NPC carries its own Voice from the seed; this is only used by
// the `-hardcoded` escape path.
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
		Name:       "Bart",
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
// npc supplies the addressable identity, Persona, and Voice (from the in-code
// seed or, via [RunFromDB], the database). The `dice` Tool grant stays in code
// (Tool Grants are a #6 table, not yet seeded). The LLM provider is Gemini — it
// matches the live deployment (providers.llm "gemini", model gemini-2.5-flash;
// there is no Anthropic key). The DB Agent's provider_config provider/model is
// recorded but adapter selection is not yet driven by it; the wired adapter is
// Gemini for any NPC in this tree. Keyless cassette tests replay the Anthropic
// adapter behind the same llm.Provider interface.
//
// synth is the [tts.Synthesizer] the TTS stage drives. [Run] passes a
// [wire.TeeSynthesizer] wrapping the real ElevenLabs synthesizer so the
// synthesized audio is tee'd to the playback path while the orchestrator keeps
// draining-and-dropping it (ADR-0021); a bare ElevenLabs synthesizer also works
// (no audio is played). It must not be nil.
func buildConversation(bus *voiceevent.Bus, log *slog.Logger, npc npcSpec, synth tts.Synthesizer, stageMetrics observe.StageRecorder) (*orchestrator.Conversation, error) {
	if stageMetrics == nil {
		stageMetrics = observe.Discard{}
	}

	engine, err := silero.New(silero.WithMinSilenceFrames(vadMinSilenceFrames))
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

	detector := orchestrator.NewAddressDetector(npcMatcher(npc))

	// grants would degrade to a single completion through the same path. The
	// `dice` grant stays in code: Tool Grants are a #6 table, not yet seeded.
	//
	// Gemini is the live LLM provider (see the function doc): it reads
	// GEMINI_API_KEY at request time (BYOK, ADR-0004); export it from the keyring
	// before a live run (docs/agents/live-npc-run.md). There is no Anthropic key,
	// so wiring the Anthropic adapter here would pass the keyless cassette tests
	// (which replay Anthropic) but fail the live run — Gemini is the only correct
	// default for a runnable NPC.
	provider := gemini.New("")
	reg := tool.NewRegistry()
	reg.MustRegister(tool.NewDice())
	grants := tool.NewGrantSet(reg, tool.Grant{ToolName: "dice"})
	toolEngine := agenttool.NewEngine(provider, grants, gemini.DefaultModel, 0, 0,
		// Gemini is the wired provider (see the function doc), so the per-round
		// LLM spans (A3) are labelled gemini. The no-op recorder keeps the keyless
		// path silent; the live binary / benchmark inject a real one.
		agenttool.WithMetrics(stageMetrics, observe.ProviderGemini))

	replier := agent.NewReplier(agent.Config{
		Persona: agent.Persona{
			AgentID:  npc.agentID,
			Markdown: npc.persona,
			Voice:    npc.voice,
		},
		Engine:       toolEngine,
		Synthesizer:  ttseleven.New(""),
		HistoryTurns: 16,
		OnError: func(err error) {
			log.Warn("agent reply failed", "npc", npc.name, "err", err)
		},
	})

	conv := orchestrator.NewConversation(bus, vadStage, sttStage, ttsStage,
		orchestrator.WithDetector(detector),
		// B1: stream the reply sentence-by-sentence so first audio begins after the
		// first sentence, not the whole completion. The agenttool Engine implements
		// agent.StreamingEngine (it streams the final answer round), so ReplyStream
		// dispatches each sentence as it lands.
		orchestrator.WithReplyStream(replier.ReplyStream()),
		// Barge-in (ADR-0027): a human talking over Bart cancels his turn. Start
		// with an instant cut (0 confirm window) to validate the async-turn path
		// live; the ~250ms confirm window is the next tuning step. With B1 a barge
		// now cancels mid-generation, not just pending dispatch.
		orchestrator.WithBargeIn(0),
		orchestrator.WithErrorHandler(func(err error) {
			log.Warn("reply dispatch failed", "err", err)
		}),
	)
	return conv, nil
}
