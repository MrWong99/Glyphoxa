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

// loadCampaignNPCs is the runtime roster loader (#323): it resolves ONE Campaign
// BY ID — the bound Active Campaign the Voice Session carries — and reads its
// Character NPCs + Language, replacing the seed-name resolution [loadSeededNPCs]
// used before. There is NO seed-name fallback here (decision 3): an empty
// campaignID is a caller bug (the loop config never received the selected
// campaign), so it fails loudly rather than silently voicing the seed roster, and
// an unknown id surfaces GetCampaign's ErrNotFound. This is what makes the voiced
// roster / TTS voices / Campaign Language follow the operator's selection on
// multi-campaign installs, and what lets an unseeded install (whose tenant is
// "Glyphoxa", not "Glyphoxa Demo") voice its own campaign instead of hard-failing
// on the seed tenant lookup.
func loadCampaignNPCs(ctx context.Context, st *storage.Store, campaignID uuid.UUID) ([]npcSpec, storage.LoadedAgent, storage.Campaign, error) {
	if campaignID == uuid.Nil {
		return nil, storage.LoadedAgent{}, storage.Campaign{}, fmt.Errorf("wirenpc: load NPCs: no Active Campaign bound to the Voice Session (empty campaign id); select a campaign before starting a session")
	}
	campaign, err := st.GetCampaign(ctx, campaignID)
	if err != nil {
		return nil, storage.LoadedAgent{}, storage.Campaign{}, fmt.Errorf("wirenpc: load NPCs: get campaign %s: %w", campaignID, err)
	}
	return loadCampaignRoster(ctx, st, campaign)
}

// loadCampaignRoster is the shared roster hydration the loaders end in: given a
// resolved Campaign, read its Character NPCs and map each to the npcSpec the voice
// loop builds its Roster from. The auto-created Butler is ignored (it is the
// slash-command Agent, not voiced here — decision 5 / #299). Split out so the
// runtime by-id loader ([loadCampaignNPCs], #323) and the seed-name test helper
// (loadSeededNPCs, in the integration tests) share ONE mapping and can't drift.
//
// It surfaces the loaded Campaign row — carrying the owning tenant ID (the
// credential bridge, #69) and the Campaign Language (the matcher's phonetic
// scheme, #199) — and the PRIMARY (first) Character's LoadedAgent, from which the
// credential bridge resolves the session BYOK keys (a single key set backs the
// whole Roster; per-NPC adapter selection is a later concern). The contract below
// (tenant-model fallback, once-per-session grant read) is unchanged.
func loadCampaignRoster(ctx context.Context, st *storage.Store, campaign storage.Campaign) ([]npcSpec, storage.LoadedAgent, storage.Campaign, error) {
	chars, err := st.CharacterAgents(ctx, campaign.ID)
	if err != nil {
		return nil, storage.LoadedAgent{}, storage.Campaign{}, err
	}
	if len(chars) == 0 {
		return nil, storage.LoadedAgent{}, storage.Campaign{}, fmt.Errorf("wirenpc: load NPCs: no Character NPC in %q", campaign.Name)
	}

	// The tenant-level LLM model is the fallback for any NPC not bound to its own
	// LLM provider_config (#227): web-created Agents carry no LLMProviderConfigID,
	// but the Configuration screen writes the tenant row — without this fallback
	// the model fix would miss the operator's own NPCs. Fetched ONCE per session
	// start (consistent with the credential/grant hydration); no row → "" so the
	// adapter default applies.
	tenantLLMModel := ""
	if cfg, err := st.GetProviderConfigByComponent(ctx, campaign.TenantID, storage.ComponentLLM); err == nil {
		tenantLLMModel = cfg.Model
	} else if !errors.Is(err, storage.ErrNotFound) {
		return nil, storage.LoadedAgent{}, storage.Campaign{}, fmt.Errorf("wirenpc: load NPCs: tenant LLM config: %w", err)
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
		// Resolve the model this NPC's engine runs (#227): the Agent's bound LLM
		// provider_config model when it has one, else the tenant-level fallback.
		// Empty stays empty so the openaicompat adapter fills groq.DefaultModel.
		spec.model = resolveNPCModel(loaded.LLMConfig, tenantLLMModel)
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

// resolveNPCModel picks the model id for one NPC's engine (#227): the Agent's own
// bound LLM provider_config model wins when present and non-empty, otherwise the
// tenant-level LLM model fallback. Both empty yields "", which the openaicompat
// adapter reads as "use the provider default" — the defaulting lives at the
// adapter, never duplicated here.
func resolveNPCModel(bound *storage.ProviderConfig, tenantModel string) string {
	if bound != nil && bound.Model != "" {
		return bound.Model
	}
	return tenantModel
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
	// Delegate to the canonical storage.VoiceFromJSON mapper so the live loop and
	// the Campaign RPC editor read the voice column through ONE decoder and can't
	// drift (#224) — the same single-source pattern as grantsFromRows (#215). The
	// null-Settings normalization lives in the mapper now.
	voice, err := storage.VoiceFromJSON(a.Voice)
	if err != nil {
		return npcSpec{}, fmt.Errorf("wirenpc: unmarshal voice for agent %s: %w", a.ID, err)
	}
	return npcSpec{
		agentID: a.ID.String(),
		name:    a.Name,
		persona: a.Persona,
		voice:   voice,
		aliases: a.Aliases,
	}, nil
}
