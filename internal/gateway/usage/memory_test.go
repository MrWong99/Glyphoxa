package usage

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoryStore_RecordAndGetUsage(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore()
	ctx := context.Background()
	period := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	// Record some usage.
	err := s.RecordUsage(ctx, "acme", Record{
		Period:       period,
		SessionHours: 2.5,
		LLMTokens:    1000,
		STTSeconds:   60.0,
		TTSChars:     500,
	})
	if err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}

	// Record more usage (should accumulate).
	err = s.RecordUsage(ctx, "acme", Record{
		Period:       period,
		SessionHours: 1.5,
		LLMTokens:    2000,
		STTSeconds:   30.0,
		TTSChars:     300,
	})
	if err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}

	got, err := s.GetUsage(ctx, "acme", period)
	if err != nil {
		t.Fatalf("GetUsage: %v", err)
	}

	if got.SessionHours != 4.0 {
		t.Errorf("SessionHours = %g, want 4.0", got.SessionHours)
	}
	if got.LLMTokens != 3000 {
		t.Errorf("LLMTokens = %d, want 3000", got.LLMTokens)
	}
	if got.STTSeconds != 90.0 {
		t.Errorf("STTSeconds = %g, want 90.0", got.STTSeconds)
	}
	if got.TTSChars != 800 {
		t.Errorf("TTSChars = %d, want 800", got.TTSChars)
	}
}

func TestMemoryStore_GetUsage_NoneRecorded(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore()
	period := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	got, err := s.GetUsage(context.Background(), "unknown", period)
	if err != nil {
		t.Fatalf("GetUsage: %v", err)
	}
	if got.TenantID != "unknown" {
		t.Errorf("TenantID = %q, want %q", got.TenantID, "unknown")
	}
	if got.SessionHours != 0 {
		t.Errorf("SessionHours = %g, want 0", got.SessionHours)
	}
}

func TestMemoryStore_CheckQuota(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		usageHours   float64
		quotaHours   float64
		wantExceeded bool
	}{
		{"under quota", 10.0, 40.0, false},
		{"at quota", 40.0, 40.0, true},
		{"over quota", 50.0, 40.0, true},
		{"unlimited", 999.0, 0, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := NewMemoryStore()
			ctx := context.Background()

			if tc.usageHours > 0 {
				err := s.RecordUsage(ctx, "acme", Record{
					Period:       CurrentPeriod(),
					SessionHours: tc.usageHours,
				})
				if err != nil {
					t.Fatalf("RecordUsage: %v", err)
				}
			}

			err := s.CheckQuota(ctx, "acme", QuotaConfig{MonthlySessionHours: tc.quotaHours})
			if tc.wantExceeded && !errors.Is(err, ErrQuotaExceeded) {
				t.Errorf("CheckQuota = %v, want ErrQuotaExceeded", err)
			}
			if !tc.wantExceeded && err != nil {
				t.Errorf("CheckQuota = %v, want nil", err)
			}
		})
	}
}

func TestMemoryStore_IsolatesTenants(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore()
	ctx := context.Background()
	period := CurrentPeriod()

	_ = s.RecordUsage(ctx, "tenant_a", Record{Period: period, SessionHours: 10})
	_ = s.RecordUsage(ctx, "tenant_b", Record{Period: period, SessionHours: 20})

	a, _ := s.GetUsage(ctx, "tenant_a", period)
	b, _ := s.GetUsage(ctx, "tenant_b", period)

	if a.SessionHours != 10 {
		t.Errorf("tenant_a SessionHours = %g, want 10", a.SessionHours)
	}
	if b.SessionHours != 20 {
		t.Errorf("tenant_b SessionHours = %g, want 20", b.SessionHours)
	}
}

func TestMemoryStore_IsolatesPeriods(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore()
	ctx := context.Background()

	march := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	april := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	_ = s.RecordUsage(ctx, "acme", Record{Period: march, SessionHours: 10})
	_ = s.RecordUsage(ctx, "acme", Record{Period: april, SessionHours: 5})

	m, _ := s.GetUsage(ctx, "acme", march)
	a, _ := s.GetUsage(ctx, "acme", april)

	if m.SessionHours != 10 {
		t.Errorf("march SessionHours = %g, want 10", m.SessionHours)
	}
	if a.SessionHours != 5 {
		t.Errorf("april SessionHours = %g, want 5", a.SessionHours)
	}
}
