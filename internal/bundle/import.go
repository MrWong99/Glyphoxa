package bundle

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/blob"
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// ImportResult reports what an [Import] persisted. AgentIDs maps each bundle
// agent ref key to the minted (or, for the Butler, the trigger-created) Agent id;
// part 1 fills it and part 2 (#292) consumes it IN-FUNCTION to remap a Chunk's
// participated_agent_ids. Every int count — including DroppedParticipantRefs —
// feeds the ServeImport JSON response, always present so the response shape is
// stable for clients.
// DroppedParticipantRefs counts chunk participant refs that mapped to no imported
// Agent and were dropped (not fatal); a nonzero value also makes [Import] emit a
// single slog.Warn after the commit succeeds (#381), so a lossy import is never
// silent.
type ImportResult struct {
	CampaignID uuid.UUID
	Name       string
	// AgentIDs remaps bundle agent ref -> minted id (Butler ref -> the merged row).
	AgentIDs               map[string]uuid.UUID
	Agents                 int
	Nodes                  int
	Edges                  int
	Characters             int
	Aspects                int
	Tags                   int
	Maps                   int
	Pins                   int
	Boards                 int
	Sessions               int
	Lines                  int
	Chunks                 int
	Appearances            int
	DroppedParticipantRefs int
}

// Import ingests a [Bundle] into a fresh Campaign under tenantID, in ONE
// transaction (ADR-0049 synchronous; ADR-0053 §4/§5/§7): st is the seam whose
// [TxRunner.InTx] carries the all-or-nothing contract — production passes
// [PGStore], unit tests an in-memory fake (#451). It mints fresh UUIDs for
// every entity and remaps intra-bundle references, so the same bundle imported
// twice yields two independent Campaigns (ADR-0053 §4: idempotent re-import is a
// non-goal). Any unknown cross-reference (a node's agent link, an edge endpoint)
// is a hard error that rolls the whole import back — a bundle is all-or-nothing.
//
// The compatibility gate runs FIRST (ADR-0053 §7): a newer or unsupported
// format_version is refused with a message naming both versions, before any DB
// work. Provider bindings are never imported — the exporter stripped them
// (ADR-0053 §2), and every Agent lands with NULL provider FKs (the tenant-level
// fallback in wirenpc tolerates that, resolving providers per tenant). Voice is
// validated through the canonical [storage.VoiceFromJSON] before insert so a
// malformed voice fails loudly, never becomes a silent NPC (#224).
//
// Butler merge (ADR-0009): CreateCampaign fires the auto-Butler trigger, which
// inserts the campaign's Butler plus its default dice grant; a partial unique
// index forbids a second Butler. So when the bundle carries a Butler this UPDATEs
// the trigger-created row (name/title/persona/voice/aliases) and REPLACES its
// grants exactly — deleting every trigger grant (including dice) then creating one
// per bundle grant. With no Butler in the bundle the trigger defaults stand. The
// Butler's address_only stays pinned true by the storage layer (ADR-0024).
//
// History (Voice Sessions + Transcript Lines/Chunks) is imported in the SAME
// transaction (#292): each Voice Session's status is coerced terminal, its Lines
// keep their line_id/seq replay keys verbatim (ADR-0040), and its Chunks land
// with embedding NULL so the destination's embedworker regenerates them
// (ADR-0011) — a chunk's participated refs are remapped through AgentIDs and an
// unmappable one is dropped and counted. A bundle with no History section imports
// exactly as part 1 did (zero history counts).
func Import(ctx context.Context, st TxRunner, tenantID uuid.UUID, b *Bundle) (ImportResult, error) {
	if err := CheckVersion(b.FormatVersion); err != nil {
		return ImportResult{}, fmt.Errorf("bundle has format_version %d; this build supports %d: %w",
			b.FormatVersion, FormatVersion, err)
	}

	res := ImportResult{
		Name:     b.Campaign.Name,
		AgentIDs: make(map[string]uuid.UUID),
	}

	// Map image bytes ride the blob seam, which is OUTSIDE the import transaction —
	// blob.NewPostgres runs on its own pool, so a Put is not rolled back when the
	// transaction is. Every key this import writes is tracked, and a failure drops
	// them by hand. Without this a failed import would leave orphaned bytes with no
	// row that names them and no sweep that would ever find them.
	var written []string
	imgs, _ := st.(MapImageWriter)

	err := st.InTx(ctx, func(tx ImportStore) error {
		return importInTx(ctx, tx, tenantID, b, imgs, &written, &res)
	})
	if err != nil {
		for _, key := range written {
			if derr := imgs.DeleteMapImage(ctx, key); derr != nil {
				observe.CtxLogger(ctx).Warn("bundle: could not drop an orphaned map image after a failed import",
					"key", key, "err", derr)
			}
		}
		return ImportResult{}, err
	}
	// Surface a lossy import (a foreign/deleted participant carried no local NPC):
	// gated on count>0, one WARN on the request-scoped logger with campaign_id +
	// count (ADR-0032), so the count is visible in logs, not just the response JSON.
	// Emitted AFTER commit succeeds, so a rolled-back import never logs a
	// campaign_id that did not persist.
	if res.DroppedParticipantRefs > 0 {
		observe.CtxLogger(ctx).Warn("bundle: import dropped participant refs",
			"campaign_id", res.CampaignID, "count", res.DroppedParticipantRefs)
	}
	return res, nil
}

