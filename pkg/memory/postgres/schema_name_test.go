package postgres

import (
	"strings"
	"testing"
)

func TestNewSchemaName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"valid simple", "public", false},
		{"valid with underscore", "tenant_acme", false},
		{"valid with numbers", "tenant42", false},
		{"valid single char", "a", false},
		{"valid 63 chars", "a" + strings.Repeat("b", 62), false},
		{"invalid 64 chars", "a" + strings.Repeat("b", 63), true},
		{"invalid starts with number", "42tenant", true},
		{"invalid starts with underscore", "_tenant", true},
		{"invalid uppercase", "Public", true},
		{"invalid hyphen", "tenant-acme", true},
		{"invalid empty", "", true},
		{"invalid spaces", "tenant acme", true},
		{"invalid sql injection", "acme'; DROP TABLE--", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sn, err := NewSchemaName(tc.raw)
			if (err != nil) != tc.wantErr {
				t.Fatalf("NewSchemaName(%q) error = %v, wantErr %v", tc.raw, err, tc.wantErr)
			}
			if !tc.wantErr && sn.String() != tc.raw {
				t.Errorf("String() = %q, want %q", sn.String(), tc.raw)
			}
		})
	}
}

func TestSchemaName_TableRef(t *testing.T) {
	t.Parallel()

	sn, err := NewSchemaName("tenant_acme")
	if err != nil {
		t.Fatalf("NewSchemaName: %v", err)
	}

	got := sn.TableRef("session_entries")
	// pgx.Identifier.Sanitize() quotes identifiers.
	want := `"tenant_acme"."session_entries"`
	if got != want {
		t.Errorf("TableRef = %q, want %q", got, want)
	}
}
