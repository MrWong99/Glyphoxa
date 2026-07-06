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
)

// Campaign-screen CRUD handlers (#71) on CampaignServer: the roster read plus
// agent create/update/delete over the polymorphic agents table (ADR-0009).
// Reads/writes resolve the single operator's active campaign server-side, the
// same single-tenant pass-through GetActiveCampaign uses (ADR-0039); per-tenant
// scoping fills in behind the X-Tenant-Id interceptor later.

// GetCampaignRoster returns the active campaign with its ordered roster: the
// Butler first, then the Character NPCs. The campaign is resolved live-first
// (the live Voice Session's campaign → durable /glyphoxa use selection →
// most-recent fallback) so the Session screen's roster/mute panel lists the NPCs
// actually voicing, not a durable selection changed mid-session (#222). No
// campaign yields CodeNotFound; a missing Butler is an ADR-0009 invariant
// violation (logged, CodeInternal).
func (s *CampaignServer) GetCampaignRoster(
	ctx context.Context,
	_ *connect.Request[managementv1.GetCampaignRosterRequest],
) (*connect.Response[managementv1.GetCampaignRosterResponse], error) {
	c, err := s.activeCampaign(ctx)
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
// server-assigned speaker-colour slot. The role is always 'character'. The active
// campaign is resolved live-first (the live Voice Session's campaign → durable
// /glyphoxa use selection → most-recent fallback), the SAME resolution the roster
// read uses, so mid-session a new NPC lands in the campaign the screen shows —
// never a silent cross-campaign write (#222).
func (s *CampaignServer) CreateAgent(
	ctx context.Context,
	req *connect.Request[managementv1.CreateAgentRequest],
) (*connect.Response[managementv1.CreateAgentResponse], error) {
	c, err := s.activeCampaign(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no active campaign"))
		}
		slog.Default().Error("CreateAgent: get active campaign failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	m := req.Msg
	id, err := s.store.CreateAgent(ctx, storage.NewAgent{
		CampaignID:  c.ID,
		Role:        storage.AgentRoleCharacter,
		Name:        m.GetName(),
		Title:       m.GetTitle(),
		Persona:     m.GetPersona(),
		Voice:       marshalVoice(m.GetVoice()),
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

	m := req.Msg
	updated, err := s.store.UpdateAgent(ctx, storage.AgentUpdate{
		ID:          id,
		Name:        m.GetName(),
		Title:       m.GetTitle(),
		Persona:     m.GetPersona(),
		Voice:       marshalVoice(m.GetVoice()),
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

// voiceBlob is the minimal shape of an Agent's voice JSONB this slice reads and
// writes: a single voice id. The column is JSONB so the shape can grow without a
// migration (ADR-0022/0023); the editor only selects a voice id today.
type voiceBlob struct {
	VoiceID string `json:"voice_id"`
}

// marshalVoice wraps a selected voice id into the voice JSONB. An empty id yields
// the empty object the schema defaults to, so "no voice" round-trips cleanly.
func marshalVoice(voiceID string) []byte {
	if voiceID == "" {
		return []byte(`{}`)
	}
	b, err := json.Marshal(voiceBlob{VoiceID: voiceID})
	if err != nil { // a string field cannot fail to marshal; guard defensively
		return []byte(`{}`)
	}
	return b
}

// unmarshalVoice extracts the voice id from an Agent's voice JSONB, returning ""
// for an empty/unparsable blob.
func unmarshalVoice(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var v voiceBlob
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	return v.VoiceID
}
