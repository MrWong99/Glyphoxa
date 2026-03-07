package config

import (
	"context"
	"testing"
)

func TestLicenseTier_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		tier LicenseTier
		want string
	}{
		{TierShared, "shared"},
		{TierDedicated, "dedicated"},
		{LicenseTier(99), "LicenseTier(99)"},
	}

	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			if got := tc.tier.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseLicenseTier(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input   string
		want    LicenseTier
		wantErr bool
	}{
		{"shared", TierShared, false},
		{"dedicated", TierDedicated, false},
		{"unknown", 0, true},
		{"", 0, true},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			got, err := ParseLicenseTier(tc.input)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParseLicenseTier(%q) error = %v, wantErr %v", tc.input, err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("ParseLicenseTier(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestTenantContext_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{"valid simple", "acme", false},
		{"valid with underscore", "acme_corp", false},
		{"valid with numbers", "tenant42", false},
		{"valid single char", "a", false},
		{"invalid starts with number", "42acme", true},
		{"invalid starts with underscore", "_acme", true},
		{"invalid uppercase", "Acme", true},
		{"invalid spaces", "acme corp", true},
		{"invalid hyphen", "acme-corp", true},
		{"invalid empty", "", true},
		{"invalid sql injection", "acme'; DROP TABLE--", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tenant := TenantContext{TenantID: tc.id}
			err := tenant.Validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestTenantContext_MaxLength(t *testing.T) {
	t.Parallel()

	// 63 chars (max PostgreSQL identifier length) should be valid.
	id63 := "a" + string(make([]byte, 62))
	for i := range id63[1:] {
		id63 = id63[:i+1] + "b" + id63[i+2:]
	}
	// Build a clean 63-char ID.
	buf := make([]byte, 63)
	buf[0] = 'a'
	for i := 1; i < 63; i++ {
		buf[i] = 'b'
	}
	id63 = string(buf)

	tc := TenantContext{TenantID: id63}
	if err := tc.Validate(); err != nil {
		t.Errorf("63-char ID should be valid: %v", err)
	}

	// 64 chars should be invalid.
	id64 := id63 + "c"
	tc2 := TenantContext{TenantID: id64}
	if err := tc2.Validate(); err == nil {
		t.Error("64-char ID should be invalid")
	}
}

func TestWithTenant_TenantFromContext(t *testing.T) {
	t.Parallel()

	tc := TenantContext{
		TenantID:    "acme",
		LicenseTier: TierDedicated,
		CampaignID:  "curse_of_strahd",
		GuildID:     "123456",
	}

	ctx := WithTenant(context.Background(), tc)

	got, ok := TenantFromContext(ctx)
	if !ok {
		t.Fatal("TenantFromContext returned false")
	}
	if got != tc {
		t.Errorf("TenantFromContext = %+v, want %+v", got, tc)
	}
}

func TestTenantFromContext_Missing(t *testing.T) {
	t.Parallel()

	_, ok := TenantFromContext(context.Background())
	if ok {
		t.Error("TenantFromContext should return false for empty context")
	}
}

func TestLocalTenant(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		campaign   string
		wantCampID string
	}{
		{"with campaign", "Curse of Strahd", "Curse of Strahd"},
		{"empty campaign", "", "default"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := LocalTenant(tc.campaign)
			if got.TenantID != "local" {
				t.Errorf("TenantID = %q, want %q", got.TenantID, "local")
			}
			if got.LicenseTier != TierShared {
				t.Errorf("LicenseTier = %v, want TierShared", got.LicenseTier)
			}
			if got.CampaignID != tc.wantCampID {
				t.Errorf("CampaignID = %q, want %q", got.CampaignID, tc.wantCampID)
			}
		})
	}
}
