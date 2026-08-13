package rpc

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/search"
)

// Campaign search RPCs for the Ctrl+K palette (#591) on SessionService:
// SearchTranscripts (semantic Chunk ANN degrading to keyword Lines) and
// SearchHighlights (keyword over promoted Highlights). Node search stays the
// existing CampaignService.SearchNodes — the palette's third source needed no
// new RPC. Both resolve the Campaign server-side with the SAME searchCampaign
// policy SearchTranscriptLines pinned (live Voice Session first, else the
// profile-first durable selection) — never a client-supplied id.
//
// Access: the web tier has a single principal today — the operator
// (ADR-0039/0055); Linked Players never reach these RPCs until the ADR-0056
// policy seam lands. The handlers therefore pass search.AccessOperator, and the
// engine's player-tier exclusions are already enforced and tested underneath
// (internal/search), so wiring a player principal later is an argument change
// here, not a re-plumb.

// searchPaletteLimit caps each palette source's result set (#591). A fixed
// server policy like searchTranscriptLimit — the palette shows a handful per
// group, and the client sends no limit.
const searchPaletteLimit = 8

// SetSearch wires the campaign search engine (#591) onto the SessionServer.
// Called once at boot; the many NewSessionServer call sites keep their
// signature. Unwired, SearchTranscripts / SearchHighlights report
// CodeUnimplemented rather than panic.
func (s *SessionServer) SetSearch(engine *search.Engine) {
	s.search = engine
}

// searchEnabled is the CodeUnimplemented guard for a server built without the
// search seam (web-standalone tests, keyless boots) — the highlightsEnabled
// posture.
func (s *SessionServer) searchEnabled() error {
	if s.search == nil {
		return connect.NewError(connect.CodeUnimplemented, errors.New("campaign search is not enabled on this server"))
	}
	return nil
}

// SearchTranscripts is the palette's Campaign-wide transcript search (#591):
// semantic ANN over embedded Transcript Chunks when an embeddings provider is
// wired, degrading to the keyword Line search when not (semantic=false on the
// wire, so the palette says so honestly). An empty/whitespace query is
// CodeInvalidArgument; no Active Campaign yields an empty result (never-run
// state, the SearchTranscriptLines choice); a storage failure is CodeInternal.
func (s *SessionServer) SearchTranscripts(
	ctx context.Context,
	req *connect.Request[managementv1.SearchTranscriptsRequest],
) (*connect.Response[managementv1.SearchTranscriptsResponse], error) {
	if err := s.searchEnabled(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Msg.GetQuery()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("query must not be empty"))
	}

	campaignID, ok, err := s.searchCampaign(ctx)
	if err != nil {
		s.log.Error("SearchTranscripts: resolve active campaign failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	if !ok {
		return connect.NewResponse(&managementv1.SearchTranscriptsResponse{}), nil
	}

	res, err := s.search.SearchTranscripts(ctx, campaignID, search.AccessOperator, req.Msg.GetQuery(), searchPaletteLimit)
	if err != nil {
		s.log.Error("SearchTranscripts: engine search failed", "campaign_id", campaignID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	hits := make([]*managementv1.TranscriptSearchHit, 0, len(res.Hits))
	for _, h := range res.Hits {
		hits = append(hits, toProtoTranscriptSearchHit(h))
	}
	return connect.NewResponse(&managementv1.SearchTranscriptsResponse{
		Semantic: res.Semantic,
		Hits:     hits,
	}), nil
}

// SearchHighlights is the palette's promoted-Highlight search (#591). Tenant-
// AND Campaign-scoped server-side like every Highlight read; candidates never
// match (excluded in the SQL, ADR-0051). An empty/whitespace query is
// CodeInvalidArgument; no Active Campaign yields an empty result; a storage
// failure is CodeInternal.
func (s *SessionServer) SearchHighlights(
	ctx context.Context,
	req *connect.Request[managementv1.SearchHighlightsRequest],
) (*connect.Response[managementv1.SearchHighlightsResponse], error) {
	if err := s.searchEnabled(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Msg.GetQuery()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("query must not be empty"))
	}
	tenantID, err := s.tenant(ctx)
	if err != nil {
		return nil, err
	}

	campaignID, ok, rerr := s.searchCampaign(ctx)
	if rerr != nil {
		s.log.Error("SearchHighlights: resolve active campaign failed", "err", rerr)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	if !ok {
		return connect.NewResponse(&managementv1.SearchHighlightsResponse{}), nil
	}

	rows, serr := s.search.SearchHighlights(ctx, tenantID, campaignID, search.AccessOperator, req.Msg.GetQuery(), searchPaletteLimit)
	if serr != nil {
		s.log.Error("SearchHighlights: engine search failed", "campaign_id", campaignID, "err", serr)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	hits := make([]*managementv1.HighlightSearchHit, 0, len(rows))
	for _, h := range rows {
		hits = append(hits, &managementv1.HighlightSearchHit{
			Id:             h.ID.String(),
			VoiceSessionId: h.VoiceSessionID.String(),
			Excerpt:        h.Excerpt,
			Reason:         h.Reason,
			StartsAt:       timestamppb.New(h.StartsAt),
		})
	}
	return connect.NewResponse(&managementv1.SearchHighlightsResponse{Hits: hits}), nil
}

// toProtoTranscriptSearchHit maps an engine hit onto the wire: the
// {session, line} deep-link pair plus the rendered context. A uuid.Nil session
// (a legacy chunk persisted without one) maps to "" so the palette renders the
// hit without a link rather than a bogus id.
func toProtoTranscriptSearchHit(h search.TranscriptHit) *managementv1.TranscriptSearchHit {
	sessionID := ""
	if h.VoiceSessionID != uuid.Nil {
		sessionID = h.VoiceSessionID.String()
	}
	return &managementv1.TranscriptSearchHit{
		VoiceSessionId: sessionID,
		LineId:         h.LineID,
		Who:            h.Who,
		Kind:           h.Kind,
		At:             timestamppb.New(h.At),
		Snippet:        h.Snippet,
	}
}
