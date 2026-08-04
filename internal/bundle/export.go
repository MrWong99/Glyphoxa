package bundle

import (
	"context"
	"encoding/base64"
	"errors"
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
	// IncludeImages embeds map image bytes as base64 (#547). A flag, not a
	// default, for the same reason History is: the blob cap is 32 MiB PER IMAGE
	// and base64 inflates by a third, so a campaign with a handful of scans would
	// turn a setup export into something nobody can mail. Without it a Map still
	// round-trips — its name, nesting, anchor, privacy and every Pin — and the GM
	// re-uploads the picture.
	IncludeImages bool
}

// Export serializes one Campaign into a [Bundle] (ADR-0053). It reads EXCLUSIVELY
// through the [ExportStore] seam — GetCampaign, ListAgents, per-agent
// ListToolGrants, ListNodes, ListEdges, ListCharacters, and (only when
// opts.IncludeHistory) ListVoiceSessions + ListTranscriptLines +
// ListTranscriptChunks. It NEVER touches provider_config, deployment_config,
// users, or auth sessions: the secrets-exclusion property (ADR-0053 §2) holds by
// construction because the bundle types carry no field for a secret and the
// seam carries no method that reads a table storing one.
//
// Every entity reference in the produced bundle is the SOURCE row's UUID string
// (ADR-0053 §4): agent ids, node ids, edge endpoints, and history session/agent
// refs are all uuid.UUID.String(). The importer mints fresh ids and remaps these.
//
// Agent voice round-trips storage.VoiceFromJSON -> VoiceToJSON so the exported
// voice carries the canonical shape minus provider bindings (the FK
// voice_provider_config_id is an Agent column this never reads). A voice column
// that does not parse is a hard error, never a silently dropped voice (#224).
func Export(ctx context.Context, st ExportStore, campaignID uuid.UUID, opts ExportOptions) (*Bundle, error) {
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
	// Tags come from ONE campaign-wide read rather than a per-node one: the
	// campaign has a handful of tags and hundreds of entries, and N reads for a
	// join that is already indexed would be the N+1 for no benefit.
	tagged, err := st.CampaignTags(ctx, campaignID)
	if err != nil {
		return nil, fmt.Errorf("bundle: export: campaign tags: %w", err)
	}
	tagsByNode := map[uuid.UUID][]string{}
	for _, t := range tagged {
		tagsByNode[t.NodeID] = append(tagsByNode[t.NodeID], t.Tag)
	}

	bNodes := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		bn := Node{
			ID:        n.ID.String(),
			Type:      string(n.Type),
			Name:      n.Name,
			Body:      n.Body,
			GMPrivate: n.GMPrivate,
			Tags:      tagsByNode[n.ID],
		}
		if n.AgentID.Valid {
			bn.AgentID = n.AgentID.UUID.String()
		}
		// ListNodes projects Aspects GM-facing, private ones INCLUDED — which is
		// what a backup must carry. An export that dropped the private ones would
		// restore a campaign missing exactly its secrets.
		for _, a := range n.Aspects {
			bn.Aspects = append(bn.Aspects, Aspect{Key: a.Key, Value: a.Value, GMPrivate: a.GMPrivate})
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
			From:        e.FromNodeID.String(),
			To:          e.ToNodeID.String(),
			Type:        string(e.Type),
			Note:        e.Note,
			Disposition: e.Disposition,
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

	bMaps, err := exportMaps(ctx, st, campaignID, opts.IncludeImages)
	if err != nil {
		return nil, err
	}
	bBoards, err := exportBoards(ctx, st, campaignID)
	if err != nil {
		return nil, err
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
			Maps:       bMaps,
			Boards:     bBoards,
			History:    history,
		},
	}, nil
}

