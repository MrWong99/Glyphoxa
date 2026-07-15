package wirenpc

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/tool"
	"github.com/MrWong99/Glyphoxa/pkg/voice/address"
	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/agenttool"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/retry"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt"
	stteleven "github.com/MrWong99/Glyphoxa/pkg/voice/stt/elevenlabs"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	ttseleven "github.com/MrWong99/Glyphoxa/pkg/voice/tts/elevenlabs"
	"github.com/MrWong99/Glyphoxa/pkg/voice/vad"
	"github.com/MrWong99/Glyphoxa/pkg/voice/vad/silero"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/wire"
)

// npcMatcher builds the Address Detection matcher for one NPC. With a single
// non-Address-Only Character NPC and no addressable Butler it uses the scoring
// Matcher (ADR-0024): the NPC gets a name/alias match AND the single-NPC
// fallback, so both a named utterance ("Bart, …") and an unnamed one route to it.
// The whole-word matcher is deliberately not used: it requires a Butler as its
// unconditional fallback, which this slice does not have, and would leave the NPC
// silent on every unnamed utterance.
//
// Multi-NPC wiring goes through [Roster.AddNPC] (the matcher's roster grows past
// the first agent via [address.Matcher.Add]); this single-agent constructor is
// retained as the unit-test seam for the one-NPC routing invariant.
func npcMatcher(npc npcSpec) *address.Matcher {
	return address.NewMatcher(address.Config{Language: "en"}, matcherAgent(npc))
}

// Provider-adapter constructors, injected as package vars so a test can spy on
// the apiKey each component receives (issue #69). The adapters expose no key
// getter, so this is the seam that pins the resolved BYOK key reaching its OWN
// adapter — a slot swap (e.g. groq.New(keys.stt)) or a dropped `cfg.keys = keys`
// would otherwise revert the feature to ENV while every providerKeys{} test
// stayed green. Production always uses the real constructors.
var (
	newLLM = llmbuild.New
	newSTT = stteleven.New
	newTTS = ttseleven.New
	// newAddressDetector is the constructor seam for the address detector (#280):
	// buildConversation dispatches through it so a test can capture the detector —
	// and the Butler GM-gate option threaded onto it — without standing up the
	// whole Conversation. The live path is orchestrator.NewAddressDetector verbatim.
	newAddressDetector = orchestrator.NewAddressDetector
)

// llmProviderID reports the provider id of an LLM Provider Config for [newLLM]
// dispatch (#272). A nil config — the env-only [Run] path, or a DB Agent with no
// bound LLM config — yields "", which [llmbuild.New] resolves to the Groq default
// (ADR-0036), keeping the pre-#272 hardwired-groq behaviour byte-identical.
func llmProviderID(cfg *storage.ProviderConfig) string {
	if cfg == nil {
		return ""
	}
	return cfg.Provider
}

// llmProviderLabel maps an LLM provider id to its bounded [observe.Provider] metric
// label (#272): the empty id (env-only / nil config) is Groq (ADR-0036). The wired
// ids equal their observe constants, so the cast is exact. It keeps the per-round
// LLM spans AND the spend price lookup keyed to the ACTUAL provider — a non-Groq
// adapter mislabelled groq would price (groq, claude-model) as a miss and trip caps
// on the frontier default.
func llmProviderLabel(providerID string) observe.Provider {
	if providerID == "" {
		return observe.ProviderGroq
	}
	return observe.Provider(providerID)
}

// The playback pump is the production [orchestrator.LookaheadPump] (#375): the same
// serialized playback path holds and releases the Cross-talk Reaction's pre-rendered
// first sentence.
var _ orchestrator.LookaheadPump = (*wire.PlaybackPump)(nil)

