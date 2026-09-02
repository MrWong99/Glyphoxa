// Package appearance indexes where each Knowledge Graph Node was mentioned in
// play (#545, ADR-0008 amendment).
//
// "When did we last see that ogre" is the question a GM asks constantly and the
// wiki cannot answer: a Node records what a thing IS, never where it came up.
//
// The tempting modelling answer — an Edge from the Node to the session — is wrong
// twice. An Edge is world truth that AgentNodeFacts walks into an NPC's Hot
// Context, so mentions would silently become facts the NPC recites back; and the
// edge-type vocabulary is closed precisely so that cannot happen by accident. So
// this is a RETRIEVAL index that sits beside the graph and is never read by
// prompt assembly.
//
// It runs as a durable job at session end, which is ADR-0049's rule applied: the
// work has a real durability need (the transcript is finished and nothing will
// re-trigger it), it is measured in whole sessions rather than turns, and it must
// not touch the voice turn's critical path.
package appearance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/address"
)

// JobKindIndexSession is the background-job kind that indexes one ended Voice
// Session's Transcript Lines against the Campaign's Knowledge Graph.
const JobKindIndexSession = "appearance.index_session"

// Payload is the job's payload: which session to index. The campaign is resolved
// from the session rather than carried, so a stale payload cannot index one
// campaign's lines against another's entries.
type Payload struct {
	VoiceSessionID uuid.UUID `json:"voice_session_id"`
}

// Marshal builds the job payload.
func Marshal(sessionID uuid.UUID) ([]byte, error) {
	return json.Marshal(Payload{VoiceSessionID: sessionID})
}

// Store is the narrow storage surface the indexer needs; *storage.Store
// satisfies it. Note what is absent: nothing that writes kg_node or kg_edge. The
// indexer is structurally incapable of changing the graph.
type Store interface {
	GetVoiceSession(ctx context.Context, id uuid.UUID) (storage.VoiceSession, error)
	GetCampaign(ctx context.Context, id uuid.UUID) (storage.Campaign, error)
	CampaignNodeNames(ctx context.Context, campaignID uuid.UUID) ([]storage.IndexableNode, error)
	SessionLinesForIndexing(ctx context.Context, sessionID uuid.UUID) ([]storage.IndexableLine, error)
	RecordNodeAppearances(ctx context.Context, rows []storage.NodeAppearance) error
}

// IndexHandler builds the job handler.
//
// It is idempotent: the write is ON CONFLICT DO NOTHING over (node, session,
// line), so a retry, a replay, or a manual re-run re-derives the same pairs and
// writes nothing new. Nobody has to reason about whether it already ran.
func IndexHandler(store Store, encoders *address.EncoderRegistry, log *slog.Logger) func(context.Context, json.RawMessage) error {
	if log == nil {
		log = slog.Default()
	}
	if encoders == nil {
		encoders = address.DefaultEncoders()
	}
	return func(ctx context.Context, raw json.RawMessage) error {
		var p Payload
		if err := json.Unmarshal(raw, &p); err != nil {
			// A payload that cannot be parsed will never parse. Returning nil
			// dead-letters it quietly instead of retrying forever.
			log.Error("appearance: unparsable job payload", "err", err)
			return nil
		}

		sess, err := store.GetVoiceSession(ctx, p.VoiceSessionID)
		if err != nil {
			return fmt.Errorf("appearance: load session %s: %w", p.VoiceSessionID, err)
		}
		campaign, err := store.GetCampaign(ctx, sess.CampaignID)
		if err != nil {
			return fmt.Errorf("appearance: load campaign %s: %w", sess.CampaignID, err)
		}

		nodes, err := store.CampaignNodeNames(ctx, campaign.ID)
		if err != nil {
			return fmt.Errorf("appearance: load node names: %w", err)
		}
		if len(nodes) == 0 {
			return nil
		}
		lines, err := store.SessionLinesForIndexing(ctx, p.VoiceSessionID)
		if err != nil {
			return fmt.Errorf("appearance: load lines: %w", err)
		}
		if len(lines) == 0 {
			return nil
		}

		// The Campaign Language picks the phonetic encoder, so a German campaign
		// matches German mis-hearings. A language with no registered encoder gets
		// nil, which the matcher documents as edit-distance only (ADR-0024).
		var enc address.Encoder
		if campaign.Language != "" {
			if e, ok := encoders.For(campaign.Language); ok {
				enc = e
			}
		}
		cfg := address.NameMatchConfig{}
		names := make([]string, len(nodes))
		for i, n := range nodes {
			names[i] = n.Name
		}
		idx := address.NewNameIndex(cfg, enc, names)
		min := address.MinNameConfidence(cfg)

		var rows []storage.NodeAppearance
		for _, line := range lines {
			for i := range idx.Match(line.Text, min) {
				rows = append(rows, storage.NodeAppearance{
					NodeID:         nodes[i].ID,
					CampaignID:     campaign.ID,
					VoiceSessionID: p.VoiceSessionID,
					LineID:         line.LineID,
					At:             line.At,
				})
			}
		}
		if len(rows) == 0 {
			return nil
		}
		if err := store.RecordNodeAppearances(ctx, rows); err != nil {
			return fmt.Errorf("appearance: record: %w", err)
		}
		log.Info("appearance: indexed session",
			"voice_session_id", p.VoiceSessionID,
			"campaign_id", campaign.ID,
			"lines", len(lines),
			"entries", len(nodes),
			"appearances", len(rows))
		return nil
	}
}

// Compile-time pins: *storage.Store satisfies every narrow store seam this
// package declares, so a drift in a store method fails THIS package's build
// instead of surfacing only at the composition root (CONTRIBUTING: interface
// assertions).
var (
	_ Store = (*storage.Store)(nil)
)