// importInTx runs the whole ingest against the tx-bound seam (see [Import]). It is
// split out so the transaction body reads top-to-bottom: campaign → Butler merge
// → character Agents → Nodes → node↔Agent links → Edges → Characters. Every step
// records or consumes the ref→id remaps in res.AgentIDs and a local node map.
func importInTx(
	ctx context.Context,
	tx ImportStore,
	tenantID uuid.UUID,
	b *Bundle,
	imgs MapImageWriter,
	written *[]string,
	res *ImportResult,
) error {
	campaignID, err := tx.CreateCampaign(ctx, storage.NewCampaign{
		TenantID: tenantID,
		Name:     b.Campaign.Name,
		System:   b.Campaign.System,
		Language: b.Campaign.Language,
	})
	if err != nil {
		return fmt.Errorf("bundle: import: create campaign: %w", err)
	}
	res.CampaignID = campaignID

	butlerSeen := false
	for i := range b.Campaign.Agents {
		a := &b.Campaign.Agents[i]
		if _, dup := res.AgentIDs[a.ID]; dup {
			return fmt.Errorf("bundle: import: duplicate agent ref %q", a.ID)
		}
		if a.Role == string(storage.AgentRoleButler) {
			if butlerSeen {
				// A Campaign has exactly one Butler (ADR-0009, types.go): a second in
				// the bundle would last-wins-overwrite the first and lie in the counts,
				// so it is a hard error that rolls the import back.
				return fmt.Errorf("bundle: import: more than one butler in bundle")
			}
			butlerSeen = true
			if err := mergeButler(ctx, tx, campaignID, a, res); err != nil {
				return err
			}
			continue
		}
		if err := createCharacterAgent(ctx, tx, campaignID, a, res); err != nil {
			return err
		}
	}

	nodeIDs := make(map[string]uuid.UUID, len(b.Campaign.Nodes))
	for i := range b.Campaign.Nodes {
		n := &b.Campaign.Nodes[i]
		if _, dup := nodeIDs[n.ID]; dup {
			// Two nodes sharing a ref key would clobber the remap, binding edges/links
			// to the wrong node — same all-or-nothing discipline as an unknown ref.
			return fmt.Errorf("bundle: import: duplicate node ref %q", n.ID)
		}
		created, err := tx.CreateNode(ctx, storage.NewKGNode{
			CampaignID: campaignID,
			Type:       storage.KGNodeType(n.Type),
			Name:       n.Name,
			Body:       n.Body,
			GMPrivate:  n.GMPrivate,
		})
		if err != nil {
			return fmt.Errorf("bundle: import: create node %q: %w", n.Name, err)
		}
		nodeIDs[n.ID] = created.ID
		res.Nodes++

		// Aspects and tags belong to the entry, not to history: an import that
		// restored an NPC's prose without its facts would look like a successful
		// restore of a hollowed-out campaign.
		if len(n.Aspects) > 0 {
			rows := make([]storage.NewKGNodeAspect, 0, len(n.Aspects))
			for _, a := range n.Aspects {
				rows = append(rows, storage.NewKGNodeAspect{Key: a.Key, Value: a.Value, GMPrivate: a.GMPrivate})
			}
			if err := tx.ReplaceNodeAspects(ctx, campaignID, created.ID,
				storage.KGNodeAspectWrite{Rows: rows}); err != nil {
				return fmt.Errorf("bundle: import: aspects for node %q: %w", n.Name, err)
			}
			res.Aspects += len(rows)
		}
		if len(n.Tags) > 0 {
			if err := tx.SetNodeTags(ctx, campaignID, created.ID, n.Tags); err != nil {
				return fmt.Errorf("bundle: import: tags for node %q: %w", n.Name, err)
			}
			res.Tags += len(n.Tags)
		}

		if n.AgentID != "" {
			agentID, ok := res.AgentIDs[n.AgentID]
			if !ok {
				return fmt.Errorf("bundle: import: node %q references unknown agent %q", n.Name, n.AgentID)
			}
			if _, err := tx.SetNodeAgent(ctx, campaignID, created.ID,
				uuid.NullUUID{UUID: agentID, Valid: true}); err != nil {
				return fmt.Errorf("bundle: import: link node %q to agent: %w", n.Name, err)
			}
		}
	}

	for i := range b.Campaign.Edges {
		e := &b.Campaign.Edges[i]
		from, ok := nodeIDs[e.From]
		if !ok {
			return fmt.Errorf("bundle: import: edge references unknown from-node %q", e.From)
		}
		to, ok := nodeIDs[e.To]
		if !ok {
			return fmt.Errorf("bundle: import: edge references unknown to-node %q", e.To)
		}
		created, err := tx.CreateEdge(ctx, storage.NewKGEdge{
			CampaignID: campaignID,
			FromNodeID: from,
			ToNodeID:   to,
			Type:       storage.KGEdgeType(e.Type),
		})
		if err != nil {
			return fmt.Errorf("bundle: import: create edge %s->%s: %w", e.From, e.To, err)
		}
		// The relation's texture (#546). CreateEdge takes only the type, so the note
		// and disposition land in a second write — a relation restored as bare
		// "knows" would reach every NPC prompt flatter than it left.
		if e.Note != "" || e.Disposition != 0 {
			if _, err := tx.UpdateEdgeDetails(ctx, campaignID, created.ID, e.Note, e.Disposition); err != nil {
				return fmt.Errorf("bundle: import: edge details %s->%s: %w", e.From, e.To, err)
			}
		}
		res.Edges++
	}

	for i := range b.Campaign.Characters {
		c := &b.Campaign.Characters[i]
		if _, err := tx.CreateCharacter(ctx, storage.NewCharacter{
			CampaignID:    campaignID,
			Name:          c.Name,
			Aliases:       c.Aliases,
			DiscordUserID: c.DiscordUserID,
		}); err != nil {
			return fmt.Errorf("bundle: import: create character %q: %w", c.Name, err)
		}
		res.Characters++
	}

	if _, err := importMaps(ctx, tx, campaignID, tenantID, b, nodeIDs, imgs, written, res); err != nil {
		return err
	}

	if err := importBoards(ctx, tx, campaignID, b, nodeIDs, res); err != nil {
		return err
	}

	if err := importHistory(ctx, tx, campaignID, b, nodeIDs, res); err != nil {
		return err
	}

	return nil
}

