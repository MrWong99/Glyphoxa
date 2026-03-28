package npcstore

import "context"

// Store provides CRUD operations for NPC definitions.
// All methods require a tenantID to enforce multi-tenant isolation.
// Implementations must be safe for concurrent use.
type Store interface {
	// Create inserts a new NPC definition. The definition is validated before
	// insertion. The definition's TenantID field must be set. Returns an error
	// if an NPC with the same ID already exists.
	Create(ctx context.Context, def *NPCDefinition) error

	// Get retrieves an NPC definition by ID, scoped to the given tenant and
	// (optionally) campaign. Returns (nil, nil) if not found.
	Get(ctx context.Context, tenantID, id, campaignID string) (*NPCDefinition, error)

	// Update replaces an existing NPC definition. The definition is validated
	// before the update. The definition's TenantID field must be set and is
	// included in the WHERE clause. Returns an error if the NPC is not found.
	Update(ctx context.Context, def *NPCDefinition) error

	// Delete removes an NPC definition by ID, scoped to the given tenant and
	// campaign (defense-in-depth). Deleting a non-existent NPC is not an error.
	Delete(ctx context.Context, tenantID, id, campaignID string) error

	// List returns NPC definitions for the given tenant, optionally filtered
	// by campaign ID. An empty campaignID returns all definitions for the
	// tenant.
	List(ctx context.Context, tenantID, campaignID string) ([]NPCDefinition, error)

	// Upsert creates or replaces an NPC definition (useful for YAML import).
	// The definition's TenantID field must be set. The definition is validated
	// before persistence.
	Upsert(ctx context.Context, def *NPCDefinition) error
}
