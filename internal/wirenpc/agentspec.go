package wirenpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
	"github.com/MrWong99/Glyphoxa/pkg/tool"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/groq"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	ttseleven "github.com/MrWong99/Glyphoxa/pkg/voice/tts/elevenlabs"
)

// This file is the task-#5 DB integration: it seeds the live NPC into Postgres
// using the task-#8 schema, and loads it back so the voice loop is built from a
// DB row instead of the in-code consts. The storage layer stays vendor-neutral;
// the storage.Agent → npcSpec / tts.Voice mapping is voice-domain knowledge and
// lives here.

const (
	// SeedTenantName / SeedCampaignName identify the demo Tenant/Campaign the
	// seed creates. The seed is idempotent on the Tenant name.
	SeedTenantName   = "Glyphoxa Demo"
	SeedCampaignName = "The Prancing Pony"

	// llmProvider / llmModel record the DEPLOYMENT LLM in provider_config.
	// They reference the groq adapter's own identifiers so the seeded row can
	// never drift from what buildConversation actually wires (the DB value is
	// recorded but not yet consumed by adapter selection — see the #5 seam note
	// in wirenpc.go — so silent drift here would mislead later).
	llmProvider = groq.ProviderID
	llmModel    = groq.DefaultModel

	// ttsModel / sttModel are the ElevenLabs models the Voice / STT configs record.
	ttsModel = "eleven_multilingual_v2"
	sttModel = "scribe_v1"

	// credPlaceholderLast4 marks a provider_config whose real key is NOT in the
	// DB: the self-host voice binary reads it from the OS keyring (#10). The
	// encrypted-cred column is the web-app BYOK path (ADR-0004, #6); the seed
	// stores a sealed placeholder so the NOT NULL column is satisfied without
	// persisting a secret.
	credPlaceholderLast4 = "env"

	// diceToolName is the [tool.Tool.Name] of the built-in dice Tool — the demo
	// NPC's (and the auto-Butler's) default Tool Grant (ADR-0009 Q14). The seed
	// grants it to Bart so the live loop hydrates his dice ability from the DB
	// (#113); the Butler's dice grant is seeded by the auto-Butler trigger.
	diceToolName = "dice"
)