// importMaps recreates the campaign's Maps and their Pins, remapping every
// cross-reference onto the freshly minted ids.
//
// It is TWO passes over the maps, and that is mandatory rather than tidy.
// parent_map_id is self-referential with a composite FK, so a single pass in
// bundle order would be refused outright the first time a child sorted before its
// parent — and bundle order is the source's ListMaps order, which is alphabetical
// by name. "The Vault inside Saltmarsh" would import as two top-level maps, or
// not at all, depending on their names.
func importMaps(
	ctx context.Context,
	tx ImportStore,
	campaignID, tenantID uuid.UUID,
	b *Bundle,
	nodeIDs map[string]uuid.UUID,
	imgs MapImageWriter,
	written *[]string,
	res *ImportResult,
) (map[string]uuid.UUID, error) {
	if len(b.Campaign.Maps) == 0 {
		return nil, nil
	}
	mapIDs := make(map[string]uuid.UUID, len(b.Campaign.Maps))

	// Pass 1: every map, flat — no parent yet.
	for i := range b.Campaign.Maps {
		m := &b.Campaign.Maps[i]
		if _, dup := mapIDs[m.ID]; dup {
			return nil, fmt.Errorf("bundle: import: duplicate map ref %q", m.ID)
		}
		anchor := uuid.NullUUID{}
		if m.AnchorNodeID != "" {
			id, ok := nodeIDs[m.AnchorNodeID]
			if !ok {
				return nil, fmt.Errorf("bundle: import: map %q references unknown anchor entry %q", m.Name, m.AnchorNodeID)
			}
			anchor = uuid.NullUUID{UUID: id, Valid: true}
		}
		// A fresh blob key per imported map, minted the same way CreateMap's RPC
		// mints one. It is minted even when the bundle carries no bytes, so the row's
		// shape is identical either way and a later re-upload lands where the row
		// already points.
		key, err := blob.Key(tenantID, "map", uuid.New(), "image")
		if err != nil {
			return nil, fmt.Errorf("bundle: import: map %q blob key: %w", m.Name, err)
		}
		// Bytes BEFORE the row, the same ordering CreateMap's RPC uses: a row that
		// references missing bytes is a broken map forever, while bytes with no row
		// are invisible and get dropped by the rollback path above.
		if m.ImageBase64 != "" {
			if imgs == nil {
				return nil, fmt.Errorf("bundle: import: map %q carries an image but this deployment has no blob store", m.Name)
			}
			data, derr := base64.StdEncoding.DecodeString(m.ImageBase64)
			if derr != nil {
				return nil, fmt.Errorf("bundle: import: map %q image is not valid base64: %w", m.Name, derr)
			}
			contentType := m.ContentType
			if contentType == "" {
				contentType = "image/png"
			}
			if err := imgs.WriteMapImage(ctx, key, contentType, data); err != nil {
				return nil, fmt.Errorf("bundle: import: map %q image: %w", m.Name, err)
			}
			*written = append(*written, key)
		}

		created, err := tx.CreateMap(ctx, storage.NewCampaignMap{
			CampaignID:   campaignID,
			Name:         m.Name,
			BlobKey:      key,
			WidthPx:      m.WidthPx,
			HeightPx:     m.HeightPx,
			AnchorNodeID: anchor,
			GMPrivate:    m.GMPrivate,
		})
		if err != nil {
			return nil, fmt.Errorf("bundle: import: create map %q: %w", m.Name, err)
		}
		mapIDs[m.ID] = created.ID
		res.Maps++
	}

	// Pass 2: the nesting, now that every id exists.
	for i := range b.Campaign.Maps {
		m := &b.Campaign.Maps[i]
		if m.ParentMapID == "" {
			continue
		}
		parent, ok := mapIDs[m.ParentMapID]
		if !ok {
			return nil, fmt.Errorf("bundle: import: map %q references unknown parent %q", m.Name, m.ParentMapID)
		}
		anchor := uuid.NullUUID{}
		if m.AnchorNodeID != "" {
			anchor = uuid.NullUUID{UUID: nodeIDs[m.AnchorNodeID], Valid: true}
		}
		// UpdateMap replaces the whole editor field set, so the fields set in pass 1
		// are passed again rather than lost.
		if _, err := tx.UpdateMap(ctx, storage.CampaignMapUpdate{
			ID:           mapIDs[m.ID],
			CampaignID:   campaignID,
			Name:         m.Name,
			ParentMapID:  uuid.NullUUID{UUID: parent, Valid: true},
			AnchorNodeID: anchor,
			GMPrivate:    m.GMPrivate,
		}); err != nil {
			return nil, fmt.Errorf("bundle: import: nest map %q: %w", m.Name, err)
		}
	}

	// Pins last: they need both a map and a node.
	for i := range b.Campaign.Maps {
		m := &b.Campaign.Maps[i]
		for _, p := range m.Pins {
			nodeID, ok := nodeIDs[p.NodeID]
			if !ok {
				return nil, fmt.Errorf("bundle: import: pin on map %q references unknown entry %q", m.Name, p.NodeID)
			}
			if _, err := tx.CreatePin(ctx, storage.NewMapPin{
				MapID:         mapIDs[m.ID],
				CampaignID:    campaignID,
				NodeID:        nodeID,
				X:             p.X,
				Y:             p.Y,
				LabelOverride: p.LabelOverride,
				GMPrivate:     p.GMPrivate,
			}); err != nil {
				return nil, fmt.Errorf("bundle: import: pin on map %q: %w", m.Name, err)
			}
			res.Pins++
		}
	}
	return mapIDs, nil
}

