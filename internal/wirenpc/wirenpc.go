// Package wirenpc constructs the one hardcoded Character NPC live voice loop for
// the MVP `voice` mode (task #4): it builds the Discord audio Manager, joins a
// voice channel, assembles the orchestrator pipeline with the production Agent
// loop as the ReplyFunc, and runs the audio loop via pkg/voice/wire.
//
// "Hardcoded" means the NPC's Persona, Voice, and provider selection live in
// code here (no DB); task #5 swaps this for a DB-loaded Agent. Credentials are
// runtime-only and never compiled in: the Discord token and provider API keys
// come from the environment or, on the DB-load path, from the decrypted saved
// provider_config (BYOK, ADR-0004/0039, issue #69).
package wirenpc

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/snowflake/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the database/sql "pgx" driver for the goose-backed schema check

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
	"github.com/MrWong99/Glyphoxa/pkg/tool"
	gxvoice "github.com/MrWong99/Glyphoxa/pkg/voice"
	"github.com/MrWong99/Glyphoxa/pkg/voice/address"
	"github.com/MrWong99/Glyphoxa/pkg/voice/agenttool"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/groq"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt"
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

	// bargeConfirmWindow is how long continuous inbound speech must persist before
	// it counts as a barge and yields Bart's floor (ADR-0027). It must be > 0
	// against a live mic: with a single shared VAD session (ADR-0019) the events
	// carry no speaker identity, so the addressing user's OWN continued speech —
	// or a VAD split of one utterance into two segments — fires a fresh
	// speech_start while Bart holds the floor. A zero window yields on that instant
	// and cancels the in-flight TTS POST (the "context canceled" self-cancel that
	// produced the 20s wait — see docs/latency-investigation/audio-process.md).
	// 250ms debounces a speaker finishing their own sentence from a genuine
	// interruption; it is the minimum until per-participant VAD (ADR-0019) can gate
	// barge on speaker != the turn's addresser.
	bargeConfirmWindow = 250 * time.Millisecond

	// floorCoalesceWindow closes root cause #2 of the latency investigation: a
	// turn's unit is a VAD segment, not a user utterance, so one utterance split by
	// VAD into two STT segments opens two turns and the second's Floor.Take cancels
	// the first mid-synthesis (a self-cancel with no barge). A Floor.Take landing
	// within this window of the previous one is treated as the same utterance
	// continuing and yields to the in-flight turn instead of superseding it, so one
	// utterance maps to one turn. Sized a hair above the end-of-speech hangover
	// (vadMinSilenceFrames*vadFrameMs = 12*32 = 384ms) so two segments split at an
	// internal pause coalesce, while a genuine new utterance after a real
	// conversational gap (turn-taking pauses run hundreds of ms longer) still opens
	// its own turn.
	floorCoalesceWindow = 600 * time.Millisecond
)

// reconnectPolicy bounds how the live voice loop backs off between failed or
// dropped Discord connections (issue #44). A serving voice pod must not
// crashloop because Discord is briefly unreachable: Run keeps serving /healthz
// and /readyz (DB-backed) and retries on this schedule instead of exiting.
// Capped exponential, no jitter — one pod reconnecting to one gateway has no
// thundering herd to spread.
type reconnectPolicy struct {
	initial time.Duration
	max     time.Duration
	factor  float64
	// healthyAfter is how long a cycle must serve after connected() fires before
	// it counts as a healthy session and resets the backoff to initial. A cycle
	// that joins but fails sooner (codec-less build, broken ONNX init — issue
	// #141) is a connect failure: the delay keeps growing to its cap instead of
	// retrying the Discord voice join at 1 Hz forever. Zero means reset on join.
	healthyAfter time.Duration
	// sleep blocks for d or until ctx is cancelled (returns ctx.Err() if
	// cancelled first). Injected so tests drive the backoff without real waits.
	sleep func(ctx context.Context, d time.Duration) error
	// now reports the current time for the healthyAfter measurement. Injected so
	// tests fake a long-serving session in milliseconds; nil means time.Now.
	now func() time.Time
}

// healthySessionDuration is how long a session must serve post-join before the
// reconnect backoff forgives past failures and resets to the initial delay.
// Sized to the backoff cap: a session must outlive the maximum backoff before
// the loop trusts it, so a persistent join-then-fail cycle (issue #141) settles
// at cap cadence instead of resetting to 1 Hz.
const healthySessionDuration = 30 * time.Second

