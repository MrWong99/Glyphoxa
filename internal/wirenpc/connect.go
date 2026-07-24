package wirenpc

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/snowflake/v2"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	gxvoice "github.com/MrWong99/Glyphoxa/pkg/voice"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/wire"
	"github.com/MrWong99/Glyphoxa/pkg/voice/wire/codec"
)

// publishConnectionState publishes a [voiceevent.ConnectionStateChanged] onto bus,
// unless bus is nil (the env-only/bench paths carry no shared bus). It is the one
// seam the connection-state transitions go through so the nil-guard lives in one
// place (#123).
func publishConnectionState(bus *voiceevent.Bus, state voiceevent.ConnectionState, detail string) {
	if bus == nil {
		return
	}
	bus.Publish(voiceevent.ConnectionStateChanged{At: time.Now(), State: state, Detail: detail})
}

// connectAndServe runs ONE connect-and-serve cycle: build the Discord client,
// open the gateway, join the channel, assemble the pipeline, and pump audio
// until ctx is cancelled or the connection drops. It calls connected() once the
// join succeeds, marking when serving began; the caller resets its backoff only
// if the cycle then survives the healthy threshold (issue #141), so a
// long-lived session that later drops reconnects promptly while a
// join-then-immediate-fail cycle keeps backing off. Any error — or a clean return (a dropped
// gateway often reports none) — flows back to runWithReconnect, which decides
// whether to retry; only a cancelled ctx ends the loop.
// newDiscordClient is the disgo constructor seam (mirrors newLLM/newSTT/newTTS):
// a test swaps it to assert the owned path calls it and the shared-client path
// does NOT. Production always uses disgo.New.
var newDiscordClient = disgo.New

// acquireClient yields the Discord client for one connect-and-serve cycle. When
// cfg.Client is set it BORROWS the standing shared client (already gateway-open,
// owned by the presence) and reports owned=false so the caller never closes it;
// a provider error fails the cycle so runWithReconnect retries. Otherwise it
// builds and opens a per-cycle client (today's behavior) and reports owned=true.
func acquireClient(ctx context.Context, cfg Config, bus *voiceevent.Bus, log *slog.Logger) (client *bot.Client, owned bool, err error) {
	if cfg.Client != nil {
		c, err := cfg.Client(ctx)
		if err != nil {
			return nil, false, fmt.Errorf("wirenpc: standing Discord client unavailable: %w", err)
		}
		return c, false, nil
	}

	// Per-cycle client: DAVE/MLS is wired at construction (it cannot be enabled
	// after disgo builds its VoiceManager). DaveOption() is a no-op stub unless the
	// binary was built with -tags dave; NewManager(WithDave(true)) then warns if
	// encryption was expected but unavailable.
	opts := []bot.ConfigOpt{
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
			gateway.IntentGuilds | gateway.IntentGuildVoiceStates,
		)),
		gxvoice.DaveOption(),
	}
	// Gateway IDENTIFY-budget observability (#486): count this per-cycle client's
	// IDENTIFYs at send time (via the identify rate-limiter wrapper, so a connect
	// that burns budget without reaching Ready still counts) and its RESUMEs on the
	// Resumed event. Only the OWNED client is instrumented here; the borrowed
	// standing client (handled above) is instrumented by the presence, so no
	// establishment is double-counted. A nil budget yields no opts.
	opts = append(opts, GatewayBudgetClientOpts(cfg.Token, cfg.GatewayBudget)...)
	// Standalone voice mode has no boot-owned presence to answer the tape consent
	// disclosure's buttons (#306, finding 5), so THIS per-cycle client must carry
	// the listener itself — otherwise every Consent/Revoke press fails. The all-mode
	// shared client (cfg.Client != nil, handled above) gets it from the presence.
	//
	// It MUST run off the gateway read goroutine: the handler does a DB upsert, a
	// publish, an authoritative reconcile (2nd DB round-trip + tape ctrl) and a
	// Discord REST reply, and stalling the read goroutine on all that misses
	// heartbeats → reconnect churn (the exact ADR-0010 hazard the presence guards
	// with async events — see internal/presence). So enable async event delivery
	// alongside the listener, mirroring defaultClientBuilder. Gated on the listener
	// so a nil-Tape cycle keeps today's synchronous, listener-free client unchanged.
	if l := tapeConsentListener(cfg.TapeConsent, bus, log); l != nil {
		opts = append(opts,
			bot.WithEventListenerFunc(l),
			bot.WithEventManagerConfigOpts(bot.WithAsyncEventsEnabled()),
		)
	}
	c, err := newDiscordClient(cfg.Token, opts...)
	if err != nil {
		return nil, false, fmt.Errorf("wirenpc: build Discord client: %w", err)
	}
	if err := c.OpenGateway(ctx); err != nil {
		c.Close(context.Background())
		return nil, false, fmt.Errorf("wirenpc: open gateway: %w", err)
	}
	return c, true, nil
}

