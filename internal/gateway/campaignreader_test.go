package gateway

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsUndefinedTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "generic error",
			err:  errors.New("something broke"),
			want: false,
		},
		{
			name: "undefined_table 42P01",
			err:  &pgconn.PgError{Code: "42P01"},
			want: true,
		},
		{
			name: "other pg error",
			err:  &pgconn.PgError{Code: "23505"},
			want: false,
		},
		{
			name: "wrapped undefined_table",
			err:  fmt.Errorf("query failed: %w", &pgconn.PgError{Code: "42P01"}),
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := isUndefinedTable(tt.err)
			if got != tt.want {
				t.Errorf("isUndefinedTable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCampaignSummary_Fields(t *testing.T) {
	t.Parallel()

	// Verify the struct has all expected fields by construction.
	c := CampaignSummary{
		ID:          "camp-1",
		TenantID:    "tenant-1",
		Name:        "Test Campaign",
		System:      "dnd5e",
		Language:    "en",
		Description: "A test campaign",
	}

	if c.ID != "camp-1" {
		t.Errorf("ID = %q, want %q", c.ID, "camp-1")
	}
	if c.Language != "en" {
		t.Errorf("Language = %q, want %q", c.Language, "en")
	}
}

func TestNewCampaignReader(t *testing.T) {
	t.Parallel()

	// NewCampaignReader should not panic with nil pool (construction only).
	r := NewCampaignReader(nil)
	if r == nil {
		t.Fatal("expected non-nil CampaignReader")
	}
}
