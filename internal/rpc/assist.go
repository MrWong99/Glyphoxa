package rpc

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/assist"
	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// campaignAssist is the on-demand LLM campaign-creation feature module (#479):
// persona drafting, knowledge-draft generation (preview-only), and the
// GM-confirmed atomic draft apply. Generation runs ONLY on an explicit GM
// request — no handler here is ever called without a button press, and the
// generate handlers persist nothing. The engine is wired at boot via
// [CampaignServer.SetAssist]; when absent (a test composition that doesn't
// exercise it), the generate handlers fail with CodeUnavailable.
type campaignAssist struct {
	store  assistStore
	active *activeCampaignSource
	engine AssistEngine
}

// assistStore is the narrow store surface the module needs; *storage.Store
// satisfies it. GetAgent seasons the persona draft; ApplyKnowledgeDraft lands a
// confirmed draft atomically.
type assistStore interface {
	GetAgent(ctx context.Context, id uuid.UUID) (storage.Agent, error)
	ApplyKnowledgeDraft(ctx context.Context, campaignID uuid.UUID, nodes []storage.NewKGNode, edges []storage.KnowledgeDraftEdge) ([]storage.KGNode, []storage.KGEdge, error)
}

// AssistEngine is the drafting surface [assist.Engine] provides; the seam a
// unit test fakes to drive the handlers without an LLM.
type AssistEngine interface {
	GeneratePersona(ctx context.Context, campaign storage.Campaign, in assist.PersonaInput) (string, error)
	GenerateKnowledge(ctx context.Context, campaign storage.Campaign, prompt string) (assist.Draft, error)
}

// maxAssistPromptChars bounds the GM prompt — a belt-and-braces spend guard
// (the prompt seasons a money-spending LLM call) and a junk-input gate.
const maxAssistPromptChars = 4000

// validAssistPrompt trims the prompt and rejects empty/oversized input with
// CodeInvalidArgument.
func validAssistPrompt(raw string) (string, *connect.Error) {
	p := strings.TrimSpace(raw)
	if p == "" {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("prompt must not be empty"))
	}
	if len(p) > maxAssistPromptChars {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("prompt is too long"))
	}
	return p, nil
}

// assistEngineErr maps a drafting failure onto its Connect code: a refused
// platform-key entitlement is an actionable precondition (save a key, ADR-0054);
// an unusable model response is CodeUnavailable (a retry may succeed); anything
// else is logged raw and returned as a static CodeUnavailable — provider
// failures are the expected failure mode here, not an internal fault.
func assistEngineErr(op string, err error) *connect.Error {
	switch {
	case errors.Is(err, llmbuild.ErrNoPlatformKeyEntitlement):
		return connect.NewError(connect.CodeFailedPrecondition, llmbuild.ErrNoPlatformKeyEntitlement)
	case errors.Is(err, assist.ErrUnusableResponse):
		return connect.NewError(connect.CodeUnavailable, errors.New("the model returned an unusable draft — try again or rephrase the prompt"))
	default:
		slog.Default().Error(op+": assist engine failed", "err", err)
		return connect.NewError(connect.CodeUnavailable, errors.New("content generation failed — check the LLM provider configuration and try again"))
	}
}

// GeneratePersona drafts a Persona for one Agent from the GM's short prompt
// (#479). The request's live editor name/title season the draft, falling back
// to the stored Agent fields when unset (#480). The draft is returned for
// review in the editor — never persisted. An
// empty/oversized prompt or unparsable agent_id is CodeInvalidArgument; a
// missing or cross-campaign agent is CodeNotFound; drafting failures map via
// assistEngineErr. State-changing (spends provider quota): auth + CSRF guard it.
func (s *campaignAssist) GeneratePersona(
	ctx context.Context,
	req *connect.Request[managementv1.GeneratePersonaRequest],
) (*connect.Response[managementv1.GeneratePersonaResponse], error) {
	if s.engine == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("content generation is not configured"))
	}
	agentID, err := uuid.Parse(req.Msg.GetAgentId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid agent id"))
	}
	prompt, perr := validAssistPrompt(req.Msg.GetPrompt())
	if perr != nil {
		return nil, perr
	}

	c, err := s.active.resolve(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no active campaign"))
		}
		slog.Default().Error("GeneratePersona: get active campaign failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	agent, err := s.store.GetAgent(ctx, agentID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("agent not found"))
		}
		slog.Default().Error("GeneratePersona: read agent failed", "agent_id", agentID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	if agent.CampaignID != c.ID {
		// The Agent is in another Campaign — invisible to this operator's session.
		return nil, connect.NewError(connect.CodeNotFound, errors.New("agent not found"))
	}

	// The editor's live (possibly unsaved) name/title win over the stored Agent
	// fields (#480). Unset falls back to the stored value; set-but-empty means
	// the GM cleared the field, so nothing must resurrect the stored one.
	name, title := agent.Name, agent.Title
	if req.Msg.Name != nil {
		name = req.Msg.GetName()
	}
	if req.Msg.Title != nil {
		title = req.Msg.GetTitle()
	}

	persona, err := s.engine.GeneratePersona(ctx, c, assist.PersonaInput{
		AgentName:  name,
		AgentTitle: title,
		Prompt:     prompt,
	})
	if err != nil {
		return nil, assistEngineErr("GeneratePersona", err)
	}
	return connect.NewResponse(&managementv1.GeneratePersonaResponse{Persona: persona}), nil
}

