package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts/elevenlabs"
)

// Campaign-screen CRUD handlers (#71) on CampaignServer: the roster read plus
// agent create/update/delete over the polymorphic agents table (ADR-0009).
// Reads/writes resolve the single operator's active campaign server-side, the
// same single-tenant pass-through GetActiveCampaign uses (ADR-0039); per-tenant
// scoping fills in behind the X-Tenant-Id interceptor later.

// GetCampaignRoster returns the active campaign with its ordered roster: the
// Butler first, then the Character NPCs. No campaign yields CodeNotFound; a
// missing Butler is an ADR-0009 invariant violation (logged, CodeInternal).
func (s *CampaignServer) GetCampaignRoster(
	ctx context.Context,
	_ *connect.Request[managementv1.GetCampaignRosterRequest],
) (*connect.Response[managementv1.GetCampaignRosterResponse], error) {
	c, err := s.store.GetActiveCampaign(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no active campaign"))
		}
		slog.Default().Error("GetCampaignRoster: get active campaign failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	butler, err := s.store.GetButler(ctx, c.ID)
	if err != nil {
		// The Butler is auto-created with the campaign (ADR-0009); its absence is
		// an invariant violation, not a client error. Log raw, return generic.
		slog.Default().Error("GetCampaignRoster: get butler failed", "campaign_id", c.ID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	chars, err := s.store.CharacterAgents(ctx, c.ID)
	if err != nil {
		slog.Default().Error("GetCampaignRoster: list character agents failed", "campaign_id", c.ID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	roster := make([]*managementv1.Agent, 0, len(chars)+1)
	roster = append(roster, toProtoAgent(butler))
	for _, a := range chars {
		roster = append(roster, toProtoAgent(a))
	}

	return connect.NewResponse(&managementv1.GetCampaignRosterResponse{
		Campaign: toProtoCampaign(c),
		Roster:   roster,
	}), nil
}

// CreateAgent adds a Character NPC to the active campaign and returns it with its
// server-assigned speaker-colour slot. The role is always 'character'.
func (s *CampaignServer) CreateAgent(
	ctx context.Context,
	req *connect.Request[managementv1.CreateAgentRequest],
) (*connect.Response[managementv1.CreateAgentResponse], error) {
	c, err := s.store.GetActiveCampaign(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no active campaign"))
		}
		slog.Default().Error("CreateAgent: get active campaign failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	m := req.Msg
	id, err := s.store.CreateAgent(ctx, storage.NewAgent{
		CampaignID: c.ID,
		Role:       storage.AgentRoleCharacter,
		Name:       m.GetName(),
		Title:      m.GetTitle(),
		Persona:    m.GetPersona(),
		// A new agent has no persisted voice yet; the active campaign's language
		// seeds the first-save default when a voice is picked (#224).
		Voice:       applyVoiceSelection(nil, m.GetVoice(), c.Language),
		AddressOnly: m.GetAddressOnly(),
		Aliases:     m.GetAliases(),
	})
	if err != nil {
		slog.Default().Error("CreateAgent: store create failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	// Read the row back so the response carries the assigned speaker-colour slot
	// and the canonical persisted shape.
	created, err := s.store.GetAgent(ctx, id)
	if err != nil {
		slog.Default().Error("CreateAgent: read-back failed", "agent_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return connect.NewResponse(&managementv1.CreateAgentResponse{Agent: toProtoAgent(created)}), nil
}

// UpdateAgent saves an agent's editor fields and returns the updated agent. The
// store force-keeps a Butler's role and Address-Only (ADR-0009 / ADR-0024). An
// unparsable id is CodeInvalidArgument; a missing id is CodeNotFound.
func (s *CampaignServer) UpdateAgent(
	ctx context.Context,
	req *connect.Request[managementv1.UpdateAgentRequest],
) (*connect.Response[managementv1.UpdateAgentResponse], error) {
	id, err := uuid.Parse(req.Msg.GetId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid agent id"))
	}

	// Read the current row + its campaign so applyVoiceSelection can preserve the
	// persisted voice tuning the editor never sees (ProviderID/Language/Settings)
	// and seed a first-save default in the campaign's language (#224). GetAgent
	// also gives the authoritative NotFound before the write.
	existing, err := s.store.GetAgent(ctx, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("agent not found"))
		}
		slog.Default().Error("UpdateAgent: read existing agent failed", "agent_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	campaign, err := s.store.GetCampaign(ctx, existing.CampaignID)
	if err != nil {
		slog.Default().Error("UpdateAgent: read campaign failed", "agent_id", id, "campaign_id", existing.CampaignID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	m := req.Msg
	updated, err := s.store.UpdateAgent(ctx, storage.AgentUpdate{
		ID:          id,
		Name:        m.GetName(),
		Title:       m.GetTitle(),
		Persona:     m.GetPersona(),
		Voice:       applyVoiceSelection(existing.Voice, m.GetVoice(), campaign.Language),
		AddressOnly: m.GetAddressOnly(),
		Aliases:     m.GetAliases(),
	})
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("agent not found"))
		}
		slog.Default().Error("UpdateAgent: store update failed", "agent_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return connect.NewResponse(&managementv1.UpdateAgentResponse{Agent: toProtoAgent(updated)}), nil
}

// DeleteAgent removes a Character NPC. Deleting the Butler is CodeFailedPrecondition
// (ADR-0009); an unparsable id is CodeInvalidArgument; a missing id is CodeNotFound.
func (s *CampaignServer) DeleteAgent(
	ctx context.Context,
	req *connect.Request[managementv1.DeleteAgentRequest],
) (*connect.Response[managementv1.DeleteAgentResponse], error) {
	id, err := uuid.Parse(req.Msg.GetId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid agent id"))
	}

	switch err := s.store.DeleteAgent(ctx, id); {
	case err == nil:
		return connect.NewResponse(&managementv1.DeleteAgentResponse{}), nil
	case errors.Is(err, storage.ErrButlerUndeletable):
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the butler cannot be deleted"))
	case errors.Is(err, storage.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, errors.New("agent not found"))
	default:
		slog.Default().Error("DeleteAgent: store delete failed", "agent_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
}

// toProtoAgent maps a storage.Agent onto its wire representation. The encrypted
// provider bindings are intentionally not exposed (ADR-0004); voice is reduced
// to its opaque voice id.
func toProtoAgent(a storage.Agent) *managementv1.Agent {
	return &managementv1.Agent{
		Id:           a.ID.String(),
		CampaignId:   a.CampaignID.String(),
		Role:         string(a.Role),
		Name:         a.Name,
		Title:        a.Title,
		Persona:      a.Persona,
		Voice:        unmarshalVoice(a.Voice),
		AddressOnly:  a.AddressOnly,
		SpeakerColor: uint32(a.SpeakerColor),
		Aliases:      a.Aliases,
	}
}

// applyVoiceSelection folds the editor's bare voice-id selection (ADR-0039 keeps
// the wire contract a plain string) into the canonical voice JSONB the voice
// pipeline reads (#224). It preserves the persisted tuning the editor never sees:
//
//   - empty id → {} : clearing the voice, the schema default (today's semantics).
//   - first save / an old {"voice_id":…} drift row / an unparsable blob → the
//     documented ElevenLabs default for this id + the campaign language, so a
//     first UI save fills provider/Settings and a silent drift row self-heals on
//     the next edit — no re-pick needed.
//   - same id as already persisted → the existing bytes verbatim (a tuned
//     Settings blob is never clobbered).
//   - changed id → keep the existing ProviderID/Language/Settings, swap the
//     VoiceID, reset the display Name (it named the old voice).
//
// existing is the row's current voice column (nil on create); campaignLanguage is
// the active/owning campaign's language, used only to seed a first-save default.
func applyVoiceSelection(existing json.RawMessage, voiceID, campaignLanguage string) []byte {
	if voiceID == "" {
		return []byte(`{}`)
	}
	current, err := storage.VoiceFromJSON(existing)
	if err != nil || current.VoiceID == "" {
		// First save, a cleared/empty blob, or an old {"voice_id":…} drift row (all
		// read back with an empty VoiceID): fill the documented defaults.
		return defaultVoiceJSON(voiceID, campaignLanguage)
	}
	if current.VoiceID == voiceID {
		return existing // unchanged selection — preserve the persisted bytes exactly
	}
	// Changed selection: keep the provider tuning, swap the id, drop the stale Name.
	current.VoiceID = voiceID
	current.Name = ""
	b, err := storage.VoiceToJSON(current)
	if err != nil { // a well-formed Voice cannot fail to marshal; guard defensively
		return defaultVoiceJSON(voiceID, campaignLanguage)
	}
	return b
}

// defaultVoiceJSON is the canonical first-save default voice blob for a selected
// id: elevenlabs.DefaultVoice serialized through the canonical mapper. Falls back
// to {} only if marshaling somehow fails (it cannot for a fixed struct).
func defaultVoiceJSON(voiceID, language string) []byte {
	b, err := storage.VoiceToJSON(elevenlabs.DefaultVoice(voiceID, language))
	if err != nil {
		return []byte(`{}`)
	}
	return b
}

// unmarshalVoice extracts the voice id from an Agent's voice JSONB via the
// canonical reader (#224) — so a seed/pipeline-shaped row shows its voice in the
// editor instead of "Pick a voice…". An empty/unparsable blob yields "".
func unmarshalVoice(raw []byte) string {
	v, err := storage.VoiceFromJSON(raw)
	if err != nil {
		return ""
	}
	return v.VoiceID
}