func defaultReconnectPolicy() reconnectPolicy {
	return reconnectPolicy{
		initial:      time.Second,
		max:          30 * time.Second,
		factor:       2,
		healthyAfter: healthySessionDuration,
		sleep:        sleepCtx,
		now:          time.Now,
	}
}

// sleepCtx blocks for d or until ctx is cancelled, returning ctx.Err() on
// cancel. A timer (not time.Sleep) so a cancelled ctx returns immediately.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func nextDelay(d time.Duration, p reconnectPolicy) time.Duration {
	next := time.Duration(float64(d) * p.factor)
	if next > p.max {
		return p.max
	}
	return next
}

// runWithReconnect calls attempt repeatedly, keeping the process alive across
// failed or dropped Discord connections. It returns nil (clean shutdown) ONLY
// when ctx is cancelled; every other return from attempt — an error OR a clean
// session-close (nil) — is a lost connection and triggers a backed-off
// reconnect. attempt is handed a connected callback it calls once the join
// succeeds; the backoff resets to initial only if the cycle then serves for at
// least p.healthyAfter (issue #141), so a long-lived session that later drops
// reconnects promptly while a join-then-immediate-fail cycle keeps growing its
// delay instead of hammering the Discord voice join at 1 Hz.
func runWithReconnect(ctx context.Context, log *slog.Logger, p reconnectPolicy, attempt func(ctx context.Context, connected func()) error) error {
	now := p.now
	if now == nil {
		now = time.Now
	}
	delay := p.initial
	for {
		// connectedAt is written by the callback inside attempt and read after
		// attempt returns — connectAndServe invokes it synchronously, same
		// goroutine, so no lock. The delay is only consumed post-return, so
		// deciding the reset here is equivalent to arming a timer on connect.
		var connectedAt time.Time
		err := attempt(ctx, func() { connectedAt = now() })
		if ctx.Err() != nil {
			return nil // shutdown requested — stop retrying, exit clean (fixes SIGTERM->exit1)
		}
		if !connectedAt.IsZero() && now().Sub(connectedAt) >= p.healthyAfter {
			delay = p.initial // served healthily — forgive past failures (issue #44)
		}
		if err != nil {
			log.Warn("voice connection failed; reconnecting", "err", err, "backoff", delay)
		} else {
			log.Info("voice session ended; reconnecting", "backoff", delay)
		}
		if serr := p.sleep(ctx, delay); serr != nil {
			return nil // ctx cancelled during backoff — clean shutdown
		}
		delay = nextDelay(delay, p)
	}
}

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
	// STTStreaming opts the voice loop into the streaming-STT transport (ADR-0042,
	// issue #180): when the wired STT recognizer supports it, utterances stream over
	// a persistent websocket and finalize on a local-VAD manual commit, with the
	// batch adapter as automatic fallback. Default OFF reproduces today's batch path
	// byte-for-byte. Parsed from GLYPHOXA_STT_STREAMING in cmd/glyphoxa.
	STTStreaming bool
	// Bus, when non-nil, is the process-wide voiceevent.Bus the orchestrator
	// publishes onto INSTEAD of a fresh per-cycle bus (issue #73, ADR-0014).
	// The web tier injects ONE bus so the SSE transcript relay can subscribe
	// once and keep seeing events across reconnect cycles AND across sessions
	// (single active session, ADR-0039). nil (the env-only voice/bench paths)
	// keeps today's behavior: connectAndServe makes its own per-cycle bus.
	Bus *voiceevent.Bus
	// npcs are the Character NPCs this loop voices. Run resolves them: RunFromDB
	// loads all Character NPCs in the campaign from storage; the env-only Run path
	// seeds the single hardcoded NPC when the slice is empty. The roster the loop
	// assembles from these is the INITIAL membership; NPCs join and leave at
	// runtime via the programmatic [Roster] API (issue #49).
	npcs []npcSpec
	// keys are the resolved per-component BYOK API keys (issue #69): RunFromDB
	// decrypts the saved provider_config credentials into them under the hybrid
	// policy (ADR-0039), and connectAndServe/buildConversation hand them to the
	// adapters. An empty key means "adapter ENV fallback", so the zero value (the
	// env-only Run path) reproduces today's behavior untouched.
	keys providerKeys
}

