package wirenpc

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"

	"github.com/disgoorg/snowflake/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the database/sql "pgx" driver for the goose-backed schema check

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
	"github.com/MrWong99/Glyphoxa/internal/tape"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// RunFromDB loads the bound Active Campaign's Character NPCs from Postgres (via
// the task-#8 storage layer, scoped by cfg.CampaignID — #323) and runs the live
// voice loop with them, instead of the in-code NPC. The campaign id is the
// selection the Voice Session carries; an empty id fails loudly rather than
// silently voicing the seed roster. pool is an already-open pgxpool the caller
// owns (and closes) — voice mode
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
	// This runs before loadCampaignNPCs (the first query). The schema check needs a
	// database/sql handle (goose's API), which the pgxpool can't provide, so the
	// dsn is recovered from the pool's own config — no second connection string
	// threaded through the callers.
	if err := ensureSchemaCurrent(ctx, pool.Config().ConnString()); err != nil {
		return err
	}

	st := storage.New(pool)
	npcs, primary, campaign, err := loadCampaignNPCs(ctx, st, cfg.CampaignID)
	if err != nil {
		return err
	}
	for _, npc := range npcs {
		log.Info("loaded NPC from DB", "npc", npc.name, "agentID", npc.agentID)
	}
	// Surface the #224 failure mode at session start, loudly: an NPC that hydrated
	// with an empty VoiceID will be silent at synthesis time (elevenlabs.Synthesize
	// rejects an empty Voice.VoiceID). Logging it here — once, at load — beats the
	// per-turn WARN storm that hid the cause during live validation.
	logVoiceGaps(log, npcs)

	// Resolve the session's BYOK keys from the saved provider_config (issue #69).
	// A decryption failure (e.g. a real saved key with the wrong/absent cipher)
	// is fatal here, before any Discord connection — the operator sees a clear
	// error instead of an NPC that silently ran on the wrong (env) key.
	keys, err := resolveSessionKeys(ctx, st, campaign.TenantID, primary, cipher, cfg.KeyEntitlement)
	if err != nil {
		return err
	}

	cfg.npcs = npcs
	cfg.keys = keys
	cfg.llmProviderID = llmProviderID(primary.LLMConfig)
	cfg.language = campaign.Language

	// The campaign's bound player-character names (#276's `character` table) feed
	// the system prompt's speaker-attribution section beside the roster's NPC
	// names, so the model can read the "<Name>:" user-line prefixes the
	// SpeakerName resolver produces. Loaded HERE, beside the roster, so both
	// refresh on the same cadence: once per session start — a Character created
	// mid-session (the same web editor flow that binds Discord users) appears on
	// the next session (re)start, like a mid-session Agent edit.
	chars, err := st.ListCharacters(ctx, cfg.CampaignID)
	if err != nil {
		return fmt.Errorf("wirenpc: load player characters: %w", err)
	}
	for _, c := range chars {
		cfg.playerCharacters = append(cfg.playerCharacters, c.Name)
	}

	// Rollover tape (#306, ADR-0051): armed ONLY when the Campaign opted in
	// (tape_armed). Seed it with the individually-consenting Speakers; agent speech
	// is always captured regardless. The tape lives across reconnect cycles for the
	// whole session and is discarded here at session end — only promoted Highlights
	// outlive it (a later slice). Default OFF: an unarmed campaign gets no tape, no
	// taps, no capture whatsoever.
	if campaign.TapeArmed {
		consented, err := st.ListTapeConsent(ctx, cfg.CampaignID)
		if err != nil {
			return fmt.Errorf("wirenpc: load tape consent: %w", err)
		}
		tp := tape.New(tape.Window, consented, log)
		defer tp.Close()
		cfg.Tape = tp
		cfg.TapeConsent = st
		cfg.TapeConsentReconcileInterval = tapeConsentReconcileInterval(os.Getenv)
		log.Info("rollover tape armed", "campaign", cfg.CampaignID, "consented_speakers", len(consented))
	}

	return Run(ctx, cfg)
}

// logVoiceGaps emits an ERROR for every loaded NPC whose Voice has an empty
// VoiceID (#224, AC6): such an NPC is unsynthesizable and will be silent, so the
// gap is surfaced ONCE at session start instead of failing per-turn at synthesis
// time. It is unconditional on the DB path — the check is the empty VoiceID
// itself, not the NPC's addressability or TTS binding, because an empty VoiceID
// is always the silent-NPC condition.
func logVoiceGaps(log *slog.Logger, npcs []npcSpec) {
	for _, npc := range npcs {
		if npc.voice.VoiceID != "" {
			continue
		}
		// The Butler is a legitimately text-only Agent (#299): a voiceless Butler
		// answers via its TextSink into the channel chat, so an empty VoiceID is
		// expected, not the silent-NPC failure. Surface it at INFO, not ERROR.
		if npc.role == voiceevent.AgentRoleButler {
			log.Info("Butler has no voice configured; it will answer as text only",
				"npc", npc.name, "agentID", npc.agentID)
			continue
		}
		log.Error("NPC has no synthesizable voice (empty VoiceID); it will be silent",
			"npc", npc.name, "agentID", npc.agentID)
	}
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

// EnsureSchemaCurrent is the exported ADR-0031 fail-fast guard for callers that
// must run a DB query BEFORE [RunFromDB] does its own internal check. The
// standalone voice entrypoint resolves the Active Campaign before RunFromDB
// (#323), so it runs this first to keep the stale-schema refusal (the actionable
// `migrate up` message) ahead of any other query. It delegates to the same
// self-contained schema-check seam RunFromDB uses.
func EnsureSchemaCurrent(ctx context.Context, dsn string) error {
	return ensureSchemaCurrent(ctx, dsn)
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
// -tags "opus dave" for a hearing, speaking, encrypted NPC.
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
	runErr := runWithReconnect(ctx, log, defaultReconnectPolicy(),
		func(ctx context.Context, connected func()) error {
			return connectAndServe(ctx, cfg, guild, channel, log, connected)
		})
	// A non-nil return is a FATAL, terminal failure (a classified *FatalError):
	// publish the terminal connection.state{failed} with its readable reason so the
	// Session screen flips to failed without a reload (#123). nil is a clean
	// ctx-cancel shutdown — no failed frame. Nil-guarded: the env-only/bench paths
	// carry no shared bus and this is a no-op there.
	if runErr != nil {
		publishConnectionState(cfg.Bus, voiceevent.ConnectionFailed, runErr.Error())
	}
	return runErr
}