// importBoards recreates the GM's session prep boards, remapping their entries.
func importBoards(
	ctx context.Context,
	tx ImportStore,
	campaignID uuid.UUID,
	b *Bundle,
	nodeIDs map[string]uuid.UUID,
	res *ImportResult,
) error {
	for i := range b.Campaign.Boards {
		bd := &b.Campaign.Boards[i]
		created, err := tx.CreateBoard(ctx, campaignID, bd.Name)
		if err != nil {
			return fmt.Errorf("bundle: import: create board %q: %w", bd.Name, err)
		}
		ids := make([]uuid.UUID, 0, len(bd.NodeIDs))
		for _, ref := range bd.NodeIDs {
			id, ok := nodeIDs[ref]
			if !ok {
				return fmt.Errorf("bundle: import: board %q references unknown entry %q", bd.Name, ref)
			}
			ids = append(ids, id)
		}
		// Rename-and-set in ONE call: the storage layer fused them deliberately, so
		// a failed entry write cannot leave a renamed board with the wrong contents.
		if err := tx.UpdateBoard(ctx, campaignID, created.ID, bd.Name, ids); err != nil {
			return fmt.Errorf("bundle: import: board %q entries: %w", bd.Name, err)
		}
		res.Boards++
	}
	return nil
}

// importHistory ingests the flag-gated transcript payload (#292, ADR-0053 §1/§3)
// in the SAME transaction as the domain grains (ADR-0049): per Voice Session an
// [storage.ImportVoiceSession] (status coerced terminal, ended_at defaulted to
// started_at), then each Transcript Line UPSERTed with its line_id/seq VERBATIM
// (ADR-0040 replay keys — never re-derived), then each Transcript Chunk INSERTed
// with embedding NULL by construction so the destination's embedworker
// regenerates it (ADR-0011 — this NEVER writes a vector or model). A chunk's
// participated_agent_ids are remapped through res.AgentIDs; an unmappable ref is
// DROPPED and counted in DroppedParticipantRefs (not fatal — a foreign agent
// simply carries no local knowledge), while speaker snowflakes travel verbatim
// (ADR-0053 §6). A nil History section is a no-op: counts stay zero (part-1 parity).
func importHistory(ctx context.Context, tx ImportStore, campaignID uuid.UUID, b *Bundle, nodeIDs map[string]uuid.UUID, res *ImportResult) error {
	if b.Campaign.History == nil {
		return nil
	}
	for i := range b.Campaign.History.Sessions {
		s := &b.Campaign.History.Sessions[i]
		endedAt := s.EndedAt
		if endedAt == nil {
			// A terminal session always has an ended_at; a bundle missing one (a
			// coerced non-terminal row) defaults it to started_at so no imported row
			// looks live to GetLatestVoiceSession or the reconciler.
			started := s.StartedAt
			endedAt = &started
		}
		sessionID, err := tx.ImportVoiceSession(ctx, storage.VoiceSession{
			CampaignID: campaignID,
			StartedAt:  s.StartedAt,
			EndedAt:    endedAt,
			Status:     coerceTerminalStatus(s.Status),
			LineCount:  s.LineCount,
			EndReason:  s.EndReason,
		})
		if err != nil {
			return fmt.Errorf("bundle: import: session %q: %w", s.ID, err)
		}
		res.Sessions++

		for j := range s.Lines {
			l := &s.Lines[j]
			// Edge (documented, no behavior change): two bundle Lines sharing a line_id
			// COALESCE at the DB upsert (ADR-0040 replay key = (voice_session_id,
			// line_id)) — the last write wins on text/who while an earlier seq can
			// linger, and res.Lines still counts BOTH inputs, so the reported Lines can
			// exceed the persisted row count. A well-formed exported bundle never
			// carries dup line_ids; this only bites a hand-edited/foreign bundle, and
			// the import stays correct-by-replay. Left as-is per #381 review.
			if err := tx.UpsertTranscriptLine(ctx, storage.TranscriptLine{
				VoiceSessionID:       sessionID,
				CampaignID:           campaignID,
				LineID:               l.LineID,
				Seq:                  l.Seq,
				Who:                  l.Who,
				Tag:                  l.Tag,
				Kind:                 l.Kind,
				TS:                   l.TS,
				Text:                 l.Text,
				SpeakerDiscordUserID: l.SpeakerDiscordUserID,
			}); err != nil {
				return fmt.Errorf("bundle: import: session %q line %q: %w", s.ID, l.LineID, err)
			}
			res.Lines++
		}

		// Appearances AFTER the lines, because the composite FK points at them.
		// A row whose Node ref is unknown is DROPPED rather than fatal: an
		// appearance is a derived convenience, and refusing an entire restore
		// because one mention pointed at a since-deleted entry would be the wrong
		// trade for something the destination can regenerate by re-indexing.
		if len(s.Appearances) > 0 {
			rows := make([]storage.NodeAppearance, 0, len(s.Appearances))
			for _, a := range s.Appearances {
				nodeID, ok := nodeIDs[a.NodeID]
				if !ok {
					continue
				}
				rows = append(rows, storage.NodeAppearance{
					NodeID:         nodeID,
					CampaignID:     campaignID,
					VoiceSessionID: sessionID,
					LineID:         a.LineID,
					At:             a.At,
				})
			}
			if len(rows) > 0 {
				if err := tx.RecordNodeAppearances(ctx, rows); err != nil {
					return fmt.Errorf("bundle: import: session %q appearances: %w", s.ID, err)
				}
				res.Appearances += len(rows)
			}
		}

		for j := range s.Chunks {
			c := &s.Chunks[j]
			participated := make([]uuid.UUID, 0, len(c.ParticipatedAgentIDs))
			for _, ref := range c.ParticipatedAgentIDs {
				agentID, ok := res.AgentIDs[ref]
				if !ok {
					// A participant that maps to no imported Agent (e.g. an Agent deleted
					// before export, or a cross-campaign artifact) is dropped, not fatal —
					// it just means no local NPC claims that chunk's knowledge.
					res.DroppedParticipantRefs++
					continue
				}
				participated = append(participated, agentID)
			}
			// embedding stays NULL and embedding_model '' by construction (ADR-0011):
			// InsertTranscriptChunk never writes a vector, so the destination
			// embedworker regenerates them. The backlog gauge is not refreshed here —
			// the worker polls the DB on its own schedule.
			if _, err := tx.InsertTranscriptChunk(ctx, storage.TranscriptChunk{
				CampaignID:            campaignID,
				VoiceSessionID:        sessionID,
				Content:               c.Content,
				SpeakerDiscordUserIDs: c.SpeakerDiscordUserIDs,
				ParticipatedAgentIDs:  participated,
				StartedAt:             c.StartedAt,
			}); err != nil {
				return fmt.Errorf("bundle: import: session %q chunk: %w", s.ID, err)
			}
			res.Chunks++
		}
	}
	return nil
}

