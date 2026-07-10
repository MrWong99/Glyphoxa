package bundle

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// ImportResult reports what an [Import] persisted. AgentIDs maps each bundle
// agent ref key to the minted (or, for the Butler, the trigger-created) Agent id;
// part 1 fills it and part 2 (#292) consumes it IN-FUNCTION to remap a Chunk's
// participated_agent_ids. The int counts feed the ServeImport JSON response.
// DroppedParticipantRefs counts unmappable chunk participant refs (part 2 only);
// it is always zero here.
type ImportResult struct {
	CampaignID uuid.UUID
	Name       string
	// AgentIDs remaps bundle agent ref -> minted id (Butler ref -> the merged row).
	AgentIDs               map[string]uuid.UUID
	Agents                 int
	Nodes                  int
	Edges                  int
	Characters             int
	Sessions               int
	Lines                  int
	Chunks                 int
	DroppedParticipantRefs int
}

// Import ingests a [Bundle] into a fresh Campaign under tenantID, in ONE
// transaction (ADR-0049 synchronous; ADR-0053 §4/§5/§7). It mints fresh UUIDs for
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
// History (Voice Sessions + Transcript Lines/Chunks) is tolerated but UNTOUCHED
// in part 1: a bundle carrying a History section imports its domain grains fine
// and leaves Sessions/Lines/Chunks at zero. Part 2 (#292) fills them in the same
// transaction.
func Import(ctx context.Context, st *storage.Store, tenantID uuid.UUID, b *Bundle) (ImportResult, error) {
	if err := CheckVersion(b.FormatVersion); err != nil {
		return ImportResult{}, fmt.Errorf("bundle has format_version %d; this build supports %d: %w",
			b.FormatVersion, FormatVersion, err)
	}

	res := ImportResult{
		Name:     b.Campaign.Name,
		AgentIDs: make(map[string]uuid.UUID),
	}

	err := st.InTx(ctx, func(tx *storage.Store) error {
		return importInTx(ctx, tx, tenantID, b, &res)
	})
	if err != nil {
		return ImportResult{}, err
	}
	return res, nil
}

// importInTx runs the whole ingest against a tx-bound Store (see [Import]). It is
// split out so the transaction body reads top-to-bottom: campaign → Butler merge
// → character Agents → Nodes → node↔Agent links → Edges → Characters. Every step
// records or consumes the ref→id remaps in res.AgentIDs and a local node map.
func importInTx(ctx context.Context, tx *storage.Store, tenantID uuid.UUID, b *Bundle, res *ImportResult) error {
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
		if _, err := tx.CreateEdge(ctx, storage.NewKGEdge{
			CampaignID: campaignID,
			FromNodeID: from,
			ToNodeID:   to,
			Type:       storage.KGEdgeType(e.Type),
		}); err != nil {
			return fmt.Errorf("bundle: import: create edge %s->%s: %w", e.From, e.To, err)
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

	// History is part 2 (#292): its presence is tolerated, its rows are left for
	// the next slice. Counts stay zero here.

	return nil
}

// mergeButler UPDATEs the trigger-created Butler from the bundle's Butler and
// replaces its Tool Grants exactly (ADR-0009 §5): the trigger already inserted the
// Butler + its default dice grant, so a second insert is impossible. Provider FKs
// are nulled (secrets never travel in a bundle, ADR-0053 §2); address_only is left
// to the storage layer, which force-keeps a Butler's true (ADR-0024). The Butler
// ref is recorded in AgentIDs so a node could link to it if the bundle so wires.
func mergeButler(ctx context.Context, tx *storage.Store, campaignID uuid.UUID, a *Agent, res *ImportResult) error {
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
func createCharacterAgent(ctx context.Context, tx *storage.Store, campaignID uuid.UUID, a *Agent, res *ImportResult) error {
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
func createGrants(ctx context.Context, tx *storage.Store, agentID uuid.UUID, grants []Grant) error {
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