// RunFromDB loads the seeded Character NPCs from Postgres (via the task-#8
// storage layer) and runs the live voice loop with them, instead of the in-code
// NPC. pool is an already-open pgxpool the caller owns (and closes) — voice mode
// opens exactly one pool that ALSO backs the /readyz probe, and all/web mode
// hands in its existing request pool, so the voice path never opens a second
// duplicate handle. This is the task-#5 DB-load path: the only thing it changes
// versus [Run] is the *source* of the NPC's Persona/Voice/identity — the
// assembled pipeline is identical.
//
// cipher decrypts the saved BYOK provider credentials (issue #69, ADR-0004): a
// real saved key (last4 != "env") drives the session decrypted, while the seeded
// "env" placeholder falls back to the adapter's own env var (the hybrid policy,
// ADR-0039). A nil cipher is fine when every config is the env placeholder — the
// no-$GLYPHOXA_SECRET self-host path — but a real saved key with no cipher is a
// clear startup error, never a silent fall back to ENV.
func RunFromDB(ctx context.Context, cfg Config, pool *pgxpool.Pool, cipher *crypto.Cipher) error {
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	// Fail fast on a stale schema BEFORE any other DB interaction (ADR-0031):
	// serving Modes (voice) never auto-migrate, so a DB behind the embedded
	// migrations must refuse to start with the actionable `migrate up` message
	// rather than running queries against a schema the code no longer matches.
	// This runs before loadSeededNPCs (the first query). The schema check needs a
	// database/sql handle (goose's API), which the pgxpool can't provide, so the
	// dsn is recovered from the pool's own config — no second connection string
	// threaded through the callers.
	if err := ensureSchemaCurrent(ctx, pool.Config().ConnString()); err != nil {
		return err
	}

	st := storage.New(pool)
	npcs, primary, tenantID, err := loadSeededNPCs(ctx, st)
	if err != nil {
		return err
	}
	for _, npc := range npcs {
		log.Info("loaded NPC from DB", "npc", npc.name, "agentID", npc.agentID)
	}

	// Resolve the session's BYOK keys from the saved provider_config (issue #69).
	// A decryption failure (e.g. a real saved key with the wrong/absent cipher)
	// is fatal here, before any Discord connection — the operator sees a clear
	// error instead of an NPC that silently ran on the wrong (env) key.
	keys, err := resolveSessionKeys(ctx, st, tenantID, primary, cipher)
	if err != nil {
		return err
	}

	cfg.npcs = npcs
	cfg.keys = keys
	return Run(ctx, cfg)
}

// ensureSchemaCurrent verifies the DB at dsn is migrated to the latest embedded
// schema version, returning the storage layer's actionable version-mismatch
// error (verbatim) if it is behind. This is the ADR-0031 fail-fast guard for
// serving Modes: [RunFromDB] calls it once at startup, after the pool opens and
// before any other query, so a process can never serve against a stale schema.
//
// [storage.EnsureCurrent] needs a database/sql handle on the pgx stdlib driver
// (goose's API; the app's own queries use the pgxpool). That handle exists only
// for this check, so it is opened from the same dsn and closed immediately —
// keeping the seam free of the live voice loop and Discord, so it is testable on
// its own against a real Postgres.
func ensureSchemaCurrent(ctx context.Context, dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("wirenpc: open schema-check handle: %w", err)
	}
	defer db.Close()
	return storage.EnsureCurrent(ctx, db)
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
	if len(cfg.npcs) == 0 {
		cfg.npcs = []npcSpec{hardcodedNPC()}
	}

	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	// Config validation is fatal: a bad guild/channel ID can never succeed, so
	// retrying would crashloop slowly. Parse before the reconnect loop so only
	// genuinely transient connection failures are retried.
	guild, err := snowflake.Parse(cfg.Guild)
	if err != nil {
		return fmt.Errorf("wirenpc: parse guild ID %q: %w", cfg.Guild, err)
	}
	channel, err := snowflake.Parse(cfg.Channel)
	if err != nil {
		return fmt.Errorf("wirenpc: parse channel ID %q: %w", cfg.Channel, err)
	}

	// Keep serving across a briefly unreachable or dropped Discord instead of
	// exiting (issue #44): cmd/glyphoxa's metrics server (which carries /healthz
	// and the DB-backed /readyz) lives for ctx independently of this loop, so a
	// reconnecting voice loop lets the Deployment reach Available without live
	// Discord creds. Each cycle is one connectAndServe; runWithReconnect backs
	// off between cycles and returns clean only when ctx is cancelled.
	//
	// Note: disgo runs its own bounded reconnect during OpenGateway, so this
	// policy governs the inter-cycle gap and post-join drops (a session that joins
	// then later disconnects), not the initial dial retries disgo already handles.
	return runWithReconnect(ctx, log, defaultReconnectPolicy(),
		func(ctx context.Context, connected func()) error {
			return connectAndServe(ctx, cfg, guild, channel, log, connected)
		})
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

	if err := client.OpenGateway(cycleCtx); err != nil {
		return fmt.Errorf("wirenpc: open gateway: %w", err)
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
	log.Info("joined voice channel", "guild", guild, "channel", channel, "npcs", npcNames(cfg.npcs))

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
	// The orchestrator bus is created here (not inside buildConversation) so the
	// tee can publish FirstAudio (A3 hook 1) and the pump can publish FirstOpus
	// (task #7, the audible-on-wire SLO end) onto the same bus the conversation's
	// stages publish on and the metrics subscriber reads.
	//
	// The web tier injects ONE process-wide bus (cfg.Bus, issue #73) so the SSE
	// transcript relay subscribes once and keeps observing events across reconnect
	// cycles and sessions; the env-only voice/bench paths leave it nil and get a
	// fresh per-cycle bus (today's behavior, unchanged).
	bus := cfg.Bus
	if bus == nil {
		bus = voiceevent.NewBus()
	}

	pump := wire.NewPlaybackPump(sess, cdc, log, bus)
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

	conv, _, cleanup, err := buildConversation(bus, log, cfg.npcs, teeSynth, cfg.StageMetrics, cfg.keys, cfg.STTStreaming)
	if err != nil {
		return fmt.Errorf("wirenpc: build pipeline: %w", err)
	}
	// cleanup closes the per-cycle VAD session (not the shared Silero engine — see
	// buildConversation). Without it each reconnect cycle (issue #44) would leak a
	// Silero session that nothing ever closed.
	defer cleanup()

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
	pipe := wire.NewPipeline(conv, cdc, log, cfg.Guild, cfg.Metrics,
		wire.WithSilenceClock(vadSampleRate, vadFrameMs))
	return pipe.Run(cycleCtx, sess)
}

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

