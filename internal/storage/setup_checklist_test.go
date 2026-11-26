//go:build integration

package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestDiscordGuildName pins the stored server name (migration 00061): written
// for the linked guild only, kept across a re-link of the SAME guild, dropped
// when the guild changes and cleared on release.
func TestDiscordGuildName(t *testing.T) {
	st := migrated(t)
	ctx := context.Background()
	tenant, err := st.CreateTenant(ctx, "Named")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	const guild, other = "111000000000000001", "222000000000000002"

	// No row at all: nothing to name.
	if ok, err := st.SaveDiscordGuildName(ctx, tenant, guild, "Saltmarsh"); err != nil || ok {
		t.Fatalf("name with no link = (%v, %v), want (false, nil)", ok, err)
	}

	if _, err := st.SaveDiscordGuild(ctx, tenant, guild); err != nil {
		t.Fatalf("SaveDiscordGuild: %v", err)
	}
	if ok, err := st.SaveDiscordGuildName(ctx, tenant, guild, "Saltmarsh"); err != nil || !ok {
		t.Fatalf("name the linked guild = (%v, %v), want (true, nil)", ok, err)
	}
	// A name for a guild that is not the linked one is dropped.
	if ok, err := st.SaveDiscordGuildName(ctx, tenant, other, "Elsewhere"); err != nil || ok {
		t.Fatalf("name another guild = (%v, %v), want (false, nil)", ok, err)
	}
	dep, err := st.GetDeploymentConfig(ctx, tenant)
	if err != nil {
		t.Fatalf("GetDeploymentConfig: %v", err)
	}
	if dep.GuildName != "Saltmarsh" {
		t.Fatalf("GuildName = %q, want Saltmarsh", dep.GuildName)
	}

	// Re-linking the same guild keeps the name; both link writers agree.
	if dep, err = st.SaveDiscordGuild(ctx, tenant, guild); err != nil || dep.GuildName != "Saltmarsh" {
		t.Fatalf("re-link same guild = (%q, %v), want the name kept", dep.GuildName, err)
	}
	if dep, err = st.SaveDiscordChannels(ctx, tenant, guild, "333000000000000003"); err != nil || dep.GuildName != "Saltmarsh" {
		t.Fatalf("channels on same guild = (%q, %v), want the name kept", dep.GuildName, err)
	}

	// Another guild: the old name must not describe it.
	if dep, err = st.SaveDiscordGuild(ctx, tenant, other); err != nil || dep.GuildName != "" {
		t.Fatalf("link another guild = (%q, %v), want the name cleared", dep.GuildName, err)
	}
	if _, err := st.SaveDiscordGuildName(ctx, tenant, other, "Elsewhere"); err != nil {
		t.Fatalf("name other: %v", err)
	}
	if dep, err = st.SaveDiscordChannels(ctx, tenant, guild, "333000000000000003"); err != nil || dep.GuildName != "" {
		t.Fatalf("channels on a new guild = (%q, %v), want the name cleared", dep.GuildName, err)
	}

	// Release clears it.
	if _, err := st.SaveDiscordGuildName(ctx, tenant, guild, "Saltmarsh"); err != nil {
		t.Fatalf("name guild: %v", err)
	}
	dep, err = st.ReleaseDiscordGuild(ctx, tenant, guild)
	if err != nil {
		t.Fatalf("ReleaseDiscordGuild: %v", err)
	}
	if dep.GuildName != "" || dep.GuildID != "" {
		t.Fatalf("after release = (%q, %q), want both empty", dep.GuildID, dep.GuildName)
	}
}

// TestInsertKeylessProviderConfigNeverOverwrites is #712 item 3: activating a
// platform slot inserts a keyless row only when none exists, so a key saved
// in between is never replaced by an empty credential.
func TestInsertKeylessProviderConfigNeverOverwrites(t *testing.T) {
	st := migrated(t)
	ctx := context.Background()
	tenant, err := st.CreateTenant(ctx, "Racer")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	keyless := storage.NewProviderConfig{
		TenantID: tenant, Component: storage.ComponentLLM, Provider: "anthropic", Model: "claude-haiku-5-5",
	}

	row, inserted, err := st.InsertKeylessProviderConfig(ctx, keyless)
	if err != nil || !inserted {
		t.Fatalf("first insert = (%v, %v), want inserted", inserted, err)
	}
	if row.CredentialsLast4 != storage.KeylessLast4 || row.Model != "claude-haiku-5-5" {
		t.Fatalf("inserted row = %q/%q, want keyless with the model", row.CredentialsLast4, row.Model)
	}

	// A key is saved (the concurrent save the activation raced with).
	if _, err := st.UpsertProviderConfigs(ctx, []storage.NewProviderConfig{{
		TenantID: tenant, Component: storage.ComponentLLM, Provider: "anthropic",
		Model: "claude-opus-5-5", CredentialsCiphertext: []byte("sealed"), CredentialsLast4: "abcd",
	}}); err != nil {
		t.Fatalf("UpsertProviderConfigs: %v", err)
	}

	row, inserted, err = st.InsertKeylessProviderConfig(ctx, keyless)
	if err != nil {
		t.Fatalf("second insert: %v", err)
	}
	if inserted {
		t.Fatal("second insert reported inserted, want the existing row kept")
	}
	if row.CredentialsLast4 != "abcd" || string(row.CredentialsCiphertext) != "sealed" || row.Model != "claude-opus-5-5" {
		t.Fatalf("existing row = %q/%q/%q, want the saved key untouched", row.CredentialsLast4, row.CredentialsCiphertext, row.Model)
	}
}

