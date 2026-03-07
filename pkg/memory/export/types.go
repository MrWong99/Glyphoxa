// Package export provides campaign export and import as .tar.gz archives.
//
// An exported archive has this structure:
//
//	campaign-export-<id>/
//	├── metadata.json          # campaign name, dates, tenant tier, version
//	├── npcs/                  # one YAML per NPC (matches config format)
//	│   ├── greymantle.yaml
//	│   └── bartok.yaml
//	├── knowledge-graph.json   # L3 entities + relationships
//	└── sessions/              # one .txt per session (human-readable)
//	    ├── session-001.txt    # <timestamp> <name>: <text>
//	    └── session-002.txt
//
// L2 semantic chunks are opt-in for Dedicated tier only.
package export

import "time"

// Metadata describes the exported campaign.
type Metadata struct {
	CampaignID  string    `json:"campaign_id"`
	TenantID    string    `json:"tenant_id"`
	LicenseTier string    `json:"license_tier"`
	ExportedAt  time.Time `json:"exported_at"`
	Version     int       `json:"version"` // archive format version
}

// KnowledgeGraphExport holds all entities and relationships for a campaign.
type KnowledgeGraphExport struct {
	Entities      []EntityExport       `json:"entities"`
	Relationships []RelationshipExport `json:"relationships"`
}

// EntityExport is a serialisable representation of a knowledge graph entity.
type EntityExport struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Name       string         `json:"name"`
	Attributes map[string]any `json:"attributes,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

// RelationshipExport is a serialisable representation of a knowledge graph edge.
type RelationshipExport struct {
	SourceID   string           `json:"source_id"`
	TargetID   string           `json:"target_id"`
	RelType    string           `json:"rel_type"`
	Attributes map[string]any   `json:"attributes,omitempty"`
	Provenance ProvenanceExport `json:"provenance"`
	CreatedAt  time.Time        `json:"created_at"`
}

// ProvenanceExport is a serialisable representation of relationship provenance.
type ProvenanceExport struct {
	SessionID   string    `json:"session_id"`
	Timestamp   time.Time `json:"timestamp"`
	Confidence  float64   `json:"confidence"`
	Source      string    `json:"source"`
	DMConfirmed bool      `json:"dm_confirmed"`
}