// coerceTerminalStatus maps an imported Voice Session status to a terminal one: a
// 'failed' row keeps its status (a distinct terminal state, ADR-0053 §6 verbatim
// spirit), anything else — including a stale 'running' from a crashed source —
// becomes 'ended', so no imported row is ever revivable or owned by a live loop.
func coerceTerminalStatus(status string) storage.VoiceSessionStatus {
	if storage.VoiceSessionStatus(status) == storage.VoiceSessionFailed {
		return storage.VoiceSessionFailed
	}
	return storage.VoiceSessionEnded
}

// mergeButler UPDATEs the trigger-created Butler from the bundle's Butler and
// replaces its Tool Grants exactly (ADR-0009 §5): the trigger already inserted the
// Butler + its default dice grant, so a second insert is impossible. Provider FKs
// are nulled (secrets never travel in a bundle, ADR-0053 §2); address_only is left
// to the storage layer, which force-keeps a Butler's true (ADR-0024). The Butler
// ref is recorded in AgentIDs so a node could link to it if the bundle so wires.
func mergeButler(ctx context.Context, tx ImportStore, campaignID uuid.UUID, a *Agent, res *ImportResult) error {
	butler, err := tx.GetButler(ctx, campaignID)
	if err != nil {
		return fmt.Errorf("bundle: import: get trigger-created butler: %w", err)
	}
	if _, err := storage.VoiceFromJSON(a.Voice); err != nil {
		return fmt.Errorf("bundle: import: butler voice: %w", err)
	}
	if _, err := tx.UpdateAgent(ctx, storage.AgentUpdate{
		ID:                    butler.ID,
		CampaignID:            campaignID,
		Name:                  a.Name,
		Title:                 a.Title,
		Persona:               a.Persona,
		Voice:                 a.Voice,
		VoiceProviderConfigID: uuid.NullUUID{},
		LLMProviderConfigID:   uuid.NullUUID{},
		AddressOnly:           a.AddressOnly,
		Aliases:               a.Aliases,
	}); err != nil {
		return fmt.Errorf("bundle: import: merge butler: %w", err)
	}

	// Replace grants exactly: delete every existing grant (incl. the trigger's
	// dice) then create one per bundle grant. This is how a Butler that was
	// re-scoped in the source (dice removed, another tool added) round-trips
	// without the trigger default lingering or duplicating.
	existing, err := tx.ListToolGrants(ctx, butler.ID)
	if err != nil {
		return fmt.Errorf("bundle: import: list butler grants: %w", err)
	}
	for _, g := range existing {
		if err := tx.DeleteToolGrant(ctx, butler.ID, g.ToolName); err != nil {
			return fmt.Errorf("bundle: import: delete butler grant %q: %w", g.ToolName, err)
		}
	}
	if err := createGrants(ctx, tx, butler.ID, a.Grants); err != nil {
		return err
	}

	res.AgentIDs[a.ID] = butler.ID
	res.Agents++
	return nil
}