// TestDeleteTenantLLMConfigs removes only the named voice LLM rows of the one
// Tenant; the image row, the chat row and another Tenant's rows stay.
func TestDeleteTenantLLMConfigs(t *testing.T) {
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
	save := func(tenant uuid.UUID, comp storage.Component, provider string) {
		t.Helper()
		if _, err := st.UpsertProviderConfigs(ctx, []storage.NewProviderConfig{{
			TenantID: tenant, Component: comp, Provider: provider,
			CredentialsCiphertext: []byte("sealed"), CredentialsLast4: "wxyz",
		}}); err != nil {
			t.Fatalf("save %s/%s: %v", comp, provider, err)
		}
	}
	save(a, storage.ComponentLLM, "groq")
	save(a, storage.ComponentLLM, "anthropic")
	save(a, storage.ComponentImage, "gemini")
	save(a, storage.ComponentChatLLM, "anthropic")
	save(b, storage.ComponentLLM, "anthropic")

	n, err := st.DeleteTenantLLMConfigs(ctx, a, []string{"anthropic"})
	if err != nil || n != 1 {
		t.Fatalf("delete anthropic = (%d, %v), want (1, nil)", n, err)
	}
	// Idempotent.
	if n, err := st.DeleteTenantLLMConfigs(ctx, a, []string{"anthropic"}); err != nil || n != 0 {
		t.Fatalf("delete again = (%d, %v), want (0, nil)", n, err)
	}

	left := map[string]bool{}
	rows, err := st.ListProviderConfigs(ctx, a)
	if err != nil {
		t.Fatalf("ListProviderConfigs: %v", err)
	}
	for _, r := range rows {
		left[string(r.Component)+"/"+r.Provider] = true
	}
	for _, want := range []string{"llm/groq", "image/gemini", "chat_llm/anthropic"} {
		if !left[want] {
			t.Errorf("row %s is gone, want it kept", want)
		}
	}
	if left["llm/anthropic"] {
		t.Error("llm/anthropic is still there")
	}
	if _, err := st.GetProviderConfigByComponent(ctx, b, storage.ComponentLLM); err != nil {
		t.Errorf("tenant B's row: %v, want it untouched", err)
	}

	if n, err := st.DeleteTenantLLMConfigs(ctx, a, []string{"groq", "anthropic"}); err != nil || n != 1 {
		t.Fatalf("delete both = (%d, %v), want (1, nil)", n, err)
	}
	if _, err := st.GetProviderConfigByComponent(ctx, a, storage.ComponentLLM); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("after deleting every llm row: %v, want ErrNotFound", err)
	}
}

// TestTenantActivePlan reads the active subscription's plan live, and is
// ErrNotFound without one.
func TestTenantActivePlan(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, _ := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)
	if _, err := st.SyncPlans(ctx, billingSpecs(), false); err != nil {
		t.Fatalf("sync: %v", err)
	}

	if _, err := st.TenantActivePlan(ctx, tenantID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("unsubscribed: %v, want ErrNotFound", err)
	}
	if _, err := st.SetTenantPlan(ctx, tenantID, "all-inclusive"); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	plan, err := st.TenantActivePlan(ctx, tenantID)
	if err != nil {
		t.Fatalf("TenantActivePlan: %v", err)
	}
	if plan.Slug != "all-inclusive" || plan.DisplayName != "All Inclusive" || plan.KeySource != "platform" {
		t.Fatalf("plan = %+v, want the all-inclusive row", plan)
	}
	if plan.IncludedUsageUSD == nil || *plan.IncludedUsageUSD != 15 {
		t.Fatalf("included = %v, want 15", plan.IncludedUsageUSD)
	}
	if err := st.EndTenantPlan(ctx, tenantID); err != nil {
		t.Fatalf("end: %v", err)
	}
	if _, err := st.TenantActivePlan(ctx, tenantID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("ended: %v, want ErrNotFound", err)
	}
}
