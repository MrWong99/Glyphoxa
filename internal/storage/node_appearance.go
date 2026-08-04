package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// The Appearances index (#545, ADR-0008 amendment): where a Knowledge Graph Node
// was mentioned in play.
//
// Nothing in this file is prompt-facing, and that is the design rather than an
// omission. Modelling a mention as an Edge would have made it world truth an
// NPC's Hot Context walks (AgentNodeFacts), so "the party talked about the ogre"
// would silently become "the ogre is connected to the party" in an NPC's head.
// The Appearances index is a RETRIEVAL index that lives beside the graph and
// answers one question: where do I go to hear this again.

// NodeAppearance is one recorded mention: a Node named in a committed Transcript
// Line.
type NodeAppearance struct {
	NodeID         uuid.UUID
	CampaignID     uuid.UUID
	VoiceSessionID uuid.UUID
	LineID         string
	At             time.Time
}

// AppearanceHit is one appearance joined to the line that produced it — what the
// entry editor's Appearances list renders and deep-links from.
type AppearanceHit struct {
	VoiceSessionID uuid.UUID
	LineID         string
	At             time.Time
	Who            string
	Kind           string
	Text           string
	// SessionStartedAt lets the UI label which session a mention came from without
	// a second read.
	SessionStartedAt time.Time
}

// RecordNodeAppearances writes a batch of appearances, ignoring ones already
// recorded.
//
// ON CONFLICT DO NOTHING is what makes the indexing job safely re-runnable: a
// retried or replayed job re-derives the same (node, line) pairs and writes
// nothing new, so nobody has to reason about whether it already ran.
func (s *Store) RecordNodeAppearances(ctx context.Context, rows []NodeAppearance) error {
	if len(rows) == 0 {
		return nil
	}
	nodeIDs := make([]uuid.UUID, len(rows))
	campaignIDs := make([]uuid.UUID, len(rows))
	sessionIDs := make([]uuid.UUID, len(rows))
	lineIDs := make([]string, len(rows))
	ats := make([]time.Time, len(rows))
	for i, r := range rows {
		nodeIDs[i] = r.NodeID
		campaignIDs[i] = r.CampaignID
		sessionIDs[i] = r.VoiceSessionID
		lineIDs[i] = r.LineID
		ats[i] = r.At
	}
	if _, err := s.db.Exec(ctx,
		`INSERT INTO node_appearance (node_id, campaign_id, voice_session_id, line_id, at)
		 SELECT * FROM unnest($1::uuid[], $2::uuid[], $3::uuid[], $4::text[], $5::timestamptz[])
		 ON CONFLICT DO NOTHING`,
		nodeIDs, campaignIDs, sessionIDs, lineIDs, ats); err != nil {
		return fmt.Errorf("storage: record node appearances: %w", err)
	}
	return nil
}

// MaxAppearances caps one entry's Appearances list. A long-running campaign
// mentions its central NPC in hundreds of lines, and "the last N times" is the
// question; an unbounded list is a slow read nobody scrolls.
const MaxAppearances = 50

