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
	"log/slog"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/highlight"
	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/tape"
	"github.com/MrWong99/Glyphoxa/pkg/tool"
	gxvoice "github.com/MrWong99/Glyphoxa/pkg/voice"
	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	ttseleven "github.com/MrWong99/Glyphoxa/pkg/voice/tts/elevenlabs"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
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
	// grants are this NPC's Tool Grants (ADR-0029): the DB-path (RunFromDB)
	// hydrates them from the Agent's tool_agent_grant rows, the env-only path
	// uses the in-code default. buildConversation resolves each into a
	// per-NPC GrantSet against the shared Registry, so the LLM only ever sees the
	// Tools this NPC is granted (#113). nil ⇒ granted nothing.
	grants []tool.Grant
	// role is this Agent's Role (CONTEXT.md "Agent Role"): "butler" or "character"
	// (empty ⇒ character, the pre-#299 default). It flows into the routing target's
	// AgentRole so the matcher's Butler GM-gate and the detector can tell the two
	// apart, and it gates the Butler-only wiring (default persona, text delivery,
	// voice-gap logging) in loadCampaignRoster / rosterDepsForLive.
	role string
	// addressOnly marks an Agent reachable only by an explicit name match (the
	// Butler, ADR-0024): matcherAgent stamps it onto the address.Agent so ambient
	// heuristics (continuation, single-NPC fallback) never route to it. false ⇒ a
	// normal Character NPC (the pre-#299 default).
	addressOnly bool
	// model is the Groq model id this NPC's engine runs (#227): loadCampaignNPCs
	// resolves it from the Agent's LLM provider_config, falling back to the
	// tenant-level LLM row. Empty means "adapter default" — it flows verbatim into
	// [llm.Request.Model], where the openaicompat adapter fills [groq.DefaultModel]
	// — so there is no defaulting duplicated here. Read once per session start
	// (like keys and grants); a change applies on the NEXT Voice Session.
	model string
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
		// The env-only path has no DB to hydrate from, so it carries the in-code
		// default grant (dice) directly — the persisted equivalent the seed writes
		// for the DB path (#113).
		grants: []tool.Grant{{ToolName: diceToolName}},
	}
}

// ClientProvider yields the standing shared Discord client a Voice Session cycle
// borrows (ADR-0010 amendment, #102). It returns an error while the presence is
// in its wait-state (no Bot token) or rebuilding; the reconnect loop treats that
// as a transient failure and retries. See [Config.Client].
type ClientProvider func(ctx context.Context) (*bot.Client, error)

