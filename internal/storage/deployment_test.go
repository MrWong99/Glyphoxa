//go:build integration

package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestGetTenantIDByGuildID is the interaction→Tenant routing read (#490): a Guild
// snowflake resolves to the Tenant that configured it, ErrNotFound for an unknown
// Guild, and — when two Tenants saved the SAME guild_id — the NEWEST-updated row
// wins (the deterministic authority both the interaction router and the member
// picker agree on).
func TestGetTenantIDByGuildID(t *testing.T) {
	st := migrated(t)
	ctx := context.Background()

	// Unknown guild → ErrNotFound (a fresh DB has no deployment_config rows).
	if _, err := st.GetTenantIDByGuildID(ctx, "999000000000000000"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("GetTenantIDByGuildID(unknown) err = %v, want ErrNotFound", err)
	}

	a, err := st.CreateTenant(ctx, "A")
	if err != nil {
		t.Fatalf("CreateTenant A: %v", err)
	}
	b, err := st.CreateTenant(ctx, "B")
	if err != nil {
		t.Fatalf("CreateTenant B: %v", err)
	}

	// Tenant A configures guild G1 — a clean hit.
	if _, err := st.SaveDiscordChannels(ctx, a, "111000000000000000", "chanA"); err != nil {
		t.Fatalf("SaveDiscordChannels A: %v", err)
	}
	got, err := st.GetTenantIDByGuildID(ctx, "111000000000000000")
	if err != nil {
		t.Fatalf("GetTenantIDByGuildID(G1): %v", err)
	}
	if got != a {
		t.Errorf("GetTenantIDByGuildID(G1) = %s, want tenant A %s", got, a)
	}

	// Both Tenants now claim the SAME guild G-dup. B saves LAST, so B is the
	// newest-wins owner; A's stale losing row must NOT resolve.
	if _, err := st.SaveDiscordChannels(ctx, a, "222000000000000000", "chanA"); err != nil {
		t.Fatalf("SaveDiscordChannels A dup: %v", err)
	}
	if _, err := st.SaveDiscordChannels(ctx, b, "222000000000000000", "chanB"); err != nil {
		t.Fatalf("SaveDiscordChannels B dup: %v", err)
	}
	got, err = st.GetTenantIDByGuildID(ctx, "222000000000000000")
	if err != nil {
		t.Fatalf("GetTenantIDByGuildID(dup): %v", err)
	}
	if got != b {
		t.Errorf("GetTenantIDByGuildID(dup) = %s, want newest-wins tenant B %s", got, b)
	}
}

// TestListTenantOperatorBindings is the per-Tenant GM-identity source (#490): each
// binding pairs a Tenant with its operator's Discord snowflake, so GMIdentity can
// scope GM standing to the owning Tenant instead of deployment-wide. The synthetic
// dev operator is excluded and a suspended operator drops out, matching
// ListTenantOperatorDiscordIDs.
func TestListTenantOperatorBindings(t *testing.T) {
	st := migrated(t)
	ctx := context.Background()

	// Fresh DB: no bindings, no error.
	bindings, err := st.ListTenantOperatorBindings(ctx)
	if err != nil {
		t.Fatalf("ListTenantOperatorBindings(empty): %v", err)
	}
	if len(bindings) != 0 {
		t.Fatalf("empty DB listed %v, want none", bindings)
	}

	// Two operators, each bound to their own Tenant.
	a, err := st.UpsertUser(ctx, storage.UpsertUserParams{DiscordUserID: "111000000000000000"})
	if err != nil {
		t.Fatalf("upsert A: %v", err)
	}
	ta, err := st.ResolveOperatorTenant(ctx, a.ID)
	if err != nil {
		t.Fatalf("bind A: %v", err)
	}
	b, err := st.UpsertUser(ctx, storage.UpsertUserParams{DiscordUserID: "222000000000000000"})
	if err != nil {
		t.Fatalf("upsert B: %v", err)
	}
	tb, err := st.ResolveOperatorTenant(ctx, b.ID)
	if err != nil {
		t.Fatalf("bind B: %v", err)
	}

	bindings, err = st.ListTenantOperatorBindings(ctx)
	if err != nil {
		t.Fatalf("ListTenantOperatorBindings: %v", err)
	}
	got := map[string]string{} // discordUserID -> tenantID
	for _, bd := range bindings {
		got[bd.DiscordUserID] = bd.TenantID.String()
	}
	if got["111000000000000000"] != ta.ID.String() {
		t.Errorf("A bound to %s, want tenant %s", got["111000000000000000"], ta.ID)
	}
	if got["222000000000000000"] != tb.ID.String() {
		t.Errorf("B bound to %s, want tenant %s", got["222000000000000000"], tb.ID)
	}

	// A suspended operator drops out of the source (ADR-0055 revocation reaches GM).
	if err := st.SetUserSuspended(ctx, "111000000000000000", true); err != nil {
		t.Fatalf("SetUserSuspended(A): %v", err)
	}
	bindings, err = st.ListTenantOperatorBindings(ctx)
	if err != nil {
		t.Fatalf("ListTenantOperatorBindings(A suspended): %v", err)
	}
	for _, bd := range bindings {
		if bd.DiscordUserID == "111000000000000000" {
			t.Errorf("suspended A still bound: %v", bd)
		}
	}
}