// conversationDeps carries everything [buildConversation] assembles the
// orchestrator pipeline from, with named fields instead of a positional
// parameter list: connectAndServe populates it from the cycle's [Config] plus
// the per-cycle constructions (tee'd synthesizer, text poster, playback pump),
// and tests populate only the fields they exercise (the zero value of every
// optional field is that feature's off state).
type conversationDeps struct {
	// bus is the cycle's voiceevent bus every stage publishes on. Required.
	bus *voiceevent.Bus
	// log receives structured logs. Required (callers pass a discard logger,
	// never nil).
	log *slog.Logger

	// npcs supplies the INITIAL Character NPCs the loop voices — their
	// addressable identity, Persona, and Voice (from the in-code seed or, via
	// [RunFromDB], the database). buildConversation assembles them into a
	// [Roster] (one address Matcher + one Cast): the detector routes against the
	// Matcher and the reply stream multiplexes across the Cast, so an utterance
	// naming an NPC is answered in that NPC's Voice and a lone NPC still catches
	// unaddressed speech. Must be non-empty.
	npcs []npcSpec
	// language is the Campaign Language of the campaign the npcs belong to: it
	// selects the Roster matcher's phonetic encoder (#199). A code with no
	// registered encoder — including the env-only path's "" — resolves to "en"
	// (see matcherLanguage).
	language string

	// synth is the [tts.Synthesizer] the TTS stage drives. [Run] passes a
	// [wire.TeeSynthesizer] wrapping the real ElevenLabs synthesizer so the
	// synthesized audio is tee'd to the playback path while the orchestrator
	// keeps draining-and-dropping it (ADR-0021); a bare ElevenLabs synthesizer
	// also works (no audio is played). Must not be nil.
	synth tts.Synthesizer
	// stageMetrics receives the per-stage latency spans (A3); nil records
	// nothing (normalized to [observe.Discard]).
	stageMetrics observe.StageRecorder

	// keys are the resolved BYOK provider keys (issue #69, hybrid policy
	// ADR-0039): each adapter is constructed with its component's key, which
	// OVERRIDES that adapter's *_API_KEY env var — except an empty key (the env
	// placeholder, or the env-only [Run] path) keeps today's behavior, where the
	// adapter reads its env var at request time (ADR-0004). So a saved key
	// drives the session and an unconfigured component falls back to ENV.
	keys providerKeys
	// llmProviderID dispatches the LLM adapter off the primary Agent's
	// provider_config.provider via [newLLM] ([llmbuild.New], #272): the seed's
	// groq config (model openai/gpt-oss-120b, the #424 default, via the
	// OpenAI-compat endpoint) and the env-only "" both resolve to Groq
	// (ADR-0036), while a saved non-Groq LLM config gets its own adapter.
	// Keyless cassette tests replay the Anthropic adapter behind the same
	// llm.Provider interface.
	llmProviderID string
	// sttStreaming opts into the streaming-STT transport (ADR-0042); false is
	// the byte-for-byte batch default. See [Config.STTStreaming].
	sttStreaming bool

	// memory / facts fill the per-turn Hot Context slots on every NPC's Agent
	// loop (#122/#126); nil disables the slot (the prompt is byte-identical).
	memory agent.MemoryRecaller
	facts  agent.FactsRecaller

	// mutes is the live per-Agent mute view (#211) and gate the live spend
	// turn gate (#130); nil is each feature's off default.
	mutes orchestrator.MuteView
	gate  orchestrator.TurnGate

	// gmSpeaker arms the Butler GM-only voice-address gate (#280, ADR-0024);
	// nil leaves the gate off. textPoster is the Butler's text-delivery sink
	// (#299), wired on butler-role specs only; nil keeps the pure-TTS path.
	gmSpeaker  func(speakerID string) bool
	textPoster func(ctx context.Context, text string) error

	// toolDeps injects the built-in knowledge Tools' read sources (S1, #296);
	// the zero value registers the Tools but reports them unavailable at
	// Execute.
	toolDeps tool.Deps

	// lookahead is the pump look-ahead seam for the Cross-talk Reaction onset
	// gap (#375); nil is the feature-off default (TEXT-only pre-render).
	lookahead orchestrator.LookaheadPump
	// clipReplayLoad + clipReplaySink wire the Highlight voice-replay reactor
	// (#310); a nil loader leaves a ReplayRequested inert.
	clipReplayLoad orchestrator.ClipLoader
	clipReplaySink orchestrator.ClipSink
}

