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
// Guild. Since the first-registrar-wins unique index (#483), a guild_id can be
// bound by at most ONE Tenant, so the read is unambiguous by construction.
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
}

// TestSaveDiscordChannelsGuildCollision pins the #483 first-registrar-wins guild
// binding (full guild-permission proof is #504): under the old newest-wins read,
// Tenant B saving victim A's guild_id silently rebound the guild — B then read A's
// voice-channel members and hijacked A's command routing (a cross-tenant PII
// leak). Now a save of a guild_id already owned by a DIFFERENT Tenant is rejected
// with ErrGuildTaken (backed by the partial UNIQUE index), while the same Tenant
// re-saving its own guild — and moving to a new, unclaimed guild — stays fine.
func TestSaveDiscordChannelsGuildCollision(t *testing.T) {
	st := migrated(t)
	ctx := context.Background()

	a, err := st.CreateTenant(ctx, "A")
	if err != nil {
		t.Fatalf("CreateTenant A: %v", err)
	}
	b, err := st.CreateTenant(ctx, "B")
	if err != nil {
		t.Fatalf("CreateTenant B: %v", err)
	}

	const guild = "222000000000000000"
	if _, err := st.SaveDiscordChannels(ctx, a, guild, "chanA"); err != nil {
		t.Fatalf("SaveDiscordChannels A (first registrar): %v", err)
	}

	// The victim's guild cannot be rebound by another Tenant.
	if _, err := st.SaveDiscordChannels(ctx, b, guild, "chanB"); !errors.Is(err, storage.ErrGuildTaken) {
		t.Fatalf("SaveDiscordChannels B on A's guild err = %v, want ErrGuildTaken", err)
	}
	got, err := st.GetTenantIDByGuildID(ctx, guild)
	if err != nil {
		t.Fatalf("GetTenantIDByGuildID after rejected steal: %v", err)
	}
	if got != a {
		t.Errorf("guild owner after rejected steal = %s, want first registrar A %s", got, a)
	}

	// The owning Tenant re-saving its own guild (e.g. changing the channel) is fine.
	if _, err := st.SaveDiscordChannels(ctx, a, guild, "chanA2"); err != nil {
		t.Fatalf("SaveDiscordChannels A re-save own guild: %v", err)
	}

	// B binding a DIFFERENT, unclaimed guild is fine; and once A moves off its
	// guild, the freed guild_id becomes claimable again.
	if _, err := st.SaveDiscordChannels(ctx, b, "333000000000000000", "chanB"); err != nil {
		t.Fatalf("SaveDiscordChannels B fresh guild: %v", err)
	}
	if _, err := st.SaveDiscordChannels(ctx, a, "444000000000000000", "chanA3"); err != nil {
		t.Fatalf("SaveDiscordChannels A move guilds: %v", err)
	}
	if _, err := st.SaveDiscordChannels(ctx, b, guild, "chanB2"); err != nil {
		t.Fatalf("SaveDiscordChannels B claim freed guild: %v", err)
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