func connectAndServe(ctx context.Context, cfg Config, guild, channel snowflake.ID, log *slog.Logger, connected func()) error {
	// Per-cycle context: everything this cycle spawns — the stage subscriber's
	// TTL-sweep goroutine (stageSub.Start), the Discord gateway, and the audio
	// loop — is bound to cycleCtx, so the deferred cancel reaps it at cycle end.
	// Without this a flapping Discord (issue #44) would leak one sweeper goroutine
	// per reconnect: the outer ctx only ends at process shutdown. cycleCtx is a
	// child of ctx, so a cancelled process still unwinds promptly (pipe.Run et al.
	// observe the cancellation), and runWithReconnect checks the OUTER ctx to
	// decide shutdown vs reconnect.
	cycleCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// The orchestrator bus is created here (not deeper) so the connection-state
	// transitions ride the SAME bus as the pipeline's events (#123). The web tier
	// injects ONE process-wide bus (cfg.Bus, issue #73) so the SSE relay subscribes
	// once and keeps observing events across reconnect cycles and sessions; the
	// env-only voice/bench paths leave it nil and get a fresh per-cycle bus (today's
	// behavior, unchanged). Hoisted above acquireClient so {connecting} is published
	// first thing each cycle, BEFORE the (possibly fatal) Discord client is acquired.
	bus := cfg.Bus
	if bus == nil {
		bus = voiceevent.NewBus()
	}
	publishConnectionState(bus, voiceevent.ConnectionConnecting, "")

	// Discord client: either the standing shared client the presence owns
	// (cfg.Client, #102) or a per-cycle client this loop builds and closes. The
	// shared client is already gateway-open and must NOT be closed here.
	client, owned, err := acquireClient(cycleCtx, cfg, bus, log)
	if err != nil {
		return err
	}
	if owned {
		defer client.Close(context.Background())
	}

	mgr := gxvoice.NewManager(client,
		gxvoice.WithLogger(log),
		gxvoice.WithDave(gxvoice.DaveAvailable()),
		gxvoice.WithMetrics(cfg.Metrics),
	)
	defer mgr.Close()

	sess, err := mgr.Open(cycleCtx, guild, channel)
	if err != nil {
		return fmt.Errorf("wirenpc: join voice channel: %w", err)
	}
	defer sess.Close()
	// The join succeeded: mark when serving began. runWithReconnect resets the
	// backoff only if this cycle survives the healthy threshold (issue #141), so
	// a session that serves for a while and later drops reconnects on the initial
	// delay (issue #44) while a join-then-immediate-fail keeps backing off.
	connected()
	// Announce the Bot is connected and serving so the Session screen flips
	// connecting → connected live (#123). Rides the same bus as {connecting}.
	publishConnectionState(bus, voiceevent.ConnectionConnected, "")
	log.Info("joined voice channel", "guild", guild, "channel", channel, "npcs", npcNames(cfg.npcs))

	// One Codec instance serves both directions: DecodeInbound (called from the
	// single Pipeline.Run goroutine) and PlaybackSource (called from the playback
	// worker) — the codec documents this split as concurrency-safe. codec.New()
	// is the real Opus transcoder under -tags opus and a fail-fast stub
	// (ErrCodecUnavailable) otherwise, so this binary needs no build-tag
	// knowledge: a default build still constructs and runs, just deaf+mute.
	// Living in the shared Run core, this audio path covers BOTH the hardcoded
	// and the RunFromDB paths (RunFromDB resolves the NPC then delegates here).
	//
	// codec_decode / codec_encode (#125): the codec stamps the per-frame Opus<->PCM
	// costs against cfg.StageMetrics. A nil StageMetrics (env-only) keeps the no-op
	// default via WithMetrics; the stub build accepts and ignores the same option.
	cdc := codec.New(codec.WithMetrics(cfg.StageMetrics))

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
	// The orchestrator bus was created at the top of this cycle (so the tee can
	// publish FirstAudio and the pump FirstOpus onto the same bus the conversation's
	// stages publish on and the metrics/SSE subscribers read) and already carries
	// this cycle's connection.state{connecting}/{connected} (#123).
	// tapePumpOptions adds the outbound (agent-speech) tape tap when the campaign is
	// armed (#306); nil tape → no option → unchanged playback.
	pump := wire.NewPlaybackPump(sess, cdc, log, bus, tapePumpOptions(cfg.Tape)...)
	defer pump.Close()

	// cfg.keys.tts is the resolved BYOK TTS key (issue #69): the decrypted saved
	// key when one is configured, or "" to fall back to ELEVENLABS_API_KEY.
	teeSynth := wire.NewTeeSynthesizer(newTTS(cfg.keys.tts), pump, bus)

	// Attach the orchestrator-sibling latency subscriber (A2/#10): it derives the
	// SLO histograms (response_latency, address_detect, per-sentence tts_ttfb) from
	// the turn-correlated bus events and feeds cfg.StageMetrics. Subscribe wires
	// the handlers (deferred unsubscribe); Start runs the TTL sweep for the run's
	// lifetime so abandoned/barged turns don't leak per-turn state. A nil
	// StageMetrics (keyless) makes the subscriber a no-op via observe.Discard.
	stageSub := observe.NewStageSubscriber(cfg.StageMetrics, observe.WithTurnLog(log))
	defer stageSub.Subscribe(bus)()
	stageSub.Start(cycleCtx)

	// Butler text delivery (#299, #297 decision 2): a poster over the BORROWED
	// Discord client that writes into the voice channel's text chat, so a Butler
	// answering as text posts there. It reuses this cycle's client and channel; the
	// TextSink is set on butler-role specs only inside rosterDepsForLive.
	textPoster := newVoiceChannelPoster(client, channel)

	conv, roster, cleanup, err := buildConversation(conversationDeps{
		bus:              bus,
		log:              log,
		npcs:             cfg.npcs,
		language:         cfg.language,
		synth:            teeSynth,
		stageMetrics:     cfg.StageMetrics,
		keys:             cfg.keys,
		llmProviderID:    cfg.llmProviderID,
		sttStreaming:     cfg.STTStreaming,
		memory:           cfg.Memory,
		facts:            cfg.Facts,
		directives:       cfg.Directives,
		speakerName:      cfg.SpeakerName,
		playerCharacters: cfg.playerCharacters,
		mutes:            cfg.Mutes,
		gate:             cfg.Gate,
		gmSpeaker:        cfg.GMSpeaker,
		toolDeps:         cfg.ToolDeps,
		textPoster:       textPoster,
		clipReplayLoad:   cfg.ClipReplayLoader,
		clipReplaySink:   orchestrator.ClipSink(pump.HandleSentence),
	})
	if err != nil {
		return fmt.Errorf("wirenpc: build pipeline: %w", err)
	}
	// cleanup closes the per-cycle VAD session (not the shared Silero engine — see
	// buildConversation). Without it each reconnect cycle (issue #44) would leak a
	// Silero session that nothing ever closed.
	defer cleanup()

	// Mute control (#211): subscribe roster.SetMuted to MuteChanged so a GM mute
	// (web panel or /glyphoxa mute) de-routes the NPC live, and seed the current
	// mute state so a mid-session Discord RECONNECT re-applies the mutes onto the
	// freshly-rebuilt roster. nil Mutes = feature off, so both are inert (voice
	// standalone / bench unchanged).
	defer wireMutes(bus, roster, cfg.Mutes)()

	// Rollover-tape consent (#306, ADR-0051): reseed the tape from the durable
	// consent rows at cycle start and reconcile on every (campaign-filtered)
	// TapeConsentChanged, and post the in-channel consent disclosure with
	// grant/revoke buttons. A nil Tape (campaign not armed) makes both inert, so the
	// loop is unchanged.
	defer wireTapeConsent(cycleCtx, bus, cfg.Tape, cfg.CampaignID, cfg.TapeConsent, cfg.TapeConsentReconcileInterval, log)()
	if cfg.Tape != nil {
		if err := postTapeDisclosure(cycleCtx, client, channel, cfg.CampaignID); err != nil {
			// A failed disclosure post must not tear down the session — capture is
			// still consent-gated (only prior consenters are taped), and the operator
			// can re-post. Log and continue.
			log.Warn("post tape consent disclosure", "err", err)
		}
	}

	// Inbound (hear): the pipeline pumps Session.Inbound through the same Codec's
	// DecodeInbound into the orchestrator. It tags its inbound counters (A2) with
	// the guild and shares the run's MetricsRecorder.
	//
	// WithSilenceClock drives VAD endpointing during the inbound packet gap a
	// paused speaker leaves (issue #91): Discord sends a few Opus silence frames
	// then stops sending entirely, so without a steady silence clock the VAD never
	// sees the trailing silence and each line lands one utterance late. The clock
	// runs at the VAD frame geometry (vadSampleRate/vadFrameMs) so silero endpoints
	// ~vadMinSilenceFrames*vadFrameMs (= 384 ms) after the speaker stops — the
	// natural cadence the bargeConfirm/floorCoalesce windows already account for.
	// Session Highlights detector (#307, ADR-0051): built ONLY when the campaign is
	// armed (Tape set) AND a highlight sink is wired. It subscribes to STTFinal off
	// the bus and consumes the decoded-PCM tap; both are non-blocking (ADR-0020). A
	// nil detector (highlights off, or provider build failed) adds no option and no
	// bus subscriber, so the loop is byte-identical. Per-cycle state: Close on exit
	// (a leak is a #44-class bug).
	detector := buildHighlightDetector(cfg, bus, log)
	if detector != nil {
		defer detector.Close()
	}

	// tapeInboundOptions adds the inbound (consented Speaker) tape tap when armed
	// (#306); nil tape → no option → byte-identical inbound loop. highlightPCMOptions
	// adds the detector's decoded-PCM tap (#307) when the detector is built.
	pipeOpts := append([]wire.Option{wire.WithSilenceClock(vadSampleRate, vadFrameMs)}, tapeInboundOptions(cfg.Tape)...)
	pipeOpts = append(pipeOpts, highlightPCMOptions(detector)...)
	pipe := wire.NewPipeline(conv, cdc, log, cfg.Guild, cfg.Metrics, pipeOpts...)
	return pipe.Run(cycleCtx, sess)
}