// Config configures a [Run] of the live NPC voice loop.
type Config struct {
	// Token is the Discord bot token (from DISCORD_BOT_TOKEN). Required.
	Token string
	// Guild and Channel are the Discord snowflake IDs of the server and voice
	// channel to join. Required.
	Guild   string
	Channel string
	// CampaignID is the bound Active Campaign whose roster this loop voices (#323).
	// RunFromDB loads THIS campaign's Character NPCs + Language via the
	// campaign-scoped loader — it never resolves the seed campaign by name. The
	// session Manager sets it in Start alongside Token/Guild/Channel (the id is
	// already in hand from CreateVoiceSession); the standalone voice-mode entrypoint
	// resolves it from the Active-Campaign policy before calling RunFromDB. An
	// uuid.Nil value is a caller bug: RunFromDB refuses to start loudly rather than
	// silently voicing the wrong (seed) roster.
	CampaignID uuid.UUID
	// Client, when non-nil, is the standing shared Discord client the boot-owned
	// presence owns (ADR-0010 amendment, #102): connectAndServe borrows it per
	// cycle instead of constructing its own with disgo.New / OpenGateway, and does
	// NOT close it at cycle end (the presence owns its lifecycle). A provider error
	// (the presence is in its wait-state, or rebuilding) fails the cycle, so
	// runWithReconnect backs off and retries — self-healing across presence
	// rebuilds. nil keeps today's per-cycle client (env-only voice mode, unchanged).
	Client ClientProvider
	// GatewayBudget observes this loop's gateway session establishments, classifying
	// IDENTIFY vs RESUME and warning as the per-application IDENTIFY budget nears
	// Discord's 1000/token/24h limit (#486). It is attached ONLY to a per-cycle
	// (owned) client this loop builds; the borrowed standing client (Client != nil)
	// is instrumented by the presence that owns it, so it is not double-counted. nil
	// disables the observation (env-only bench paths).
	GatewayBudget GatewayBudgetRecorder
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
	// llmProviderID is the primary Agent's LLM Provider Config provider id (#272):
	// buildConversation dispatches [newLLM] off it so a DB Agent bound to a non-Groq
	// LLM provider gets that adapter. Empty (the env-only Run path, or a nil LLM
	// config) resolves to the Groq default (ADR-0036) — byte-identical to the
	// pre-#272 hardwired constructor.
	llmProviderID string
	// language is the Campaign Language (CONTEXT.md) of the campaign the npcs
	// were loaded from: it selects the Roster matcher's phonetic encoder (#199).
	// RunFromDB sets it from the campaign row; the env-only Run path leaves it
	// "", which — like any code without a registered encoder — resolves to "en"
	// (see matcherLanguage), preserving the pre-#199 behavior.
	language string
	// playerCharacters are the campaign's bound player-character names (#276's
	// `character` table): with SpeakerName wired they render the system prompt's
	// speaker-attribution section ([agent.Config.PlayerCharacters]) beside the
	// sibling-NPC names derived from npcs, so the model can read the "<Name>:"
	// user-line prefixes instead of guessing from its persona. RunFromDB loads
	// them beside the roster — once per session start, so a Character created
	// mid-session appears on the next session (re)start, exactly the roster's
	// refresh cadence. Empty (the env-only Run path) renders no section.
	playerCharacters []string

	// Memory is the NPC memory recaller injected into every NPC's Agent loop
	// (#122, ADR-0011/0042): it fills the Hot Context memory slot each turn. The
	// web tier resolves one recaller (over the shared embeddings provider + the
	// process store/bus) and sets it on the session Manager's base config; it flows
	// through connectAndServe → buildConversation → the Roster. nil (voice/bench
	// standalone, or an unavailable embeddings/DB path) disables recall entirely,
	// so Agent turns behave exactly as before (AC6).
	Memory agent.MemoryRecaller

	// Facts is the NPC KG-facts recaller injected into every NPC's Agent loop (#126,
	// ADR-0008): it fills the reserved Hot Context KG-facts slot each turn with the
	// Campaign's gm-public Node facts. The web tier resolves one recaller (over the
	// process store + session Manager) and sets it on the session Manager's base
	// config; it flows through connectAndServe → buildConversation → the Roster like
	// Memory. nil (voice/bench standalone) disables facts entirely, so the prompt is
	// byte-identical to the pre-facts behavior (#126).
	Facts agent.FactsRecaller

	// SpeakerName resolves a route's SpeakerID (ADR-0050) to the human speaker's
	// display name for the agent-facing transcript: every NPC's Agent loop
	// prefixes its user lines "<Name>: <text>" (the transcript-names seam,
	// [agent.Config.SpeakerName]). The web tier wires the speaker.Resolver's
	// cache-only Lookup over the active session's Campaign — it must never
	// block (it runs on the turn's hot path; the resolver is warmed on
	// VADSpeechStart). nil (voice/bench standalone) disables attribution: the
	// prompt is byte-identical to the pre-seam behavior.
	SpeakerName func(speakerID string) string

	// Mutes is the live per-Agent mute view (#211): the session Manager satisfies it
	// (its volatile, session-local mute set), and the web tier sets it on the base
	// voice config so every manager-started session copies it. It flows through
	// connectAndServe (which subscribes roster.SetMuted to MuteChanged and seeds the
	// current mute state on connect — a mid-session Discord reconnect re-applies the
	// mutes) → buildConversation (orchestrator.Barge.Mutes). nil is the feature-off
	// default: voice standalone / the benchmark are byte-for-byte unchanged.
	Mutes orchestrator.MuteView

	// Gate is the live turn gate (#130, ADR-0046): the session Manager's spend meter
	// satisfies it (AllowTurn() false once the soft cap is crossed), and Start sets
	// it on the per-session config copy so buildConversation wires it via
	// orchestrator.Barge.Gate. It flows straight into the replier's pre-Take gate,
	// beside the mute check. nil is the feature-off default: no caps configured, so
	// voice standalone / the benchmark / an uncapped session are byte-for-byte
	// unchanged.
	Gate orchestrator.TurnGate

	// ToolDeps injects the built-in knowledge Tools' read sources (S1, #296): the
	// transcript-search and KG-query retrieval paths the tool-use loop calls. The
	// web tier builds the internal/knowledge adapter over the process store +
	// session Manager and sets it on the session Manager's base config; it flows
	// through connectAndServe → buildConversation → tool.BuiltinRegistry(cfg.ToolDeps),
	// so a live NPC granted kg_query/transcript_search actually reaches the DB. The
	// zero value (voice/bench standalone) registers the Tools but reports them
	// unavailable at Execute — the loop feeds that back and continues, never panics.
	ToolDeps tool.Deps

	// Tape, when non-nil, is the rollover tape (#306, ADR-0051) this Voice Session
	// captures into: connectAndServe wires the inbound Opus tap (consented Speaker
	// audio) and the outbound Opus tap (agent speech, always on) into it, posts the
	// in-channel consent disclosure after joining, and subscribes tape.SetConsent to
	// TapeConsentChanged. nil means the Campaign is not armed (default OFF) — NO
	// tape, NO taps, NO disclosure, so the loop is byte-identical to the pre-tape
	// path. RunFromDB constructs it (from campaign.tape_armed + the consent set) and
	// owns its Close; it lives across reconnect cycles for the whole session.
	Tape *tape.Tape
	// TapeConsent is the durable consent surface the tape reseeds from and the
	// voice-mode consent buttons write to (#306): ListTapeConsent authoritatively
	// reseeds the tape each cycle (so a revoke during a reconnect gap still lands),
	// and Upsert/DeleteTapeConsent back the standalone voice-mode client's own
	// consent-button listener (all mode's presence owns its own). Set by RunFromDB
	// alongside Tape; nil when the campaign is not armed.
	TapeConsent TapeConsentStore
	// TapeConsentReconcileInterval is how often the per-cycle consent poller re-reads
	// the durable tape_consent rows (#492): a consent button is dispatched by the
	// elected presence OWNER, which publishes TapeConsentChanged on ITS OWN process
	// bus — but in the fleet the tape may run on a DIFFERENT pod (a claim-plane
	// worker) whose bus never sees that event. The poller closes that cross-pod gap
	// so a grant/revoke converges on the tape within one interval, bounding staleness.
	// Read from GLYPHOXA_TAPE_CONSENT_RECONCILE_INTERVAL by RunFromDB; <=0 takes the
	// 5s default. Wired only when Tape is non-nil.
	TapeConsentReconcileInterval time.Duration

	// Highlights, when non-nil, is the Session Highlights trigger sink (#307/#308,
	// ADR-0051): connectAndServe builds the moment detector ONLY when both Tape and
	// Highlights are set (a detector with no tape to cut clips from is pointless) and
	// wires its PCM tap into the inbound pipeline. The detector watches STTFinal, runs
	// the #305 classifier over the recent transcript window, and hands promoted
	// [highlight.Trigger]s to this sink. nil (default) = highlights off, so the loop
	// is byte-identical. The detector is per Voice Session cycle; connectAndServe
	// owns its Close.
	Highlights highlight.Sink

	// ClipReplayLoader, when non-nil, loads a promoted Session Highlight's clip by
	// its blob key and decodes it into playable chunks for the voice-replay path
	// (#310, ADR-0005: the ReplayRequested event carries the KEY, this resolves it).
	// The live binary sets it to blob.Get + mixdown.DecodeWAV; nil (default) leaves
	// the ClipReplay reactor unwired, so a ReplayRequested is inert. The playback
	// sink is always the session's own PlaybackPump.
	ClipReplayLoader orchestrator.ClipLoader

	// GMSpeaker reports whether a Discord SpeakerID belongs to a Game Master —
	// the tenant-operator binding union the env allowlist per ADR-0055 (amending
	// ADR-0050's allowlist-membership clause; the deterministic GM identity with
	// no per-session binding stands). When non-nil it arms the Butler GM-only
	// voice-address gate (ADR-0024): a Butler-addressed utterance routes only
	// from a GM SpeakerID, and fails closed on any other or empty one; Character
	// NPC routing is untouched. The predicate must never block — it runs inside
	// address detection; the live binary sets it to auth.GMIdentity's
	// snapshot-cached check (cmd/glyphoxa, web/all AND standalone voice mode).
	// nil is the feature-off default (the -hardcoded no-DB smoke path / the
	// benchmark), so the gate is absent and every Butler route publishes as
	// before.
	GMSpeaker func(speakerID string) bool

	// KeyEntitlement gates the session's provider-key env fallback behind the
	// tenant's platform-key entitlement (ADR-0054 seam (a), ADR-0055): a
	// resolution landing on "" (no Provider Config row, or the seeded "env"
	// placeholder) is refused for a tenant the entitlement does not grant,
	// instead of silently spending the deployment's *_API_KEY Platform Keys.
	// nil (default) and the composition root's `allowlist`-Admission-Mode
	// EnvFallbackAllowed wiring grant everything — the ADR-0039 hybrid policy
	// unchanged; `open` Admission Mode wires llmbuild.SubscriptionKeyGate.
	KeyEntitlement llmbuild.PlatformKeyEntitlement
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
	// Delegate to the canonical ElevenLabs default (the same one the web editor's
	// first save writes) and add the seed's display Name, so this seed source and
	// the RPC first-save default stay byte-identical (#224).
	v := ttseleven.DefaultVoice(elevenGeorgeVoiceID, "en")
	v.Name = "Bart"
	return v
}
