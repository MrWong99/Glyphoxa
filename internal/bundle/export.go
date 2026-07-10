package bundle

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// ExportOptions controls what an [Export] writes. History (Voice Sessions +
// their Transcript Lines/Chunks) is flag-gated and default off (ADR-0053 §1):
// the default export shares/provisions a campaign SETUP; IncludeHistory serves
// the backup/migration path.
type ExportOptions struct {
	IncludeHistory bool
}

// Export serializes one Campaign into a [Bundle] (ADR-0053). It reads EXCLUSIVELY
// through the storage layer's allowlisted read paths — GetCampaign, ListAgents,
// per-agent ListToolGrants, ListNodes, ListEdges, ListCharacters, and (only when
// opts.IncludeHistory) ListVoiceSessions + ListTranscriptLines +
// ListTranscriptChunks. It NEVER touches provider_config, deployment_config,
// users, or auth sessions: the secrets-exclusion property (ADR-0053 §2) holds by
// construction because the bundle types carry no field for a secret and this
// function reads no table that stores one.
//
// Every entity reference in the produced bundle is the SOURCE row's UUID string
// (ADR-0053 §4): agent ids, node ids, edge endpoints, and history session/agent
// refs are all uuid.UUID.String(). The importer mints fresh ids and remaps these.
//
// Agent voice round-trips storage.VoiceFromJSON -> VoiceToJSON so the exported
// voice carries the canonical shape minus provider bindings (the FK
// voice_provider_config_id is an Agent column this never reads). A voice column
// that does not parse is a hard error, never a silently dropped voice (#224).
func Export(ctx context.Context, st *storage.Store, campaignID uuid.UUID, opts ExportOptions) (*Bundle, error) {
	campaign, err := st.GetCampaign(ctx, campaignID)
	if err != nil {
		return nil, fmt.Errorf("bundle: export: get campaign %s: %w", campaignID, err)
	}

	agents, err := st.ListAgents(ctx, campaignID)
	if err != nil {
		return nil, fmt.Errorf("bundle: export: list agents: %w", err)
	}
	bAgents := make([]Agent, 0, len(agents))
	for _, a := range agents {
		voice, err := exportVoice(a.Voice)
		if err != nil {
			return nil, fmt.Errorf("bundle: export: agent %s voice: %w", a.ID, err)
		}
		grants, err := exportGrants(ctx, st, a.ID)
		if err != nil {
			return nil, err
		}
		bAgents = append(bAgents, Agent{
			ID:          a.ID.String(),
			Role:        string(a.Role),
			Name:        a.Name,
			Title:       a.Title,
			Persona:     a.Persona,
			Voice:       voice,
			AddressOnly: a.AddressOnly,
			Aliases:     a.Aliases,
			Grants:      grants,
		})
	}

	nodes, err := st.ListNodes(ctx, campaignID)
	if err != nil {
		return nil, fmt.Errorf("bundle: export: list nodes: %w", err)
	}
	bNodes := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		bn := Node{
			ID:        n.ID.String(),
			Type:      string(n.Type),
			Name:      n.Name,
			Body:      n.Body,
			GMPrivate: n.GMPrivate,
		}
		if n.AgentID.Valid {
			bn.AgentID = n.AgentID.UUID.String()
		}
		bNodes = append(bNodes, bn)
	}

	edges, err := st.ListEdges(ctx, campaignID)
	if err != nil {
		return nil, fmt.Errorf("bundle: export: list edges: %w", err)
	}
	bEdges := make([]Edge, 0, len(edges))
	for _, e := range edges {
		bEdges = append(bEdges, Edge{
			From: e.FromNodeID.String(),
			To:   e.ToNodeID.String(),
			Type: string(e.Type),
		})
	}

	chars, err := st.ListCharacters(ctx, campaignID)
	if err != nil {
		return nil, fmt.Errorf("bundle: export: list characters: %w", err)
	}
	bChars := make([]Character, 0, len(chars))
	for _, c := range chars {
		bChars = append(bChars, Character{
			Name:          c.Name,
			Aliases:       c.Aliases,
			DiscordUserID: c.DiscordUserID,
		})
	}

	var history *History
	if opts.IncludeHistory {
		history, err = exportHistory(ctx, st, campaignID)
		if err != nil {
			return nil, err
		}
	}

	return &Bundle{
		FormatVersion: FormatVersion,
		ExportedAt:    time.Now().UTC(),
		Campaign: Campaign{
			Name:       campaign.Name,
			System:     campaign.System,
			Language:   campaign.Language,
			Agents:     bAgents,
			Nodes:      bNodes,
			Edges:      bEdges,
			Characters: bChars,
			History:    history,
		},
	}, nil
}