// SeedNPC creates the demo Tenant, Campaign (which auto-creates its Butler via
// the ADR-0009 trigger), the Groq-LLM + ElevenLabs-TTS/STT Provider Configs,
// and the "Bart" Character NPC bound to them. It is idempotent: if a Tenant
// named [SeedTenantName] already exists, it does nothing and reports that.
//
// cipher seals the credential placeholders written to provider_config (real
// keys live in the keyring, not the DB — see [credPlaceholderLast4]).
func SeedNPC(ctx context.Context, pool *pgxpool.Pool, cipher *crypto.Cipher, log *slog.Logger) error {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	st := storage.New(pool)

	if _, err := st.FindTenantByName(ctx, SeedTenantName); err == nil {
		log.Info("seed: tenant already present, skipping", "tenant", SeedTenantName)
		return nil
	} else if !errors.Is(err, storage.ErrNotFound) {
		return fmt.Errorf("wirenpc: seed precheck: %w", err)
	}

	npc := hardcodedNPC()

	voiceJSON, err := json.Marshal(npc.voice)
	if err != nil {
		return fmt.Errorf("wirenpc: marshal voice: %w", err)
	}

	// Sealed placeholder credential — never a real key (see credPlaceholderLast4).
	placeholder, err := cipher.Seal([]byte("placeholder: real key in OS keyring"))
	if err != nil {
		return fmt.Errorf("wirenpc: seal credential placeholder: %w", err)
	}

	// All inserts run in one transaction so a partial failure can't leave a
	// half-seeded DB (which the FindTenantByName precheck would then treat as
	// already-seeded and skip forever).
	err = st.InTx(ctx, func(tx *storage.Store) error {
		tenantID, err := tx.CreateTenant(ctx, SeedTenantName)
		if err != nil {
			return err
		}

		// Creating the Campaign fires the auto-Butler trigger: a 'Glyphoxa'
		// Butler row appears here without an explicit insert (ADR-0009).
		campaignID, err := tx.CreateCampaign(ctx, storage.NewCampaign{
			TenantID: tenantID,
			Name:     SeedCampaignName,
			System:   "dnd5e",
			Language: "en",
		})
		if err != nil {
			return err
		}

		llmCfgID, err := tx.CreateProviderConfig(ctx, storage.NewProviderConfig{
			TenantID:              tenantID,
			Component:             storage.ComponentLLM,
			Provider:              llmProvider,
			Model:                 llmModel,
			CredentialsCiphertext: placeholder,
			CredentialsLast4:      credPlaceholderLast4,
		})
		if err != nil {
			return err
		}

		ttsCfgID, err := tx.CreateProviderConfig(ctx, storage.NewProviderConfig{
			TenantID:              tenantID,
			Component:             storage.ComponentTTS,
			Provider:              ttseleven.ProviderID,
			Model:                 ttsModel,
			CredentialsCiphertext: placeholder,
			CredentialsLast4:      credPlaceholderLast4,
		})
		if err != nil {
			return err
		}

		// STT shares the ElevenLabs key; recorded as its own Component row.
		if _, err := tx.CreateProviderConfig(ctx, storage.NewProviderConfig{
			TenantID:              tenantID,
			Component:             storage.ComponentSTT,
			Provider:              ttseleven.ProviderID,
			Model:                 sttModel,
			CredentialsCiphertext: placeholder,
			CredentialsLast4:      credPlaceholderLast4,
		}); err != nil {
			return err
		}

		bartID, err := tx.CreateAgent(ctx, storage.NewAgent{
			CampaignID:            campaignID,
			Role:                  storage.AgentRoleCharacter,
			Name:                  npc.name,
			Persona:               npc.persona,
			Voice:                 voiceJSON,
			VoiceProviderConfigID: uuid.NullUUID{UUID: ttsCfgID, Valid: true},
			LLMProviderConfigID:   uuid.NullUUID{UUID: llmCfgID, Valid: true},
			AddressOnly:           false, // a lone Character NPC catches unaddressed speech
			Aliases:               npc.aliases,
		})
		if err != nil {
			return err
		}

		// Seed Bart's dice Tool Grant so the live loop hydrates his dice ability
		// from the DB (#113) — the persisted equivalent of the in-code default
		// grant this replaces. The auto-Butler's dice grant lands via the trigger
		// (migration 00013); Bart is a Character, so his grant is explicit here.
		_, err = tx.CreateToolGrant(ctx, storage.NewToolGrant{
			AgentID:  bartID,
			ToolName: diceToolName,
		})
		return err
	})
	if err != nil {
		return err
	}

	log.Info("seed: created NPC",
		"tenant", SeedTenantName, "campaign", SeedCampaignName, "npc", npc.name)
	return nil
}

