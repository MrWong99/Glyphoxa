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

// Campaign-screen Tool Grant handlers (#117) on CampaignServer: list every
// built-in Tool with an Agent's current grant state, and toggle a grant on/off
// (editing its scope config) for an Agent. The available-Tools catalog is the
// built-in Registry (ADR-0028), so the toggles a GM sees are exactly the Tools a
// Voice Session runs; a change hydrates into the NEXT session (#113, ADR-0029).
//
// ListToolGrants is a read (NO_SIDE_EFFECTS) so the CSRF interceptor exempts it;
// UpdateToolGrant is state-changing so the auth + CSRF interceptors guard it —
// identical to the sibling agent CRUD mutations (AC5), inherited from the shared
// interceptor stack the web tier mounts, not re-implemented here.

// ListToolGrants returns, for one Agent, every registered Tool paired with the
// Agent's grant state (granted, scope config, whether the Tool supports a scope).
// An unparsable agent_id is CodeInvalidArgument.
func (s *CampaignServer) ListToolGrants(
	ctx context.Context,
	req *connect.Request[managementv1.ListToolGrantsRequest],
) (*connect.Response[managementv1.ListToolGrantsResponse], error) {
	agentID, err := uuid.Parse(req.Msg.GetAgentId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid agent id"))
	}
	// Resolve the active campaign and require the Agent to belong to it (#356): the
	// list is keyed on agent_id alone, so without this an operator on campaign A
	// could READ campaign B's Agent grant configs by id. The scoped check refuses a
	// cross-campaign (or deleted, issue #215) target as CodeNotFound — a clean
	// missing-Agent instead of a fabricated full-catalog-ungranted list — mirroring
	// the sibling UpdateToolGrant write guard.
	c, err := s.activeCampaign(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no active campaign"))
		}
		slog.Default().Error("ListToolGrants: get active campaign failed", "agent_id", agentID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	if err := s.requireAgentInCampaign(ctx, c.ID, agentID); err != nil {
		return nil, err
	}

	grants, err := s.toolGrantsFor(ctx, agentID)
	if err != nil {
		slog.Default().Error("ListToolGrants: read grants failed", "agent_id", agentID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return connect.NewResponse(&managementv1.ListToolGrantsResponse{Grants: grants}), nil
}

// UpdateToolGrant toggles an Agent's grant of one Tool and edits its scope config.
// granted=true upserts the row (with config); granted=false removes it (revoking
// an already-ungranted Tool is a no-op success). It validates the Tool is
// registered and any config is valid JSON, then reads the resulting state back so
// the response is the persisted truth. An unparsable agent_id, an unknown Tool, or
// invalid config is CodeInvalidArgument.
func (s *CampaignServer) UpdateToolGrant(
	ctx context.Context,
	req *connect.Request[managementv1.UpdateToolGrantRequest],
) (*connect.Response[managementv1.UpdateToolGrantResponse], error) {
	m := req.Msg
	agentID, err := uuid.Parse(m.GetAgentId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid agent id"))
	}
	// The Tool must be a registered built-in — a grant naming an unregistered Tool
	// grants access to nothing (ADR-0029), so reject it up front rather than
	// persist a dead row the hydration path would silently skip.
	registered, ok := s.tools.Get(m.GetToolName())
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("unknown tool"))
	}
	// Resolve the active campaign and require the Agent to belong to it (#342): a
	// Tool Grant write is keyed on agent_id alone, so without this an operator on
	// campaign A could grant/revoke Tools on campaign B's Agent (incl. B's Butler).
	// The scoped check refuses a cross-campaign target as CodeNotFound before any
	// write — and still maps a deleted Agent to CodeNotFound (issue #215).
	c, err := s.activeCampaign(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no active campaign"))
		}
		slog.Default().Error("UpdateToolGrant: get active campaign failed", "agent_id", agentID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	if err := s.requireAgentInCampaign(ctx, c.ID, agentID); err != nil {
		return nil, err
	}

	if m.GetGranted() {
		var config json.RawMessage
		if raw := m.GetConfig(); raw != "" {
			if !json.Valid([]byte(raw)) {
				return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("config is not valid JSON"))
			}
			config = json.RawMessage(raw)
		}
		if err := s.store.UpsertToolGrant(ctx, storage.NewToolGrant{
			AgentID:  agentID,
			ToolName: registered.Name(),
			Config:   config,
		}); err != nil {
			slog.Default().Error("UpdateToolGrant: upsert failed", "agent_id", agentID, "tool", registered.Name(), "err", err)
			return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
		}
	} else {
		// Revoke is idempotent: a not-present grant is already in the desired state.
		if err := s.store.DeleteToolGrant(ctx, agentID, registered.Name()); err != nil && !errors.Is(err, storage.ErrNotFound) {
			slog.Default().Error("UpdateToolGrant: delete failed", "agent_id", agentID, "tool", registered.Name(), "err", err)
			return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
		}
	}

	// Read the state back so the client renders persisted truth (jsonb reformats).
	grants, err := s.toolGrantsFor(ctx, agentID)
	if err != nil {
		slog.Default().Error("UpdateToolGrant: read-back failed", "agent_id", agentID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	for _, g := range grants {
		if g.GetToolName() == registered.Name() {
			return connect.NewResponse(&managementv1.UpdateToolGrantResponse{Grant: g}), nil
		}
	}
	// The Tool is registered, so toolGrantsFor always emits an entry for it.
	slog.Default().Error("UpdateToolGrant: toggled tool missing from catalog", "tool", registered.Name())
	return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
}

// requireAgentInCampaign requires an Agent both to exist AND belong to
// campaignID, else CodeNotFound (a missing Agent maps the same way, issue #215).
// Agents never move between Campaigns (campaign_id is immutable), so the
// read-then-compare is race-free — it verifies ownership before a Tool Grant read
// or mutation keyed on agent_id alone can reach another campaign's Agent. Any
// other lookup failure is CodeInternal. Shared by ListToolGrants (#356) and
// UpdateToolGrant (#342).
func (s *CampaignServer) requireAgentInCampaign(ctx context.Context, campaignID, agentID uuid.UUID) error {
	a, err := s.store.GetAgent(ctx, agentID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return connect.NewError(connect.CodeNotFound, errors.New("agent not found"))
		}
		slog.Default().Error("tool grant: agent lookup failed", "agent_id", agentID, "err", err)
		return connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	if a.CampaignID != campaignID {
		// The Agent is in another Campaign — invisible to this operator's session.
		return connect.NewError(connect.CodeNotFound, errors.New("agent not found"))
	}
	return nil
}

// toolGrantsFor joins the built-in Tool catalog with an Agent's persisted grant
// rows into the wire list: one entry per registered Tool (Name order), each
// carrying its description, the Agent's granted bit + scope config, and whether
// the Tool supports a scope. It is the shared body of both grant handlers.
func (s *CampaignServer) toolGrantsFor(ctx context.Context, agentID uuid.UUID) ([]*managementv1.ToolGrant, error) {
	rows, err := s.store.ListToolGrants(ctx, agentID)
	if err != nil {
		return nil, err
	}
	granted := make(map[string]storage.ToolGrant, len(rows))
	for _, r := range rows {
		granted[r.ToolName] = r
	}

	tools := s.tools.Tools()
	out := make([]*managementv1.ToolGrant, 0, len(tools))
	for _, t := range tools {
		entry := &managementv1.ToolGrant{
			ToolName:      t.Name(),
			Description:   t.Description(),
			SupportsScope: t.SupportsScope(),
		}
		if row, ok := granted[t.Name()]; ok {
			entry.Granted = true
			if len(row.Config) > 0 {
				entry.Config = string(row.Config)
			}
		}
		out = append(out, entry)
	}
	return out, nil
}