// buildConversation assembles the orchestrator reactive pipeline: VAD (Silero)
// → STT (ElevenLabs) → Address Detection → production Reply (the Agent loop over
// Groq, with the dice Tool granted via the tool-use loop) → TTS (d.synth).
//
// All NPCs share ONE tool-engine (one client, the `dice` grant in code — Tool
// Grants are a #6 table, not yet seeded); only the per-NPC GrantSet and model
// differ (see [conversationDeps] for the per-field contracts). The returned
// Roster is the programmatic control surface for adding/removing NPCs at
// runtime (#49); the caller owns it for the cycle's lifetime.
func buildConversation(d conversationDeps) (*orchestrator.Conversation, *Roster, func(), error) {
	bus, log := d.bus, d.log
	stageMetrics := d.stageMetrics
	if stageMetrics == nil {
		stageMetrics = observe.Discard{}
	}
	if len(d.npcs) == 0 {
		return nil, nil, nil, fmt.Errorf("wirenpc: buildConversation needs at least one NPC")
	}

	engine, err := silero.New(silero.WithMinSilenceFrames(vadMinSilenceFrames))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("init Silero VAD: %w", err)
	}
	vadSession, err := engine.NewSession(vad.Config{
		SampleRate:       vadSampleRate,
		FrameSizeMs:      vadFrameMs,
		SpeechThreshold:  0.5,
		SilenceThreshold: 0.35,
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open VAD session: %w", err)
	}
	// cleanup releases ONLY the per-cycle VAD session (its ONNX inferencer), which
	// a reconnect cycle (issue #44) would otherwise leak on every loop. It must
	// NOT close the engine: silero.Engine wraps the process-global ONNX
	// environment (initialised once via sync.Once and never re-initialised), so
	// engine.Close() → DestroyEnvironment would tear ONNX down for the whole
	// process — the next cycle's NewSession would fail and the NPC would go
	// permanently deaf after the first Discord drop. The engine is a singleton: it
	// lives for the process and is never closed here. session.Close is idempotent.
	cleanup := func() {
		_ = vadSession.Close()
	}
	// vad_hangover (#125): the fixed end-of-speech detection lag is a constant the
	// stage cannot read off the Silero session (vad.Session doesn't expose
	// minSilenceFrames), so compute it here from the same consts the session is
	// configured with — vadMinSilenceFrames*vadFrameMs (= 384 ms).
	vadHangover := vadMinSilenceFrames * vadFrameMs * time.Millisecond
	vadStage := orchestrator.NewVAD(bus, vadSession,
		orchestrator.WithVADMetrics(stageMetrics, vadHangover))

	// Speaker Lanes (ADR-0050): each Discord participant's already-separated frame
	// stream (codec stamps Frame.UserID) opens its own VAD lane, so utterances are
	// segmented and attributed per speaker. The lane factory builds a fresh Silero
	// session PER lane from the SAME process-global engine (NewSession is cheap; the
	// engine is the ONNX singleton, never re-created) and its close releases that
	// lane's ONNX inferencer on reap (risk (b)). The default lane keeps vadSession;
	// the factory builds only the non-default lanes.
	laneVADFactory := func() (*orchestrator.VAD, func(), error) {
		sess, err := engine.NewSession(vad.Config{
			SampleRate:       vadSampleRate,
			FrameSizeMs:      vadFrameMs,
			SpeechThreshold:  0.5,
			SilenceThreshold: 0.35,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("open lane VAD session: %w", err)
		}
		v := orchestrator.NewVAD(bus, sess, orchestrator.WithVADMetrics(stageMetrics, vadHangover))
		return v, func() { _ = sess.Close() }, nil
	}

	// Bounded transient-error retry (#124, ADR-0044): one shared policy threaded
	// into all three provider stages (STT, TTS, LLM). Log-only per-attempt detail;
	// the injected time is the production timer (a live run, not a cassette), and the
	// per-stage deadlines stay the hard bound the loop never extends.
	retryPolicy := retry.Policy{Log: log}

	// One recognizer instance backs both the batch stage and — when streaming is
	// enabled and the adapter supports it — the stream manager (the ElevenLabs
	// Client is both a batch stt.Recognizer and a stt.StreamingRecognizer, ADR-0042).
	var recognizer stt.Recognizer = newSTT(d.keys.stt)
	sttStage := orchestrator.NewSTT(bus, recognizer,
		orchestrator.WithSTTMetrics(stageMetrics, observe.ProviderElevenLabs),
		orchestrator.WithSTTRetry(retryPolicy))
	// tts_total + tts-stage provider health (#125): ElevenLabs is the wired TTS
	// provider (ADR-0039), so the spans are labelled elevenlabs.
	ttsStage := orchestrator.NewTTS(bus, d.synth,
		orchestrator.WithTTSMetrics(stageMetrics, observe.ProviderElevenLabs),
		orchestrator.WithTTSRetry(retryPolicy))
	streamMgr := buildStreamManager(recognizer, d.sttStreaming, stageMetrics, log)
	// Per-lane streaming STT (ADR-0042 × ADR-0050): each Speaker Lane opens its own
	// stream (stamping its SpeakerID on partials) under a concurrency cap, so
	// concurrent sockets track concurrent speakers, not channel size. Nil unless
	// streaming is on AND the adapter streams — the byte-for-byte batch default.
	laneStreamFactory := buildLaneStreamFactory(recognizer, d.sttStreaming, stageMetrics)

	// Tool Grants are now DB-backed and hydrated per NPC (#113, ADR-0029): each
	// NPC carries its own grants (spec.grants — from its tool_agent_grant rows on
	// the DB path, the in-code default on the env-only path), resolved into a
	// per-NPC GrantSet against ONE shared Registry. So the LLM only ever sees the
	// Tools that NPC is granted, and an NPC with no grant rows is shown no Tool at
	// all — least-privilege, data-driven, not compiled in.
	//
	// Groq is the live LLM provider (see the function doc). Its key is keys.llm:
	// the decrypted saved BYOK key (issue #69) when one is configured, otherwise
	// "" so the adapter falls back to GROQ_API_KEY at request time (BYOK,
	// ADR-0004) — export it from the keyring before an env-only live run
	// (docs/agents/live-npc-run.md). There is no Anthropic key, so wiring the
	// Anthropic adapter here would pass the keyless cassette tests (which replay
	// Anthropic) but fail the live run — Groq is the only correct default for a
	// runnable NPC. The Groq client and the Registry are shared across every NPC
	// — only the GrantSet differs per NPC — so N NPCs reuse one client rather than
	// each opening their own.
	// Dispatch the LLM adapter off the Agent's Provider Config provider id (#272):
	// an empty id (env-only path, or a nil LLM config) resolves to the Groq default
	// (ADR-0036), so the seed's groq config stays byte-identical and the keyless
	// cassette gate holds. A saved non-Groq provider now gets its own adapter.
	provider, err := newLLM(d.llmProviderID, d.keys.llm)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("wirenpc: build LLM provider: %w", err)
	}
	reg := tool.BuiltinRegistry(d.toolDeps)
	engineFor := engineFactory(provider, reg, d.language, stageMetrics, llmProviderLabel(d.llmProviderID), retryPolicy)

	// Assemble the initial roster: each AddNPC registers the NPC's routing Agent
	// in the Matcher and its Replier (over a per-NPC engine carrying that NPC's
	// GrantSet) in the Cast. The Matcher is built from the first NPC and grown for
	// the rest.
	roster := newRoster(rosterDepsForLive(d, engineFor, newTTS(d.keys.tts), 16))
	for _, npc := range d.npcs {
		roster.AddNPC(npc)
	}

	// Butler GM-only voice-address gate (#280, ADR-0024): WithButlerGMGate reads
	// the utterance's SpeakerID (ADR-0050 attribution) to keep the Butler a
	// GM-only voice address. A nil gmSpeaker (voice standalone / bench) passes a
	// nil gate — off, so every Butler route publishes exactly as before.
	detector := newAddressDetector(roster.matcher, orchestrator.WithButlerGMGate(d.gmSpeaker))

	conv := orchestrator.NewConversation(bus, vadStage, sttStage, ttsStage,
		orchestrator.WithDetector(detector),
		// B1: stream the reply sentence-by-sentence so first audio begins after the
		// first sentence, not the whole completion. The agenttool Engine implements
		// agent.StreamingEngine (it streams the final answer round), so the Cast's
		// reply stream dispatches each sentence as it lands, to the addressed NPC.
		orchestrator.WithReplyStream(roster.cast.ReplyStream()),
		// Barge-in (ADR-0027): a human talking over Bart cancels his turn. The
		// confirm window must be > 0 against a live mic — a zero window let the
		// addressing user's own continued speech (single shared VAD session, no
		// speaker identity) cancel the turn it had just triggered, which is the 20s
		// self-cancel the latency investigation found. With B1 a confirmed barge
		// cancels mid-generation, not just pending dispatch.
		orchestrator.WithBargeInCoalesce(bargeConfirmWindow, floorCoalesceWindow),
		// Streaming STT (ADR-0042, issue #180): a nil manager is byte-for-byte the
		// batch path, so this option is unconditional — buildStreamManager returns nil
		// unless GLYPHOXA_STT_STREAMING is set AND the adapter is a StreamingRecognizer.
		orchestrator.WithStreamingSTT(streamMgr),
		// Speaker Lanes (ADR-0050): attributed inbound frames open per-speaker VAD
		// lanes so utterances are segmented and attributed independently. A single
		// participant still gets one lane transcribed exactly as today; the default
		// (unattributed) lane carries the silence clock. Unconditional — the factory is
		// only invoked for a non-empty SpeakerID.
		orchestrator.WithSpeakerLanes(laneVADFactory),
		// Per-lane streaming STT under the env cap (ADR-0042 × ADR-0050). A nil factory
		// (batch default) leaves the lanes batch-only, so this is unconditional.
		orchestrator.WithLaneStreamingSTT(laneStreamFactory, streamMaxLanes()),
		// Per-Agent mute (#211): the replier discards a muted addressee's route
		// before taking the floor, and a MuteCut reactor cuts the floor when the
		// speaking Agent is muted. A nil view is the feature-off default (voice
		// standalone / bench), so this option is unconditional.
		orchestrator.WithMute(d.mutes),
		// Per-session spend soft cap (#130, ADR-0046): the replier refuses a NEW turn
		// once the session's estimated spend crosses the soft cap. A nil gate is the
		// feature-off default (no caps configured), so this option is unconditional.
		orchestrator.WithTurnGate(d.gate),
		// GM /say direct speech (#295, ADR-0010): a DirectSpeech reactor renders a
		// SpeakRequested (the /say slash command) to TTS in the addressed NPC's Voice,
		// looked up from THIS roster. It shares the barge-in floor (so a human barge
		// cancels a /say) and the spend gate, but bypasses mute (GM puppeteering). The
		// session Manager publishes SpeakRequested; the lookup is always wired so /say
		// works whenever a session is live.
		orchestrator.WithDirectSpeech(roster.Voice),
		// Pump look-ahead for the Cross-talk Reaction onset gap (#375, ADR-0025): the
		// playback pump doubles as the [orchestrator.LookaheadPump], so a queued
		// Reaction's first sentence pre-renders its audio during the Lead's playback.
		// A nil pump is the feature-off default (TEXT-only pre-render); it only takes
		// effect alongside an Ensemble speaker, so it is safe to wire unconditionally.
		orchestrator.WithReactionLookahead(d.lookahead),
		// Highlight voice replay (#310, ADR-0051): a ClipReplay reactor plays a promoted
		// Highlight's clip into the live voice channel on a ReplayRequested, loading the
		// clip via clipReplayLoad and pushing its chunks to the session's PlaybackPump.
		// It shares the barge-in floor (so a human barge cancels a replay). A nil loader
		// is the feature-off default — the sink is always the live pump, so this is safe
		// to wire unconditionally; Register only binds the reactor when the loader is set.
		orchestrator.WithClipReplay(d.clipReplayLoad, d.clipReplaySink),
		// Handles failures the reactors fire off the audio loop: the replier's TTS
		// dispatch and the segmenter's off-loop STT call (#24). The wrapped error
		// names its stage (orchestrator.TTS.Dispatch / orchestrator.STT.Transcribe).
		orchestrator.WithErrorHandler(func(err error) {
			log.Warn("voice pipeline stage failed", "err", err)
		}),
	)
	return conv, roster, cleanup, nil
}

