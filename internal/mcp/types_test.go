package mcp_test

import (
	"testing"

	"github.com/MrWong99/glyphoxa/internal/mcp"
)

func TestBudgetTier_Resolve(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tier mcp.BudgetTier
		want mcp.BudgetTier
	}{
		{"Unset resolves to Fast", mcp.BudgetUnset, mcp.BudgetFast},
		{"Fast stays Fast", mcp.BudgetFast, mcp.BudgetFast},
		{"Standard stays Standard", mcp.BudgetStandard, mcp.BudgetStandard},
		{"Deep stays Deep", mcp.BudgetDeep, mcp.BudgetDeep},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.tier.Resolve()
			if got != tt.want {
				t.Errorf("BudgetTier(%d).Resolve() = %d, want %d", tt.tier, got, tt.want)
			}
		})
	}
}

func TestBudgetTier_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		tier mcp.BudgetTier
		want string
	}{
		{mcp.BudgetUnset, "UNSET"},
		{mcp.BudgetFast, "FAST"},
		{mcp.BudgetStandard, "STANDARD"},
		{mcp.BudgetDeep, "DEEP"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			got := tt.tier.String()
			if got != tt.want {
				t.Errorf("BudgetTier(%d).String() = %q, want %q", tt.tier, got, tt.want)
			}
		})
	}
}

func TestBudgetTier_MaxLatencyMs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		tier mcp.BudgetTier
		want int
	}{
		{mcp.BudgetUnset, 500},
		{mcp.BudgetFast, 500},
		{mcp.BudgetStandard, 1500},
		{mcp.BudgetDeep, 4000},
	}

	for _, tt := range tests {
		t.Run(tt.tier.String(), func(t *testing.T) {
			t.Parallel()
			got := tt.tier.MaxLatencyMs()
			if got != tt.want {
				t.Errorf("BudgetTier(%d).MaxLatencyMs() = %d, want %d", tt.tier, got, tt.want)
			}
		})
	}
}

func TestBudgetTier_IotaOrdering(t *testing.T) {
	t.Parallel()

	// Verify the integer ordering is correct for FilterTools comparison.
	if mcp.BudgetUnset >= mcp.BudgetFast {
		t.Error("BudgetUnset should be less than BudgetFast")
	}
	if mcp.BudgetFast >= mcp.BudgetStandard {
		t.Error("BudgetFast should be less than BudgetStandard")
	}
	if mcp.BudgetStandard >= mcp.BudgetDeep {
		t.Error("BudgetStandard should be less than BudgetDeep")
	}
}
