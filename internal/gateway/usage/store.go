// Package usage provides per-tenant usage tracking and quota enforcement.
// It records resource consumption (session hours, LLM tokens, STT seconds,
// TTS characters) per billing period (monthly) and checks quota limits
// before allowing new sessions to start.
//
// Implementations must be safe for concurrent use.
package usage

import (
	"context"
	"fmt"
	"time"
)

// Record represents a tenant's aggregated usage for a billing period.
type Record struct {
	TenantID     string
	Period       time.Time // first of the month
	SessionHours float64
	LLMTokens    int64
	STTSeconds   float64
	TTSChars     int64
}

// QuotaConfig holds the quota limits for a tenant.
type QuotaConfig struct {
	// MonthlySessionHours is the maximum session hours per month.
	// A value of 0 means unlimited.
	MonthlySessionHours float64
}

// ErrQuotaExceeded is returned when a tenant has exceeded their quota.
var ErrQuotaExceeded = fmt.Errorf("usage: quota exceeded")

// Store provides usage tracking and quota enforcement.
type Store interface {
	// RecordUsage atomically increments usage counters for the current
	// billing period. Uses UPSERT for atomic increment.
	RecordUsage(ctx context.Context, tenantID string, delta Record) error

	// GetUsage returns the usage record for a tenant in the given period.
	// Returns a zero Record (not an error) if no usage has been recorded.
	GetUsage(ctx context.Context, tenantID string, period time.Time) (Record, error)

	// CheckQuota checks whether the tenant can start a new session.
	// Returns ErrQuotaExceeded if the tenant's session hours for the current
	// period are at or above the quota limit. Returns nil if within quota
	// or if quota is unlimited (0).
	CheckQuota(ctx context.Context, tenantID string, quota QuotaConfig) error
}

// CurrentPeriod returns the first of the current month in UTC.
func CurrentPeriod() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
}