// engineFactory builds the per-NPC engine constructor buildConversation hands the
// Roster: one shared Groq provider + Registry, one engine per NPC differing only
// by GrantSet and model. It is a named function (not an inline closure) so the
// model-threading contract (#227) is unit-testable with a fake provider that
// captures the [llm.Request].
//
// spec.model — resolved from the Agent's LLM provider_config (loadCampaignNPCs),
// tenant-level row as fallback — flows verbatim into the request. Empty passes
// through as "" so the openaicompat adapter fills [groq.DefaultModel]; there is
// NO defaulting duplicated here (the AC "empty → provider default" is proven at
// the adapter). language selects the dice gate's keyword set (#226). provName is
// the ACTUAL LLM provider (#272): it labels the per-round LLM spans (A3) AND keys
// the spend price lookup, so a non-Groq adapter is not mispriced as groq.
func engineFactory(provider llm.Provider, reg *tool.Registry, language string, stageMetrics observe.StageRecorder, provName observe.Provider, retryPolicy retry.Policy) func(npcSpec) agent.Engine {
	return func(spec npcSpec) agent.Engine {
		return agenttool.NewEngine(provider, tool.NewGrantSet(reg, spec.grants...), spec.agentID, spec.model, 0, 0,
			// The per-round LLM spans (A3) and the spend price lookup are labelled with
			// the actual provider (#272). The no-op recorder keeps the keyless path
			// silent; the live binary / benchmark inject a real one.
			agenttool.WithMetrics(stageMetrics, provName),
			// The dice gate selects its keyword set by Campaign Language (#226), so a
			// German campaign arms the dice Tool for German roll requests instead of
			// gating it out and forcing the model to improvise the roll.
			agenttool.WithLanguage(language),
			// Bounded transient-error retry around the LLM start call (#124, ADR-0044):
			// the same shared policy the STT/TTS stages carry, so one 429/5xx rides
			// through inside the per-turn deadline.
			agenttool.WithRetry(retryPolicy))
	}
}