// createCharacterAgent inserts one Character NPC Agent with NULL provider FKs and
// its Tool Grants, recording the ref→id remap. Speaker-colour slot assignment is
// server-side and depends on bundle order (deterministic).
func createCharacterAgent(ctx context.Context, tx ImportStore, campaignID uuid.UUID, a *Agent, res *ImportResult) error {
	if _, err := storage.VoiceFromJSON(a.Voice); err != nil {
		return fmt.Errorf("bundle: import: agent %q voice: %w", a.Name, err)
	}
	agentID, err := tx.CreateAgent(ctx, storage.NewAgent{
		CampaignID:            campaignID,
		Role:                  storage.AgentRoleCharacter,
		Name:                  a.Name,
		Title:                 a.Title,
		Persona:               a.Persona,
		Voice:                 a.Voice,
		VoiceProviderConfigID: uuid.NullUUID{},
		LLMProviderConfigID:   uuid.NullUUID{},
		AddressOnly:           a.AddressOnly,
		Aliases:               a.Aliases,
	})
	if err != nil {
		return fmt.Errorf("bundle: import: create agent %q: %w", a.Name, err)
	}
	if err := createGrants(ctx, tx, agentID, a.Grants); err != nil {
		return err
	}
	res.AgentIDs[a.ID] = agentID
	res.Agents++
	return nil
}

// createGrants creates one Tool Grant per bundle grant for an Agent, carrying the
// scope Config verbatim (nil when the grant narrows nothing, e.g. dice).
func createGrants(ctx context.Context, tx ImportStore, agentID uuid.UUID, grants []Grant) error {
	for _, g := range grants {
		if _, err := tx.CreateToolGrant(ctx, storage.NewToolGrant{
			AgentID:  agentID,
			ToolName: g.ToolName,
			Config:   g.Config,
		}); err != nil {
			return fmt.Errorf("bundle: import: create grant %q: %w", g.ToolName, err)
		}
	}
	return nil
}