// GenerateKnowledge drafts a set of linked Knowledge Graph entries from the
// GM's short prompt (#479). PREVIEW-ONLY: nothing is written until the GM
// confirms via ApplyGeneratedKnowledge. An empty/oversized prompt is
// CodeInvalidArgument; drafting failures map via assistEngineErr.
// State-changing (spends provider quota): auth + CSRF guard it.
func (s *campaignAssist) GenerateKnowledge(
	ctx context.Context,
	req *connect.Request[managementv1.GenerateKnowledgeRequest],
) (*connect.Response[managementv1.GenerateKnowledgeResponse], error) {
	if s.engine == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("content generation is not configured"))
	}
	prompt, perr := validAssistPrompt(req.Msg.GetPrompt())
	if perr != nil {
		return nil, perr
	}

	c, err := s.active.resolve(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no active campaign"))
		}
		slog.Default().Error("GenerateKnowledge: get active campaign failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	draft, err := s.engine.GenerateKnowledge(ctx, c, prompt)
	if err != nil {
		return nil, assistEngineErr("GenerateKnowledge", err)
	}

	nodes := make([]*managementv1.DraftNode, len(draft.Nodes))
	for i, n := range draft.Nodes {
		nodes[i] = &managementv1.DraftNode{
			NodeType:  toProtoNodeType(storage.KGNodeType(n.Type)),
			Name:      n.Name,
			Body:      n.Body,
			GmPrivate: n.GMPrivate,
		}
	}
	edges := make([]*managementv1.DraftEdge, len(draft.Edges))
	for i, e := range draft.Edges {
		edges[i] = &managementv1.DraftEdge{
			FromIndex: uint32(e.FromIndex),
			ToIndex:   uint32(e.ToIndex),
			EdgeType:  toProtoEdgeType(storage.KGEdgeType(e.Type)),
		}
	}
	return connect.NewResponse(&managementv1.GenerateKnowledgeResponse{Nodes: nodes, Edges: edges}), nil
}

// ApplyGeneratedKnowledge lands a GM-confirmed (possibly client-side edited)
// knowledge draft atomically (#479): every Node and Edge in one transaction,
// with CreateNode/CreateEdge's validation. An empty draft, invalid node
// type/name, unknown edge type, or out-of-range/self edge index is
// CodeInvalidArgument; a matrix-invalid or duplicate edge is
// CodeFailedPrecondition (the whole draft rolls back). State-changing: auth +
// CSRF guard it.
func (s *campaignAssist) ApplyGeneratedKnowledge(
	ctx context.Context,
	req *connect.Request[managementv1.ApplyGeneratedKnowledgeRequest],
) (*connect.Response[managementv1.ApplyGeneratedKnowledgeResponse], error) {
	m := req.Msg
	if len(m.GetNodes()) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("draft has no entries"))
	}

	nodes := make([]storage.NewKGNode, len(m.GetNodes()))
	for i, n := range m.GetNodes() {
		nodeType, ok := toStorageNodeType(n.GetNodeType())
		if !ok {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("node type must be specified"))
		}
		if strings.TrimSpace(n.GetName()) == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name must not be empty"))
		}
		nodes[i] = storage.NewKGNode{
			Type:      nodeType,
			Name:      strings.TrimSpace(n.GetName()),
			Body:      n.GetBody(),
			GMPrivate: n.GetGmPrivate(),
		}
	}
	edges := make([]storage.KnowledgeDraftEdge, len(m.GetEdges()))
	for i, e := range m.GetEdges() {
		edgeType, ok := toStorageEdgeType(e.GetEdgeType())
		if !ok {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("edge type must be specified"))
		}
		from, to := int(e.GetFromIndex()), int(e.GetToIndex())
		if from >= len(nodes) || to >= len(nodes) || from == to {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("edge references an invalid entry index"))
		}
		edges[i] = storage.KnowledgeDraftEdge{FromIndex: from, ToIndex: to, Type: edgeType}
	}

	c, err := s.active.resolve(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no active campaign"))
		}
		slog.Default().Error("ApplyGeneratedKnowledge: get active campaign failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	created, createdEdges, err := s.store.ApplyKnowledgeDraft(ctx, c.ID, nodes, edges)
	switch {
	case err == nil:
	case errors.Is(err, storage.ErrInvalidEdge):
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the draft contains an invalid relation — remove it and apply again"))
	case errors.Is(err, storage.ErrConflict):
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the draft contains a duplicate relation — remove it and apply again"))
	default:
		slog.Default().Error("ApplyGeneratedKnowledge: store apply failed", "campaign_id", c.ID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	out := make([]*managementv1.Node, len(created))
	for i, n := range created {
		out[i] = toProtoNode(n)
	}
	return connect.NewResponse(&managementv1.ApplyGeneratedKnowledgeResponse{
		Nodes:        out,
		EdgesCreated: uint32(len(createdEdges)),
	}), nil
}
