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
	History    *History    `json:"history,omitempty"`
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
}

// Edge is a knowledge-graph edge referencing node ref keys.
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Type string `json:"type"`
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
