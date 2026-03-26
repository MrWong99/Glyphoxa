package gateway

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CampaignSummary holds the fields the gateway needs from mgmt.campaigns.
// The gateway reads but never writes this table — ownership stays with the
// web management service.
type CampaignSummary struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tenant_id"`
	Name        string    `json:"name"`
	System      string    `json:"system"`
	Language    string    `json:"language"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// CampaignReader provides read-only access to the mgmt.campaigns table
// managed by the web service. All methods are safe for concurrent use
// (backed by pgxpool).
type CampaignReader struct {
	pool *pgxpool.Pool
}

// NewCampaignReader creates a CampaignReader. It does not run migrations —
// the mgmt schema is owned by the web management service.
func NewCampaignReader(pool *pgxpool.Pool) *CampaignReader {
	return &CampaignReader{pool: pool}
}

// ListForTenant returns all non-deleted campaigns for the given tenant,
// ordered by name. Returns an empty slice (not an error) if the mgmt schema
// or table does not exist, so deployments that haven't run the web service
// migrations degrade gracefully.
func (r *CampaignReader) ListForTenant(ctx context.Context, tenantID string) ([]CampaignSummary, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, tenant_id, name, system, language, description, created_at, updated_at
		FROM mgmt.campaigns
		WHERE tenant_id = $1 AND deleted_at IS NULL
		ORDER BY name
	`, tenantID)
	if err != nil {
		if isUndefinedTable(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("gateway: list campaigns for tenant %q: %w", tenantID, err)
	}
	defer rows.Close()

	var campaigns []CampaignSummary
	for rows.Next() {
		c, scanErr := scanCampaignSummary(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		campaigns = append(campaigns, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("gateway: list campaigns for tenant %q: %w", tenantID, err)
	}
	return campaigns, nil
}

// Get returns a single campaign by ID and tenant, or (zero, false, nil) if not
// found. Returns an error only on unexpected failures. Like [ListForTenant],
// a missing mgmt schema returns (zero, false, nil) rather than an error.
func (r *CampaignReader) Get(ctx context.Context, tenantID, campaignID string) (CampaignSummary, bool, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, name, system, language, description, created_at, updated_at
		FROM mgmt.campaigns
		WHERE id = $1 AND tenant_id = $2 AND deleted_at IS NULL
	`, campaignID, tenantID)

	c, err := scanCampaignSummary(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || isUndefinedTable(err) {
			return CampaignSummary{}, false, nil
		}
		return CampaignSummary{}, false, fmt.Errorf("gateway: get campaign %q for tenant %q: %w", campaignID, tenantID, err)
	}
	return c, true, nil
}

// scanCampaignSummary reads a single row into a CampaignSummary.
func scanCampaignSummary(row pgx.Row) (CampaignSummary, error) {
	var c CampaignSummary
	err := row.Scan(&c.ID, &c.TenantID, &c.Name, &c.System, &c.Language, &c.Description, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return CampaignSummary{}, err
	}
	return c, nil
}

// isUndefinedTable checks whether the error indicates a missing table or schema
// (PostgreSQL error code 42P01 = undefined_table).
func isUndefinedTable(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "42P01"
	}
	return false
}
