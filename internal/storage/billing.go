package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// SaaS billing foundation (ADR-0054): the Plan catalog, per-Tenant Subscriptions
// with price snapshots, and the durable per-Tenant Usage Ledger. Storage stays a
// faithful persistence layer — catalog validation lives in internal/billing, and
// pricing happens in the ledger sink before rows reach AddUsage.

// ErrPlanArchived is returned by SetTenantPlan for an archived plan slug: an
// archived tier accepts no NEW subscriptions (existing ones keep running).
var ErrPlanArchived = errors.New("storage: plan is archived")

// PlanSpec is the write shape SyncPlans upserts from the operator's catalog file
// (ADR-0054). Limits is raw JSON (validated upstream); nil stores '{}'.
type PlanSpec struct {
	Slug             string
	DisplayName      string
	Description      string
	MonthlyPriceUSD  float64
	KeySource        string // 'byok' | 'platform' (plan_key_source enum)
	IncludedUsageUSD *float64
	Limits           json.RawMessage
}

// Plan is a catalog row.
type Plan struct {
	ID               uuid.UUID
	Slug             string
	DisplayName      string
	Description      string
	MonthlyPriceUSD  float64
	KeySource        string
	IncludedUsageUSD *float64
	Limits           json.RawMessage
	Archived         bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Subscription is a Tenant's binding to a Plan. PlanSlug and MonthlyPriceUSD are
// snapshots taken at subscribe time (revenue history survives catalog edits);
// EndedAt is nil while the subscription is active.
type Subscription struct {
	ID              uuid.UUID
	TenantID        uuid.UUID
	PlanID          uuid.UUID
	PlanSlug        string
	MonthlyPriceUSD float64
	StartedAt       time.Time
	EndedAt         *time.Time
}

// UsageRow is one daily-bucketed usage accumulation the ledger sink flushes
// (ADR-0054). Day is a calendar date (UTC); only its date part is stored.
// EstimatedUSD is priced from the static map at capture time — an ESTIMATE,
// never billing truth (ADR-0046 posture).
type UsageRow struct {
	TenantID        uuid.UUID
	Day             time.Time
	Component       Component
	Provider        string
	Model           string
	LLMInputTokens  int64
	LLMOutputTokens int64
	TTSCharacters   int64
	STTAudioSeconds float64
	EstimatedUSD    float64
}

// PlanSyncResult reports what one SyncPlans call changed.
type PlanSyncResult struct {
	Upserted int
	Archived int
}

// SyncPlans upserts the catalog specs by slug (reviving a previously archived
// slug) and, when archiveMissing is set, archives every plan whose slug is
// absent from specs. Runs in one transaction: a partially applied catalog is
// never visible. Plans are never deleted — subscriptions reference them.
func (s *Store) SyncPlans(ctx context.Context, specs []PlanSpec, archiveMissing bool) (PlanSyncResult, error) {
	var res PlanSyncResult
	err := s.InTx(ctx, func(tx *Store) error {
		slugs := make([]string, 0, len(specs))
		for _, spec := range specs {
			limits := spec.Limits
			if len(limits) == 0 {
				limits = json.RawMessage(`{}`)
			}
			_, err := tx.db.Exec(ctx,
				`INSERT INTO plan (slug, display_name, description, monthly_price_usd,
				                   key_source, included_usage_usd, limits, archived)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, false)
				 ON CONFLICT (slug) DO UPDATE SET
				     display_name = EXCLUDED.display_name,
				     description = EXCLUDED.description,
				     monthly_price_usd = EXCLUDED.monthly_price_usd,
				     key_source = EXCLUDED.key_source,
				     included_usage_usd = EXCLUDED.included_usage_usd,
				     limits = EXCLUDED.limits,
				     archived = false,
				     updated_at = now()`,
				spec.Slug, spec.DisplayName, spec.Description, spec.MonthlyPriceUSD,
				spec.KeySource, spec.IncludedUsageUSD, limits)
			if err != nil {
				return fmt.Errorf("storage: sync plan %q: %w", spec.Slug, err)
			}
			res.Upserted++
			slugs = append(slugs, spec.Slug)
		}
		if archiveMissing {
			tag, err := tx.db.Exec(ctx,
				`UPDATE plan SET archived = true, updated_at = now()
				  WHERE NOT archived AND slug <> ALL($1)`, slugs)
			if err != nil {
				return fmt.Errorf("storage: archive missing plans: %w", err)
			}
			res.Archived = int(tag.RowsAffected())
		}
		return nil
	})
	if err != nil {
		return PlanSyncResult{}, err
	}
	return res, nil
}

// ListPlans returns the full catalog (archived included, flagged), stable-ordered
// by slug.
func (s *Store) ListPlans(ctx context.Context) ([]Plan, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, slug, display_name, description, monthly_price_usd,
		        key_source, included_usage_usd, limits, archived, created_at, updated_at
		   FROM plan ORDER BY slug`)
	if err != nil {
		return nil, fmt.Errorf("storage: list plans: %w", err)
	}
	defer rows.Close()

	var plans []Plan
	for rows.Next() {
		var p Plan
		if err := rows.Scan(&p.ID, &p.Slug, &p.DisplayName, &p.Description,
			&p.MonthlyPriceUSD, &p.KeySource, &p.IncludedUsageUSD, &p.Limits,
			&p.Archived, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("storage: scan plan: %w", err)
		}
		plans = append(plans, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list plans: %w", err)
	}
	return plans, nil
}

// SetTenantPlan subscribes a Tenant to the plan with slug, snapshotting the
// plan's current slug + monthly price onto the new subscription row. Any active
// subscription is ended first (its EndedAt set), so the partial unique index
// keeps at most one active row per tenant. ErrNotFound for an unknown slug or
// tenant; ErrPlanArchived for an archived one.
func (s *Store) SetTenantPlan(ctx context.Context, tenantID uuid.UUID, slug string) (Subscription, error) {
	var sub Subscription
	err := s.InTx(ctx, func(tx *Store) error {
		var planID uuid.UUID
		var price float64
		var archived bool
		err := tx.db.QueryRow(ctx,
			`SELECT id, monthly_price_usd, archived FROM plan WHERE slug = $1`, slug).
			Scan(&planID, &price, &archived)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("storage: load plan %q: %w", slug, err)
		}
		if archived {
			return fmt.Errorf("%w: %q", ErrPlanArchived, slug)
		}

		if _, err := tx.db.Exec(ctx,
			`UPDATE tenant_subscription SET ended_at = now()
			  WHERE tenant_id = $1 AND ended_at IS NULL`, tenantID); err != nil {
			return fmt.Errorf("storage: end active subscription for tenant %s: %w", tenantID, err)
		}

		err = tx.db.QueryRow(ctx,
			`INSERT INTO tenant_subscription (tenant_id, plan_id, plan_slug, monthly_price_usd)
			 VALUES ($1, $2, $3, $4)
			 RETURNING id, tenant_id, plan_id, plan_slug, monthly_price_usd, started_at, ended_at`,
			tenantID, planID, slug, price).
			Scan(&sub.ID, &sub.TenantID, &sub.PlanID, &sub.PlanSlug,
				&sub.MonthlyPriceUSD, &sub.StartedAt, &sub.EndedAt)
		if err != nil {
			// The tenant FK is the only constraint an unknown tenant trips here.
			return fmt.Errorf("storage: subscribe tenant %s to %q: %w", tenantID, slug, err)
		}
		return nil
	})
	if err != nil {
		return Subscription{}, err
	}
	return sub, nil
}

// EndTenantPlan ends a Tenant's active subscription (cancellation). ErrNotFound
// when the tenant has no active subscription.
func (s *Store) EndTenantPlan(ctx context.Context, tenantID uuid.UUID) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE tenant_subscription SET ended_at = now()
		  WHERE tenant_id = $1 AND ended_at IS NULL`, tenantID)
	if err != nil {
		return fmt.Errorf("storage: end subscription for tenant %s: %w", tenantID, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ActiveSubscription returns a Tenant's active subscription, ErrNotFound when
// none (an unsubscribed tenant — the BYOK self-host default).
func (s *Store) ActiveSubscription(ctx context.Context, tenantID uuid.UUID) (Subscription, error) {
	var sub Subscription
	err := s.db.QueryRow(ctx,
		`SELECT id, tenant_id, plan_id, plan_slug, monthly_price_usd, started_at, ended_at
		   FROM tenant_subscription
		  WHERE tenant_id = $1 AND ended_at IS NULL`, tenantID).
		Scan(&sub.ID, &sub.TenantID, &sub.PlanID, &sub.PlanSlug,
			&sub.MonthlyPriceUSD, &sub.StartedAt, &sub.EndedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Subscription{}, ErrNotFound
	}
	if err != nil {
		return Subscription{}, fmt.Errorf("storage: active subscription for tenant %s: %w", tenantID, err)
	}
	return sub, nil
}

// AddUsage upsert-accumulates ledger rows: an existing (tenant, day, component,
// provider, model) bucket has the quantities and estimate ADDED, a new bucket is
// inserted. Idempotence is NOT promised — the flush path must not double-send —
// but ordering is irrelevant and rows commute, so concurrent sessions of one
// tenant simply accumulate.
func (s *Store) AddUsage(ctx context.Context, usageRows []UsageRow) error {
	if len(usageRows) == 0 {
		return nil
	}
	return s.InTx(ctx, func(tx *Store) error {
		for _, r := range usageRows {
			_, err := tx.db.Exec(ctx,
				`INSERT INTO usage_ledger (tenant_id, day, component, provider, model,
				                           llm_input_tokens, llm_output_tokens,
				                           tts_characters, stt_audio_seconds, estimated_usd)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
				 ON CONFLICT (tenant_id, day, component, provider, model) DO UPDATE SET
				     llm_input_tokens = usage_ledger.llm_input_tokens + EXCLUDED.llm_input_tokens,
				     llm_output_tokens = usage_ledger.llm_output_tokens + EXCLUDED.llm_output_tokens,
				     tts_characters = usage_ledger.tts_characters + EXCLUDED.tts_characters,
				     stt_audio_seconds = usage_ledger.stt_audio_seconds + EXCLUDED.stt_audio_seconds,
				     estimated_usd = usage_ledger.estimated_usd + EXCLUDED.estimated_usd,
				     updated_at = now()`,
				r.TenantID, r.Day, r.Component, r.Provider, r.Model,
				r.LLMInputTokens, r.LLMOutputTokens, r.TTSCharacters,
				r.STTAudioSeconds, r.EstimatedUSD)
			if err != nil {
				return fmt.Errorf("storage: add usage for tenant %s: %w", r.TenantID, err)
			}
		}
		return nil
	})
}

// TenantBillingLine is one tenant's row in the billing report window: the active
// or overlapping subscription snapshot(s) plus the ledger's summed estimated
// cost. A tenant that switched plans mid-window appears once per subscription
// (revenue is per subscription row); usage is attached to the FIRST line only so
// summing the report never double-counts cost.
type TenantBillingLine struct {
	TenantID        uuid.UUID
	TenantName      string
	PlanSlug        string  // empty: no subscription overlapped the window
	MonthlyPriceUSD float64 // 0 when PlanSlug is empty
	EstimatedUSD    float64
	LLMInputTokens  int64
	LLMOutputTokens int64
	TTSCharacters   int64
	STTAudioSeconds float64
}

// BillingReport aggregates revenue and estimated cost per tenant over [from, to)
// (ADR-0054): every subscription overlapping the window contributes its monthly
// price snapshot un-prorated (label it as such when surfacing), and the usage
// ledger contributes summed quantities + estimated USD. Tenants with usage but
// no subscription (BYOK) appear with an empty PlanSlug.
func (s *Store) BillingReport(ctx context.Context, from, to time.Time) ([]TenantBillingLine, error) {
	rows, err := s.db.Query(ctx,
		`WITH usage_sums AS (
		     SELECT tenant_id,
		            SUM(estimated_usd) AS estimated_usd,
		            SUM(llm_input_tokens) AS llm_input_tokens,
		            SUM(llm_output_tokens) AS llm_output_tokens,
		            SUM(tts_characters) AS tts_characters,
		            SUM(stt_audio_seconds) AS stt_audio_seconds
		       FROM usage_ledger
		      WHERE day >= $1::date AND day < $2::date
		      GROUP BY tenant_id
		 ), subs AS (
		     SELECT tenant_id, plan_slug, monthly_price_usd, started_at,
		            ROW_NUMBER() OVER (PARTITION BY tenant_id ORDER BY started_at) AS rn
		       FROM tenant_subscription
		      WHERE started_at < $2 AND (ended_at IS NULL OR ended_at >= $1)
		 )
		 SELECT t.id, t.name,
		        COALESCE(s.plan_slug, ''), COALESCE(s.monthly_price_usd, 0),
		        CASE WHEN COALESCE(s.rn, 1) = 1 THEN COALESCE(u.estimated_usd, 0) ELSE 0 END,
		        CASE WHEN COALESCE(s.rn, 1) = 1 THEN COALESCE(u.llm_input_tokens, 0) ELSE 0 END,
		        CASE WHEN COALESCE(s.rn, 1) = 1 THEN COALESCE(u.llm_output_tokens, 0) ELSE 0 END,
		        CASE WHEN COALESCE(s.rn, 1) = 1 THEN COALESCE(u.tts_characters, 0) ELSE 0 END,
		        CASE WHEN COALESCE(s.rn, 1) = 1 THEN COALESCE(u.stt_audio_seconds, 0) ELSE 0 END
		   FROM tenant t
		   LEFT JOIN subs s ON s.tenant_id = t.id
		   LEFT JOIN usage_sums u ON u.tenant_id = t.id
		  WHERE s.tenant_id IS NOT NULL OR u.tenant_id IS NOT NULL
		  ORDER BY t.name, t.id, s.started_at NULLS FIRST`,
		from, to)
	if err != nil {
		return nil, fmt.Errorf("storage: billing report: %w", err)
	}
	defer rows.Close()

	var lines []TenantBillingLine
	for rows.Next() {
		var l TenantBillingLine
		if err := rows.Scan(&l.TenantID, &l.TenantName, &l.PlanSlug, &l.MonthlyPriceUSD,
			&l.EstimatedUSD, &l.LLMInputTokens, &l.LLMOutputTokens,
			&l.TTSCharacters, &l.STTAudioSeconds); err != nil {
			return nil, fmt.Errorf("storage: scan billing line: %w", err)
		}
		lines = append(lines, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: billing report: %w", err)
	}
	return lines, nil
}

// ListTenantsWithPlan lists every tenant with its active plan slug (empty when
// unsubscribed) — the operator's `billing tenants` view for finding tenant ids.
func (s *Store) ListTenantsWithPlan(ctx context.Context) ([]TenantPlanRow, error) {
	rows, err := s.db.Query(ctx,
		`SELECT t.id, t.name, t.created_at, COALESCE(s.plan_slug, '')
		   FROM tenant t
		   LEFT JOIN tenant_subscription s
		          ON s.tenant_id = t.id AND s.ended_at IS NULL
		  ORDER BY t.created_at, t.id`)
	if err != nil {
		return nil, fmt.Errorf("storage: list tenants: %w", err)
	}
	defer rows.Close()

	var out []TenantPlanRow
	for rows.Next() {
		var r TenantPlanRow
		if err := rows.Scan(&r.ID, &r.Name, &r.CreatedAt, &r.PlanSlug); err != nil {
			return nil, fmt.Errorf("storage: scan tenant: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list tenants: %w", err)
	}
	return out, nil
}

// TenantPlanRow is one line of ListTenantsWithPlan.
type TenantPlanRow struct {
	ID        uuid.UUID
	Name      string
	CreatedAt time.Time
	PlanSlug  string // empty: no active subscription
}