// buildStreamManager returns the streaming-STT manager when streaming is enabled
// AND the wired recognizer implements [stt.StreamingRecognizer], else nil (the
// byte-for-byte batch default). It is the selection seam (ADR-0042, issue #180):
// keeping it a small pure function lets the gating be unit-tested without standing
// up the whole Silero/ONNX pipeline. The provider label is elevenlabs (the only
// streaming STT adapter in the MVP matrix, ADR-0039).
func buildStreamManager(recognizer stt.Recognizer, streaming bool, stageMetrics observe.StageRecorder, log *slog.Logger) *orchestrator.StreamManager {
	if !streaming {
		return nil
	}
	sr, ok := recognizer.(stt.StreamingRecognizer)
	if !ok {
		// Opting in but the wired provider can't stream is a misconfiguration worth
		// surfacing — otherwise the operator sees the batch path with no hint why.
		if log != nil {
			log.Warn("STT streaming requested but provider does not support it; using batch")
		}
		return nil
	}
	return orchestrator.NewStreamManager(sr,
		orchestrator.WithStreamMetrics(stageMetrics, observe.ProviderElevenLabs))
}

// defaultStreamMaxLanes caps concurrent per-lane streaming-STT sockets (ADR-0050):
// concurrent connections equal concurrent speakers, not channel size. Sized for a
// typical active table; past it a lane transcribes pure batch.
const defaultStreamMaxLanes = 4