// npcNames returns the NPCs' display names for a log line.
func npcNames(npcs []npcSpec) []string {
	names := make([]string, len(npcs))
	for i, n := range npcs {
		names[i] = n.name
	}
	return names
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

// Provider-adapter constructors, injected as package vars so a test can spy on
// the apiKey each component receives (issue #69). The adapters expose no key
// getter, so this is the seam that pins the resolved BYOK key reaching its OWN
// adapter — a slot swap (e.g. groq.New(keys.stt)) or a dropped `cfg.keys = keys`
// would otherwise revert the feature to ENV while every providerKeys{} test
// stayed green. Production always uses the real constructors.
var (
	newLLM = groq.New
	newSTT = stteleven.New
	newTTS = ttseleven.New
)

// buildConversation assembles the orchestrator reactive pipeline: VAD (Silero)
// → STT (ElevenLabs) → Address Detection → production Reply (the Agent loop over
// Groq, with the dice Tool granted via the tool-use loop) → TTS (synth).
//
// keys are the resolved BYOK provider keys (issue #69, hybrid policy ADR-0039):
// each adapter is constructed with its component's key, which OVERRIDES that
// adapter's *_API_KEY env var — except an empty key (the env placeholder, or the
// env-only [Run] path) keeps today's behavior, where the adapter reads its env
// var at request time (ADR-0004). So a saved key drives the session and an
// unconfigured component falls back to ENV.
//
// npcs supplies the INITIAL Character NPCs the loop voices — their addressable
// identity, Persona, and Voice (from the in-code seed or, via [RunFromDB], the
// database). It assembles them into a [Roster] (one address Matcher + one Cast):
// the detector routes against the Matcher and the reply stream multiplexes across
// the Cast, so an utterance naming an NPC is answered in that NPC's Voice and a
// lone NPC still catches unaddressed speech. The returned Roster is the
// programmatic control surface for adding/removing NPCs at runtime (#49); the
// caller owns it for the cycle's lifetime. npcs must be non-empty.
//
// All NPCs share ONE Groq tool-engine (one client, the `dice` grant in code —
// Tool Grants are a #6 table, not yet seeded). The LLM provider is Groq (model
// llama-3.3-70b-versatile via the OpenAI-compat endpoint). The DB Agent's
// provider_config provider/model is recorded but adapter selection is not yet
// driven by it; the wired adapter is Groq for any NPC in this tree. Keyless
// cassette tests replay the Anthropic adapter behind the same llm.Provider
// interface.
//
// synth is the [tts.Synthesizer] the TTS stage drives. [Run] passes a
// [wire.TeeSynthesizer] wrapping the real ElevenLabs synthesizer so the
// synthesized audio is tee'd to the playback path while the orchestrator keeps
// draining-and-dropping it (ADR-0021); a bare ElevenLabs synthesizer also works
// (no audio is played). It must not be nil.
func buildConversation(bus *voiceevent.Bus, log *slog.Logger, npcs []npcSpec, synth tts.Synthesizer, stageMetrics observe.StageRecorder, keys providerKeys, streaming bool) (*orchestrator.Conversation, *Roster, func(), error) {
	if stageMetrics == nil {
		stageMetrics = observe.Discard{}
	}
	if len(npcs) == 0 {
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
	vadStage := orchestrator.NewVAD(bus, vadSession)

	// One recognizer instance backs both the batch stage and — when streaming is
	// enabled and the adapter supports it — the stream manager (the ElevenLabs
	// Client is both a batch stt.Recognizer and a stt.StreamingRecognizer, ADR-0042).
	var recognizer stt.Recognizer = newSTT(keys.stt)
	sttStage := orchestrator.NewSTT(bus, recognizer,
		orchestrator.WithSTTMetrics(stageMetrics, observe.ProviderElevenLabs))
	ttsStage := orchestrator.NewTTS(bus, synth)
	streamMgr := buildStreamManager(recognizer, streaming, stageMetrics)

	// The `dice` grant stays in code: Tool Grants are a #6 table, not yet
	// seeded. With no grants the tool engine degrades to a single completion
	// through the same path.
	//
	// Groq is the live LLM provider (see the function doc). Its key is keys.llm:
	// the decrypted saved BYOK key (issue #69) when one is configured, otherwise
	// "" so the adapter falls back to GROQ_API_KEY at request time (BYOK,
	// ADR-0004) — export it from the keyring before an env-only live run
	// (docs/agents/live-npc-run.md). There is no Anthropic key, so wiring the
	// Anthropic adapter here would pass the keyless cassette tests (which replay
	// Anthropic) but fail the live run — Groq is the only correct default for a
	// runnable NPC. One engine is shared across every NPC in the Roster — they
	// reuse one client rather than each opening their own.
	provider := newLLM(keys.llm)
	reg := tool.NewRegistry()
	reg.MustRegister(tool.NewDice())
	grants := tool.NewGrantSet(reg, tool.Grant{ToolName: "dice"})
	toolEngine := agenttool.NewEngine(provider, grants, groq.DefaultModel, 0, 0,
		// Groq is the wired provider (see the function doc), so the per-round
		// LLM spans (A3) are labelled groq. The no-op recorder keeps the keyless
		// path silent; the live binary / benchmark inject a real one.
		agenttool.WithMetrics(stageMetrics, observe.ProviderGroq))

	// Assemble the initial roster: each AddNPC registers the NPC's routing Agent
	// in the Matcher and its Replier (over the shared engine) in the Cast. The
	// Matcher is built from the first NPC and grown for the rest.
	roster := newRoster(rosterDepsForLive(toolEngine, newTTS(keys.tts), 16, log))
	for _, npc := range npcs {
		roster.AddNPC(npc)
	}

	detector := orchestrator.NewAddressDetector(roster.matcher)

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
		// Handles failures the reactors fire off the audio loop: the replier's TTS
		// dispatch and the segmenter's off-loop STT call (#24). The wrapped error
		// names its stage (orchestrator.TTS.Dispatch / orchestrator.STT.Transcribe).
		orchestrator.WithErrorHandler(func(err error) {
			log.Warn("voice pipeline stage failed", "err", err)
		}),
	)
	return conv, roster, cleanup, nil
}

// buildStreamManager returns the streaming-STT manager when streaming is enabled
// AND the wired recognizer implements [stt.StreamingRecognizer], else nil (the
// byte-for-byte batch default). It is the selection seam (ADR-0042, issue #180):
// keeping it a small pure function lets the gating be unit-tested without standing
// up the whole Silero/ONNX pipeline. The provider label is elevenlabs (the only
// streaming STT adapter in the MVP matrix, ADR-0039).
func buildStreamManager(recognizer stt.Recognizer, streaming bool, stageMetrics observe.StageRecorder) *orchestrator.StreamManager {
	if !streaming {
		return nil
	}
	sr, ok := recognizer.(stt.StreamingRecognizer)
	if !ok {
		return nil
	}
	return orchestrator.NewStreamManager(sr,
		orchestrator.WithStreamMetrics(stageMetrics, observe.ProviderElevenLabs))
}