// ListNodeAppearances returns an entry's most recent appearances, newest first.
//
// The join is what makes the list useful: each row carries the line's own text
// and speaker, so the GM reads WHAT was said without following the link, and
// follows it only when they want the surrounding scene.
func (s *Store) ListNodeAppearances(ctx context.Context, campaignID, nodeID uuid.UUID, limit int) ([]AppearanceHit, error) {
	if limit <= 0 || limit > MaxAppearances {
		limit = MaxAppearances
	}
	rows, err := s.db.Query(ctx,
		`SELECT a.voice_session_id, a.line_id, a.at, l.who, l.kind, l.text,
		        COALESCE(v.started_at, a.at)
		   FROM node_appearance a
		   JOIN transcript_line l
		     ON l.voice_session_id = a.voice_session_id AND l.line_id = a.line_id
		   LEFT JOIN voice_sessions v ON v.id = a.voice_session_id
		  WHERE a.node_id = $1 AND a.campaign_id = $2
		  ORDER BY a.at DESC
		  LIMIT $3`, nodeID, campaignID, limit)
	if err != nil {
		return nil, fmt.Errorf("storage: list node appearances %s: %w", nodeID, err)
	}
	defer rows.Close()

	var out []AppearanceHit
	for rows.Next() {
		var h AppearanceHit
		if err := rows.Scan(&h.VoiceSessionID, &h.LineID, &h.At, &h.Who, &h.Kind, &h.Text,
			&h.SessionStartedAt); err != nil {
			return nil, fmt.Errorf("storage: scan node appearance: %w", err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list node appearances %s: %w", nodeID, err)
	}
	return out, nil
}

// SessionAppearance is one session's appearance row, for the Campaign Bundle
// export (#547). It carries the Node id and the Line's own stable key — the two
// things an import has to remap or preserve.
type SessionAppearance struct {
	NodeID uuid.UUID
	LineID string
	At     time.Time
}

// ListSessionAppearances returns every appearance recorded in one Voice Session,
// for the history-flagged bundle export.
func (s *Store) ListSessionAppearances(ctx context.Context, sessionID uuid.UUID) ([]SessionAppearance, error) {
	rows, err := s.db.Query(ctx,
		`SELECT node_id, line_id, at FROM node_appearance
		  WHERE voice_session_id = $1 ORDER BY at, line_id, node_id`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("storage: list session appearances %s: %w", sessionID, err)
	}
	defer rows.Close()
	var out []SessionAppearance
	for rows.Next() {
		var a SessionAppearance
		if err := rows.Scan(&a.NodeID, &a.LineID, &a.At); err != nil {
			return nil, fmt.Errorf("storage: scan session appearance: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list session appearances %s: %w", sessionID, err)
	}
	return out, nil
}

// IndexableLine is one committed Transcript Line the indexer scans.
type IndexableLine struct {
	LineID string
	At     time.Time
	Text   string
}

// SessionLinesForIndexing returns a session's committed Transcript Lines.
//
// Committed only, because that is all transcript_line ever holds: a partial
// never reaches it (ADR-0012/ADR-0040), so "appearances come from committed
// lines" needs no filter here — it is a property of the table.
func (s *Store) SessionLinesForIndexing(ctx context.Context, sessionID uuid.UUID) ([]IndexableLine, error) {
	rows, err := s.db.Query(ctx,
		`SELECT line_id, ts, text FROM transcript_line
		  WHERE voice_session_id = $1 AND text <> ''
		  ORDER BY seq`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("storage: session lines for indexing %s: %w", sessionID, err)
	}
	defer rows.Close()

	var out []IndexableLine
	for rows.Next() {
		var l IndexableLine
		if err := rows.Scan(&l.LineID, &l.At, &l.Text); err != nil {
			return nil, fmt.Errorf("storage: scan indexable line: %w", err)
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: session lines for indexing %s: %w", sessionID, err)
	}
	return out, nil
}

// IndexableNode is one Node the indexer matches against: its id and its name,
// and nothing else. Deliberately no prose — the indexer matches NAMES, and
// hauling every entry's body across for that would be pure waste.
type IndexableNode struct {
	ID   uuid.UUID
	Name string
}

// CampaignNodeNames returns every Node in a Campaign as (id, name).
//
// gm_private entries are INCLUDED. An appearance is GM-facing retrieval, never a
// prompt read: the GM's own secret being mentioned at the table is exactly the
// kind of thing they want to find again, and hiding it here would make the index
// lie about their own campaign.
func (s *Store) CampaignNodeNames(ctx context.Context, campaignID uuid.UUID) ([]IndexableNode, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, name FROM kg_node WHERE campaign_id = $1 AND name <> '' ORDER BY id`, campaignID)
	if err != nil {
		return nil, fmt.Errorf("storage: campaign node names %s: %w", campaignID, err)
	}
	defer rows.Close()

	var out []IndexableNode
	for rows.Next() {
		var n IndexableNode
		if err := rows.Scan(&n.ID, &n.Name); err != nil {
			return nil, fmt.Errorf("storage: scan node name: %w", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: campaign node names %s: %w", campaignID, err)
	}
	return out, nil
}
