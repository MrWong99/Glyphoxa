// Package bundle defines the campaign bundle format (ADR-0053): a versioned,
// gzipped-JSON envelope for exporting and importing a campaign setup.
//
// The structs in this file ARE the secrets-exclusion allowlist (ADR-0053 §2):
// there is deliberately no field for provider_config, deployment_config, users,
// auth sessions, ciphertext, last4, speaker_color, linked_user_id, embeddings,
// embedding_model, or provider FK ids. Never add one — the exporter builds a
// bundle by populating these fields explicitly, never by reflecting over tables.
//
// All entity IDs are opaque string ref keys (§4: the exporter writes the source
// UUID strings; a hand-written bundle may use "n1"). There is no uuid.UUID here.
package bundle

import (
	"encoding/json"
	"time"
)

// Bundle is the top-level envelope written to a .glyphoxa.json.gz file.
type Bundle struct {
	FormatVersion int       `json:"format_version"`
	ExportedAt    time.Time `json:"exported_at"`
	Campaign      Campaign  `json:"campaign"`
}

// Campaign is the exported campaign payload. The Butler is included in Agents;
// more than one Butler agent is invalid.
type Campaign struct {
	Name       string      `json:"name"`
	System     string      `json:"system"`
	Language   string      `json:"language"`
	Agents     []Agent     `json:"agents"`
	Nodes      []Node      `json:"nodes,omitempty"`
	Edges      []Edge      `json:"edges,omitempty"`
	Characters []Character `json:"characters,omitempty"`
	// Maps carry their Pins nested (#538, #547): a Pin has no meaning apart from
	// the Map it sits on, so nesting keeps a hand-written bundle from producing an
	// orphan.
	Maps []Map `json:"maps,omitempty"`
	// Boards are the GM's session shortlists (#543). They reference Node ref keys.
	Boards  []Board  `json:"boards,omitempty"`
	History *History `json:"history,omitempty"`
}

// Agent is an NPC or the Butler. Voice is opaque JSON minus provider bindings.
type Agent struct {
	ID          string          `json:"id"`
	Role        string          `json:"role"`
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Persona     string          `json:"persona,omitempty"`
	Voice       json.RawMessage `json:"voice,omitempty"`
	AddressOnly bool            `json:"address_only,omitempty"`
	Aliases     []string        `json:"aliases,omitempty"`
	Grants      []Grant         `json:"grants,omitempty"`
}

// Grant is a tool grant for an agent.
type Grant struct {
	ToolName string          `json:"tool_name"`
	Config   json.RawMessage `json:"config,omitempty"`
}

// Node is a knowledge-graph node. AgentID links an NPC node to its agent.
type Node struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Name      string `json:"name"`
	Body      string `json:"body,omitempty"`
	GMPrivate bool   `json:"gm_private,omitempty"`
	AgentID   string `json:"agent_id,omitempty"`
	// Aspects are the Node's ordered (key, value) facts, each with its OWN privacy
	// flag (#542). They are part of the entry, not history: an export that dropped
	// them would restore an NPC with its prose and none of its facts.
	Aspects []Aspect `json:"aspects,omitempty"`
	// Tags are the GM's own free-form labels (#543).
	Tags []string `json:"tags,omitempty"`
}

// Aspect is one (key, value) fact on a Node, with its own visibility (#542).
type Aspect struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	GMPrivate bool   `json:"gm_private,omitempty"`
}

// Edge is a knowledge-graph edge referencing node ref keys.
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Type string `json:"type"`
	// Note and Disposition are the relation's texture (#546) — "knows and
	// despises" rather than bare "knows". Both reach an NPC's prompt, so an export
	// that dropped them would restore a flatter world than it left.
	Note        string `json:"note,omitempty"`
	Disposition int    `json:"disposition,omitempty"`
}

// Map is a world map with its Pins (#538, ADR-0060).
//
// ImageBase64 is present only when the export asked for images. That is a flag,
// not a default, for the same reason History is: the blob cap is 32 MiB per
// image, base64 inflates it by a third, and a setup export should stay something
// a person can mail.
type Map struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	WidthPx     int    `json:"width_px"`
	HeightPx    int    `json:"height_px"`
	GMPrivate   bool   `json:"gm_private,omitempty"`
	ParentMapID string `json:"parent_map_id,omitempty"`
	// AnchorNodeID is the Location entry this map DEPICTS, as a Node ref key.
	AnchorNodeID string `json:"anchor_node_id,omitempty"`
	ContentType  string `json:"content_type,omitempty"`
	ImageBase64  string `json:"image_base64,omitempty"`
	Pins         []Pin  `json:"pins,omitempty"`
}

// Pin is a normalized position on a Map, referencing a Node ref key (#538).
type Pin struct {
	NodeID        string  `json:"node_id"`
	X             float64 `json:"x"`
	Y             float64 `json:"y"`
	LabelOverride string  `json:"label_override,omitempty"`
	GMPrivate     bool    `json:"gm_private,omitempty"`
}

// Board is a saved session prep board: a named, ordered shortlist of Node ref
// keys (#543).
type Board struct {
	Name    string   `json:"name"`
	NodeIDs []string `json:"node_ids,omitempty"`
}

// Character is a player character. DiscordUserID is kept verbatim (ADR-0053 §6).
type Character struct {
	Name          string   `json:"name"`
	Aliases       []string `json:"aliases,omitempty"`
	DiscordUserID string   `json:"discord_user_id"`
}

// History is the flag-gated transcript payload (ADR-0053 §1, default off).
type History struct {
	Sessions []Session `json:"sessions"`
}

// Session is a voice session with its transcript lines and recall chunks.
type Session struct {
	ID        string     `json:"id"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	Status    string     `json:"status"`
	LineCount int        `json:"line_count"`
	EndReason *string    `json:"end_reason,omitempty"`
	Lines     []Line     `json:"lines,omitempty"`
	Chunks    []Chunk    `json:"chunks,omitempty"`
	// Appearances are where the campaign's entries were named in this session's
	// lines (#545). They ride the HISTORY flag, not the default export: an
	// appearance is a record of what was said, not part of the campaign's setup —
	// and it is derived, so a destination that re-indexes gets them back anyway.
	Appearances []Appearance `json:"appearances,omitempty"`
}

// Appearance is one recorded mention, referencing a Node ref key and a Line's
// own stable id within its session (#545).
type Appearance struct {
	NodeID string    `json:"node_id"`
	LineID string    `json:"line_id"`
	At     time.Time `json:"at"`
}

// Line is a transcript line.
type Line struct {
	LineID               string    `json:"line_id"`
	Seq                  int64     `json:"seq"`
	Who                  string    `json:"who"`
	Tag                  string    `json:"tag,omitempty"`
	Kind                 string    `json:"kind"`
	TS                   time.Time `json:"ts"`
	Text                 string    `json:"text"`
	SpeakerDiscordUserID string    `json:"speaker_discord_user_id,omitempty"`
}

// Chunk is a recall chunk. Embeddings are never exported (ADR-0053 §3): there is
// no embedding or embedding_model field, and there never will be.
type Chunk struct {
	Content               string    `json:"content"`
	SpeakerDiscordUserIDs []string  `json:"speaker_discord_user_ids,omitempty"`
	ParticipatedAgentIDs  []string  `json:"participated_agent_ids,omitempty"`
	StartedAt             time.Time `json:"started_at"`
}
