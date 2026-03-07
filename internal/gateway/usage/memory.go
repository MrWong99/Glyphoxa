package usage

import (
	"context"
	"sync"
	"time"
)

// Compile-time interface assertion.
var _ Store = (*MemoryStore)(nil)

// MemoryStore is an in-memory usage store for single-process mode and testing.
//
// All methods are safe for concurrent use.
type MemoryStore struct {
	mu      sync.RWMutex
	records map[string]Record // key: "tenantID|YYYY-MM"
}

// NewMemoryStore creates an in-memory usage store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		records: make(map[string]Record),
	}
}

func recordKey(tenantID string, period time.Time) string {
	return tenantID + "|" + period.Format("2006-01")
}

// RecordUsage atomically increments usage counters.
func (s *MemoryStore) RecordUsage(_ context.Context, tenantID string, delta Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := recordKey(tenantID, delta.Period)
	r := s.records[key]
	r.TenantID = tenantID
	r.Period = delta.Period
	r.SessionHours += delta.SessionHours
	r.LLMTokens += delta.LLMTokens
	r.STTSeconds += delta.STTSeconds
	r.TTSChars += delta.TTSChars
	s.records[key] = r
	return nil
}

// GetUsage returns the usage record for a tenant in the given period.
func (s *MemoryStore) GetUsage(_ context.Context, tenantID string, period time.Time) (Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := recordKey(tenantID, period)
	r, ok := s.records[key]
	if !ok {
		return Record{TenantID: tenantID, Period: period}, nil
	}
	return r, nil
}

// CheckQuota checks whether the tenant can start a new session.
func (s *MemoryStore) CheckQuota(ctx context.Context, tenantID string, quota QuotaConfig) error {
	if quota.MonthlySessionHours <= 0 {
		return nil
	}

	period := CurrentPeriod()
	rec, err := s.GetUsage(ctx, tenantID, period)
	if err != nil {
		return err
	}

	if rec.SessionHours >= quota.MonthlySessionHours {
		return ErrQuotaExceeded
	}
	return nil
}
