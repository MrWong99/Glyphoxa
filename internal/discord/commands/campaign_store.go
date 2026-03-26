package commands

import "context"

// CampaignSummary is a lightweight campaign record used by Discord commands.
// It mirrors the mgmt.campaigns table without importing web-service types.
type CampaignSummary struct {
	ID          string
	Name        string
	System      string
	Description string
}

// CampaignReader provides read-only access to campaigns for a tenant.
// The gateway's CampaignReader (Phase 1) will implement this interface
// by querying the mgmt.campaigns table.
type CampaignReader interface {
	// ListForTenant returns all non-deleted campaigns for the given tenant.
	ListForTenant(ctx context.Context, tenantID string) ([]CampaignSummary, error)

	// GetCampaign returns a single campaign by ID, scoped to the tenant.
	GetCampaign(ctx context.Context, tenantID, campaignID string) (*CampaignSummary, error)
}

// TenantCampaignUpdater persists the active campaign selection for a tenant.
// Implemented by the gateway's AdminStore once campaign_id is persisted
// in the tenants table (Phase 1).
type TenantCampaignUpdater interface {
	// SetActiveCampaign updates the tenant's campaign_id in the database.
	SetActiveCampaign(ctx context.Context, tenantID, campaignID string) error
}

// CampaignWriter creates campaign records in the management database.
// Used by /campaign load to persist uploaded YAML campaigns.
type CampaignWriter interface {
	// CreateCampaign inserts a new campaign and returns its generated ID.
	CreateCampaign(ctx context.Context, tenantID, name, system, description string) (string, error)
}