// exportGrants reads one Agent's persisted Tool Grants and maps them to bundle
// grains. Config is carried verbatim (nil when the grant narrows nothing).
func exportGrants(ctx context.Context, st ExportStore, agentID uuid.UUID) ([]Grant, error) {
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
func exportHistory(ctx context.Context, st ExportStore, campaignID uuid.UUID) (*History, error) {
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
		apps, err := st.ListSessionAppearances(ctx, s.ID)
		if err != nil {
			return nil, fmt.Errorf("bundle: export: session %s appearances: %w", s.ID, err)
		}
		var bAppearances []Appearance
		for _, a := range apps {
			bAppearances = append(bAppearances, Appearance{
				NodeID: a.NodeID.String(), LineID: a.LineID, At: a.At,
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
			// Appearances ride the history flag: they are a record of what was SAID,
			// not part of the campaign's setup (#545, #547).
			Appearances: bAppearances,
		})
	}

	return &History{Sessions: bSessions}, nil
}

// exportMaps serializes the campaign's Maps with their Pins nested, optionally
// embedding the image bytes.
//
// The per-map Pin read is an N+1 by construction and that is deliberate: a
// campaign has a handful of Maps, the alternative is a campaign-wide pin read
// plus a client-side group-by, and this way the nesting and the ordering come
// straight from the store's own stable orders.
func exportMaps(ctx context.Context, st ExportStore, campaignID uuid.UUID, withImages bool) ([]Map, error) {
	maps, err := st.ListMaps(ctx, campaignID)
	if err != nil {
		return nil, fmt.Errorf("bundle: export: list maps: %w", err)
	}
	if len(maps) == 0 {
		return nil, nil
	}
	out := make([]Map, 0, len(maps))
	for _, m := range maps {
		bm := Map{
			ID:        m.ID.String(),
			Name:      m.Name,
			WidthPx:   m.WidthPx,
			HeightPx:  m.HeightPx,
			GMPrivate: m.GMPrivate,
		}
		if m.ParentMapID.Valid {
			bm.ParentMapID = m.ParentMapID.UUID.String()
		}
		if m.AnchorNodeID.Valid {
			bm.AnchorNodeID = m.AnchorNodeID.UUID.String()
		}
		if withImages {
			data, contentType, err := st.ReadMapImage(ctx, m.BlobKey)
			switch {
			case errors.Is(err, storage.ErrNotFound):
				// A row whose bytes are gone still exports as a Map. Refusing the whole
				// backup because one image went missing would be the worst possible
				// trade: the metadata and every Pin are still recoverable.
			case err != nil:
				return nil, fmt.Errorf("bundle: export: map image %s: %w", m.ID, err)
			default:
				bm.ContentType = contentType
				bm.ImageBase64 = base64.StdEncoding.EncodeToString(data)
			}
		}
		pins, err := st.ListPins(ctx, campaignID, m.ID)
		if err != nil {
			return nil, fmt.Errorf("bundle: export: list pins for map %s: %w", m.ID, err)
		}
		for _, p := range pins {
			bm.Pins = append(bm.Pins, Pin{
				NodeID:        p.NodeID.String(),
				X:             p.X,
				Y:             p.Y,
				LabelOverride: p.LabelOverride,
				GMPrivate:     p.GMPrivate,
			})
		}
		out = append(out, bm)
	}
	return out, nil
}

// exportBoards serializes the GM's session prep boards (#543).
func exportBoards(ctx context.Context, st ExportStore, campaignID uuid.UUID) ([]Board, error) {
	boards, err := st.ListBoards(ctx, campaignID)
	if err != nil {
		return nil, fmt.Errorf("bundle: export: list boards: %w", err)
	}
	if len(boards) == 0 {
		return nil, nil
	}
	out := make([]Board, 0, len(boards))
	for _, b := range boards {
		bb := Board{Name: b.Name}
		for _, id := range b.NodeIDs {
			bb.NodeIDs = append(bb.NodeIDs, id.String())
		}
		out = append(out, bb)
	}
	return out, nil
}