// exportGrants reads one Agent's persisted Tool Grants and maps them to bundle
// grains. Config is carried verbatim (nil when the grant narrows nothing).
func exportGrants(ctx context.Context, st *storage.Store, agentID uuid.UUID) ([]Grant, error) {
	rows, err := st.ListToolGrants(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("bundle: export: list grants for agent %s: %w", agentID, err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	grants := make([]Grant, 0, len(rows))
	for _, g := range rows {
		grants = append(grants, Grant{ToolName: g.ToolName, Config: g.Config})
	}
	return grants, nil
}

// exportVoice round-trips a persisted voice JSONB through the canonical mapper so
// the bundle carries the normalized shape and never provider bindings. A voice
// that decodes to the zero Voice — an empty column or the '{}' schema/trigger
// default — is exported as absent (nil) rather than a noisy all-empty object, so
// a voiceless Butler round-trips clean. An unparsable column is an error (#224).
func exportVoice(raw []byte) ([]byte, error) {
	v, err := storage.VoiceFromJSON(raw)
	if err != nil {
		return nil, err
	}
	if v.ProviderID == "" && v.VoiceID == "" && v.Name == "" && v.Language == "" &&
		len(v.Settings) == 0 && len(v.Metadata) == 0 {
		return nil, nil
	}
	out, err := storage.VoiceToJSON(v)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// exportHistory reads the flag-gated transcript payload (ADR-0053 §1): every
// Voice Session of the Campaign with its Transcript Lines (seq order) and its
// Transcript Chunks grouped by Voice Session. Embeddings are never read
// (ListTranscriptChunks is called with includeVectors=false, ADR-0053 §3) — the
// destination re-embeds. ListVoiceSessions has no all-rows overload, so the cap
// is math.MaxInt32: a backup pulls every session, and no campaign approaches it.
func exportHistory(ctx context.Context, st *storage.Store, campaignID uuid.UUID) (*History, error) {
	sessions, err := st.ListVoiceSessions(ctx, campaignID, math.MaxInt32)
	if err != nil {
		return nil, fmt.Errorf("bundle: export: list voice sessions: %w", err)
	}

	chunks, err := st.ListTranscriptChunks(ctx, campaignID, false)
	if err != nil {
		return nil, fmt.Errorf("bundle: export: list transcript chunks: %w", err)
	}
	chunksBySession := make(map[uuid.UUID][]Chunk, len(sessions))
	for _, c := range chunks {
		// voice_session_id is a nullable column (ADR-0011 SEAM): a chunk not bound to
		// a Voice Session (uuid.Nil) has no Session to nest under, so it is skipped
		// from the history payload rather than orphaned under a nil map key. Every
		// chunk the live pipeline writes carries a session, so this is a defensive
		// guard, not the normal path.
		if c.VoiceSessionID == uuid.Nil {
			continue
		}
		participated := make([]string, 0, len(c.ParticipatedAgentIDs))
		for _, id := range c.ParticipatedAgentIDs {
			participated = append(participated, id.String())
		}
		chunksBySession[c.VoiceSessionID] = append(chunksBySession[c.VoiceSessionID], Chunk{
			Content:               c.Content,
			SpeakerDiscordUserIDs: c.SpeakerDiscordUserIDs,
			ParticipatedAgentIDs:  participated,
			StartedAt:             c.StartedAt,
		})
	}

	bSessions := make([]Session, 0, len(sessions))
	for _, s := range sessions {
		lines, err := st.ListTranscriptLines(ctx, s.ID)
		if err != nil {
			return nil, fmt.Errorf("bundle: export: list transcript lines for session %s: %w", s.ID, err)
		}
		bLines := make([]Line, 0, len(lines))
		for _, l := range lines {
			bLines = append(bLines, Line{
				LineID:               l.LineID,
				Seq:                  l.Seq,
				Who:                  l.Who,
				Tag:                  l.Tag,
				Kind:                 l.Kind,
				TS:                   l.TS,
				Text:                 l.Text,
				SpeakerDiscordUserID: l.SpeakerDiscordUserID,
			})
		}
		bSessions = append(bSessions, Session{
			ID:        s.ID.String(),
			StartedAt: s.StartedAt,
			EndedAt:   s.EndedAt,
			Status:    string(s.Status),
			LineCount: s.LineCount,
			EndReason: s.EndReason,
			Lines:     bLines,
			Chunks:    chunksBySession[s.ID],
		})
	}

	return &History{Sessions: bSessions}, nil
}