// streamMaxLanes reads the per-lane streaming concurrency cap from
// GLYPHOXA_STT_STREAM_MAX_LANES. Absent, empty, or unparseable → [defaultStreamMaxLanes]
// (a typo must not silently change behaviour). An explicit "0" IS honoured — it
// disables per-lane streaming (0 lanes stream) while the default lane keeps its own
// stream; a negative value is invalid and falls back to the default (finding 7).
func streamMaxLanes() int {
	v := os.Getenv("GLYPHOXA_STT_STREAM_MAX_LANES")
	if v == "" {
		return defaultStreamMaxLanes
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return defaultStreamMaxLanes
	}
	return n // 0 = explicitly disabled
}

// buildLaneStreamFactory returns the per-Speaker-Lane [orchestrator.StreamManager]
// factory when streaming is enabled AND the wired recognizer streams, else nil (the
// batch default). It is the lane analogue of [buildStreamManager]: each lane's
// manager stamps its SpeakerID on partials (ADR-0050). The provider label is
// elevenlabs (the only streaming STT adapter in the MVP matrix, ADR-0039).
func buildLaneStreamFactory(recognizer stt.Recognizer, streaming bool, stageMetrics observe.StageRecorder) func(speakerID string) *orchestrator.StreamManager {
	if !streaming {
		return nil
	}
	sr, ok := recognizer.(stt.StreamingRecognizer)
	if !ok {
		return nil
	}
	return func(speakerID string) *orchestrator.StreamManager {
		return orchestrator.NewStreamManager(sr,
			orchestrator.WithStreamMetrics(stageMetrics, observe.ProviderElevenLabs),
			orchestrator.WithStreamSpeakerID(speakerID))
	}
}