// loadSeededNPCs reads ALL the demo Campaign's Character NPCs from the DB and
// maps each to the npcSpec the voice loop builds its Roster from. The
// auto-created Butler is ignored (it is the slash-command Agent for #6, not
// voiced here). This is the INITIAL roster membership; NPCs join and leave at
// runtime via the programmatic [Roster] API (#49). The single-NPC default of the
// demo seed yields a one-element slice — the pre-Roster behavior unchanged.
//
// AgentID is each Agent's DB UUID as a string — the stable identity Address
// Detection routes on (the in-code path used "bart"; the value only has to be
// consistent between the matcher and the Persona, which it is).
//
// It also surfaces the loaded Campaign row — carrying the owning tenant ID
// (the credential bridge, issue #69) and the Campaign Language (the matcher's
// phonetic scheme, #199) — and the PRIMARY (first) Character's LoadedAgent, the
// bit the credential bridge resolves the session BYOK keys from. The primary's
// LoadedAgent already carries its LLM/TTS provider configs (LoadAgent joins
// them), so this returns them rather than re-querying. A single set of keys
// backs the whole session (one shared Groq client + one ElevenLabs synth across
// the Roster — see buildConversation), so the primary agent is the
// representative; per-NPC adapter selection is a later concern.
func loadSeededNPCs(ctx context.Context, st *storage.Store) ([]npcSpec, storage.LoadedAgent, storage.Campaign, error) {
	tenant, err := st.FindTenantByName(ctx, SeedTenantName)
	if err != nil {
		return nil, storage.LoadedAgent{}, storage.Campaign{}, fmt.Errorf("wirenpc: load NPCs: find tenant: %w", err)
	}

	campaign, err := st.FindCampaignByName(ctx, tenant.ID, SeedCampaignName)
	if err != nil {
		return nil, storage.LoadedAgent{}, storage.Campaign{}, fmt.Errorf("wirenpc: load NPCs: find campaign: %w", err)
	}

	chars, err := st.CharacterAgents(ctx, campaign.ID)
	if err != nil {
		return nil, storage.LoadedAgent{}, storage.Campaign{}, err
	}
	if len(chars) == 0 {
		return nil, storage.LoadedAgent{}, storage.Campaign{}, fmt.Errorf("wirenpc: load NPCs: no Character NPC in %q", SeedCampaignName)
	}

	specs := make([]npcSpec, 0, len(chars))
	var primary storage.LoadedAgent
	for i, c := range chars {
		loaded, err := st.LoadAgent(ctx, c.ID)
		if err != nil {
			return nil, storage.LoadedAgent{}, storage.Campaign{}, err
		}
		if i == 0 {
			primary = loaded
		}
		spec, err := npcSpecFromAgent(loaded.Agent)
		if err != nil {
			return nil, storage.LoadedAgent{}, storage.Campaign{}, err
		}
		// Hydrate this NPC's Tool Grants from its DB rows (#113): tool
		// availability is data-driven, not compiled in. An NPC with no rows gets
		// no grants, so its GrantSet declares no Tool to the LLM (least-privilege).
		//
		// CONTRACT: grants are read ONCE here — at RunFromDB, i.e. per Voice
		// Session start — and the resulting GrantSet is read-only for the
		// session's life. A grant row added or removed mid-session (e.g. a GM
		// toggle via #117) takes effect on the NEXT Voice Session, not the running
		// one.
		grantRows, err := st.ListToolGrants(ctx, c.ID)
		if err != nil {
			return nil, storage.LoadedAgent{}, storage.Campaign{}, err
		}
		spec.grants = grantsFromRows(grantRows)
		specs = append(specs, spec)
	}
	return specs, primary, campaign, nil
}

// grantsFromRows hydrates an Agent's persisted Tool Grant rows into the in-memory
// [tool.Grant]s the voice loop builds its GrantSet from (#113). It delegates to
// the canonical [storage.GrantsFromRows] so the live loop and the grant RPC's
// AC4 hydration test share ONE mapping and can't drift (issue #215) — the
// behaviour is unchanged, this is just the single source of truth.
func grantsFromRows(rows []storage.ToolGrant) []tool.Grant {
	return storage.GrantsFromRows(rows)
}

// npcSpecFromAgent maps a storage.Agent (vendor-neutral) to the voice-domain
// npcSpec, deserializing the opaque Voice JSONB into a tts.Voice.
func npcSpecFromAgent(a storage.Agent) (npcSpec, error) {
	var voice tts.Voice
	if len(a.Voice) > 0 {
		if err := json.Unmarshal(a.Voice, &voice); err != nil {
			return npcSpec{}, fmt.Errorf("wirenpc: unmarshal voice for agent %s: %w", a.ID, err)
		}
	}
	// A nil Settings serializes to the JSON literal `null`, which unmarshals
	// back into a non-nil json.RawMessage("null"). Normalize it to nil so a
	// settings-less Voice round-trips identically.
	if string(voice.Settings) == "null" {
		voice.Settings = nil
	}
	return npcSpec{
		agentID: a.ID.String(),
		name:    a.Name,
		persona: a.Persona,
		voice:   voice,
		aliases: a.Aliases,
	}, nil
}
