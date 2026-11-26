package rpc_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/spend"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// These tests pin the Setup checklist backend (ADR-0004 amendment
// 2026-11-26): GetUsageSummary, removing a voice LLM key, the insert-only
// platform activation (#712 item 3) and the linked server's stored name.

// fakePlans is a scripted active-plan read.
type fakePlans struct {
	plan storage.Plan
	err  error
}

func (f fakePlans) TenantActivePlan(context.Context, uuid.UUID) (storage.Plan, error) {
	return f.plan, f.err
}

// fakeAllowance is a scripted spend.AllowanceReader, driven through the real
// spend.PlanAllowance so the month window is the gate's own.
type fakeAllowance struct {
	included *float64
	month    float64
	from, to time.Time
}

func (f *fakeAllowance) TenantIncludedUsageUSD(context.Context, uuid.UUID) (*float64, error) {
	return f.included, nil
}

func (f *fakeAllowance) TenantMonthUsageUSD(_ context.Context, _ uuid.UUID, from, to time.Time) (float64, error) {
	f.from, f.to = from, to
	return f.month, nil
}

var usageNow = time.Date(2026, 11, 26, 18, 30, 0, 0, time.UTC)

func usageClient(t *testing.T, plans interface {
	TenantActivePlan(context.Context, uuid.UUID) (storage.Plan, error)
}, allowance *fakeAllowance, gate *fakeKeyGate) managementv1connect.ProviderServiceClient {
	t.Helper()
	srv := rpc.NewProviderServer(newFakeProviderStore(), testCipher(t), nil)
	if gate != nil {
		srv.SetKeyEntitlement(gate)
	}
	srv.SetClockForTest(func() time.Time { return usageNow })
	if allowance != nil {
		srv.SetUsageSources(plans, spend.PlanAllowance{Reader: allowance, Now: func() time.Time { return usageNow }})
	} else {
		srv.SetUsageSources(plans, nil)
	}
	client, _ := clientForServer(t, srv)
	return client
}

func usageSummary(t *testing.T, client managementv1connect.ProviderServiceClient) *managementv1.GetUsageSummaryResponse {
	t.Helper()
	resp, err := client.GetUsageSummary(context.Background(), connect.NewRequest(&managementv1.GetUsageSummaryRequest{}))
	if err != nil {
		t.Fatalf("GetUsageSummary: %v", err)
	}
	return resp.Msg
}

func TestGetUsageSummary_WithAllowance(t *testing.T) {
	t.Parallel()
	included := 5.0
	allowance := &fakeAllowance{included: &included, month: 1.25}
	plans := fakePlans{plan: storage.Plan{Slug: "beta-trial", DisplayName: "Beta trial", KeySource: "platform", IncludedUsageUSD: &included}}
	got := usageSummary(t, usageClient(t, plans, allowance, &fakeKeyGate{allowed: true}))

	if got.GetPlanSlug() != "beta-trial" || got.GetPlanName() != "Beta trial" {
		t.Errorf("plan = %q/%q, want beta-trial/Beta trial", got.GetPlanSlug(), got.GetPlanName())
	}
	if !got.GetUsesPlatformKeys() {
		t.Error("uses_platform_keys = false for an entitled tenant")
	}
	if got.IncludedUsd == nil || got.GetIncludedUsd() != 5 || got.GetMonthUsd() != 1.25 {
		t.Errorf("usage = %v of %v, want 1.25 of 5", got.GetMonthUsd(), got.IncludedUsd)
	}
	if got.GetExhausted() {
		t.Error("exhausted with $3.75 left")
	}
	wantReset := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	if !got.GetResetsAt().AsTime().Equal(wantReset) {
		t.Errorf("resets_at = %v, want %v", got.GetResetsAt().AsTime(), wantReset)
	}
	// The month the ledger was summed over is the gate's own window.
	if !allowance.from.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) || !allowance.to.Equal(wantReset) {
		t.Errorf("ledger window = [%v, %v), want November 2026", allowance.from, allowance.to)
	}
}

func TestGetUsageSummary_Exhausted(t *testing.T) {
	t.Parallel()
	included := 5.0
	got := usageSummary(t, usageClient(t, fakePlans{}, &fakeAllowance{included: &included, month: 5.02}, nil))
	if !got.GetExhausted() || got.GetMonthUsd() != 5.02 {
		t.Errorf("exhausted = %v at %v of 5, want true", got.GetExhausted(), got.GetMonthUsd())
	}
}

func TestGetUsageSummary_WithoutAllowance(t *testing.T) {
	t.Parallel()

	t.Run("no allowance reader wired", func(t *testing.T) {
		t.Parallel()
		// The plan row names an allowance, but nothing enforces one here.
		included := 5.0
		plans := fakePlans{plan: storage.Plan{Slug: "beta-trial", DisplayName: "Beta trial", IncludedUsageUSD: &included}}
		got := usageSummary(t, usageClient(t, plans, nil, nil))
		if got.IncludedUsd != nil || got.GetMonthUsd() != 0 || got.GetExhausted() {
			t.Errorf("usage = %+v, want no allowance", got)
		}
		if got.GetPlanName() != "Beta trial" {
			t.Errorf("plan name = %q, want it reported anyway", got.GetPlanName())
		}
		if got.GetResetsAt() == nil {
			t.Error("resets_at missing")
		}
	})

	t.Run("a BYOK plan has no allowance", func(t *testing.T) {
		t.Parallel()
		allowance := &fakeAllowance{}
		plans := fakePlans{plan: storage.Plan{Slug: "byok-free", DisplayName: "Bring your own keys", KeySource: "byok"}}
		got := usageSummary(t, usageClient(t, plans, allowance, &fakeKeyGate{allowed: false}))
		if got.IncludedUsd != nil || got.GetUsesPlatformKeys() {
			t.Errorf("usage = %+v, want own keys and no allowance", got)
		}
		if !allowance.from.IsZero() {
			t.Error("the ledger was read for a tenant with no allowance")
		}
	})

	t.Run("no subscription at all", func(t *testing.T) {
		t.Parallel()
		got := usageSummary(t, usageClient(t, fakePlans{err: storage.ErrNotFound}, &fakeAllowance{}, nil))
		if got.GetPlanSlug() != "" || got.GetPlanName() != "" || got.IncludedUsd != nil {
			t.Errorf("usage = %+v, want an empty plan", got)
		}
	})

	t.Run("a failed plan read is an error", func(t *testing.T) {
		t.Parallel()
		_, err := usageClient(t, fakePlans{err: errors.New("db down")}, nil, nil).
			GetUsageSummary(context.Background(), connect.NewRequest(&managementv1.GetUsageSummaryRequest{}))
		if connect.CodeOf(err) != connect.CodeInternal {
			t.Errorf("err = %v, want CodeInternal", err)
		}
	})
}

func deleteSlot(t *testing.T, client managementv1connect.ProviderServiceClient, slot string) *managementv1.DeleteProviderConfigResponse {
	t.Helper()
	resp, err := client.DeleteProviderConfig(context.Background(), connect.NewRequest(&managementv1.DeleteProviderConfigRequest{Slot: slot}))
	if err != nil {
		t.Fatalf("Delete(%s): %v", slot, err)
	}
	return resp.Msg
}

// A tenant that saved an Anthropic voice key removes it and is back on the
// platform default: effective_llm follows, the health cache is busted, and
// nothing but the voice LLM row is touched.
func TestProviderDeleteVoiceLLM_BackToPlatformDefault(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	srv := rpc.NewProviderServer(store, testCipher(t), nil)
	srv.SetLLMDefault(haikuDefault(t))
	srv.SetEnvLookupForTest(func(k string) string { return bothKeys[k] })
	busted := 0
	srv.SetHealthInvalidator(func(uuid.UUID) { busted++ })
	client, _ := clientForServer(t, srv)

	save(t, client, &managementv1.SaveProviderConfigRequest{Provider: "anthropic", Secret: "test-anthropic-secret-4242", Model: "claude-opus-5-5"})
	save(t, client, &managementv1.SaveProviderConfigRequest{Provider: "elevenlabs", Secret: "test-elevenlabs-secret-7777"})
	if eff, _ := listLLM(t, client); eff.GetPlatformDefault() || eff.GetModel() != "claude-opus-5-5" {
		t.Fatalf("before = %+v, want the tenant's own anthropic row", eff)
	}
	before := busted

	if resp := deleteSlot(t, client, "anthropic"); resp.GetChatModelReset() {
		t.Error("chat_model_reset with no chat choice")
	}
	eff, slots := listLLM(t, client)
	if !eff.GetPlatformDefault() || eff.GetProvider() != "anthropic" || eff.GetModel() != "claude-haiku-4-5" {
		t.Errorf("after = %+v, want the platform default anthropic/claude-haiku-4-5", eff)
	}
	if slots["anthropic"].GetEverSaved() {
		t.Error("the anthropic slot still reads saved")
	}
	if busted != before+1 {
		t.Errorf("health cache busted %d times, want 1", busted-before)
	}
	if _, ok := store.configs["tts|elevenlabs"]; !ok {
		t.Error("removing the voice key took the ElevenLabs row with it")
	}

	// Idempotent, and a no-op busts nothing.
	deleteSlot(t, client, "anthropic")
	if busted != before+1 {
		t.Error("an idempotent delete busted the health cache")
	}
}

// "voice_llm" removes every voice LLM row: an own Groq key AND a keyless
// Anthropic platform choice.
func TestProviderDeleteVoiceLLM_AllSlots(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	client := llmServer(t, store, llmbuild.DeploymentDefault{}, bothKeys, &fakeKeyGate{allowed: true})
	save(t, client, &managementv1.SaveProviderConfigRequest{Provider: "groq", Secret: "test-groq-secret-1111"})
	save(t, client, &managementv1.SaveProviderConfigRequest{Provider: "anthropic", Activate: true})

	deleteSlot(t, client, "voice_llm")
	for k, c := range store.configs {
		if c.Component == storage.ComponentLLM {
			t.Errorf("row %s survived voice_llm", k)
		}
	}
	if eff, _ := listLLM(t, client); !eff.GetPlatformDefault() || eff.GetProvider() != "groq" {
		t.Errorf("after = %+v, want the code default", eff)
	}
}

// Removing the provider key a free-text chat model rode on would land that
// model on the Platform Key, which only runs priced models: the chat model is
// reset to the provider's chat default and the response says so.
func TestProviderDeleteVoiceLLM_ResetsUnpricedChatModel(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	client := llmServer(t, store, llmbuild.DeploymentDefault{}, bothKeys, &fakeKeyGate{allowed: true})
	save(t, client, &managementv1.SaveProviderConfigRequest{Provider: "anthropic", Secret: "test-anthropic-own-key-1234"})
	mustSaveChat(t, client, "anthropic", "claude-fable-5-2", "")

	resp := deleteSlot(t, client, "anthropic")
	if !resp.GetChatModelReset() {
		t.Fatal("chat_model_reset = false, want the unpriced chat model reset")
	}
	rows := chatRowsOf(store)
	if len(rows) != 1 || rows[0].Model != "" || rows[0].Provider != "anthropic" {
		t.Fatalf("chat rows = %+v, want the anthropic choice on its chat default", rows)
	}
	eff := effectiveChat(t, client)
	if eff.GetKeySource() != keyPlatform || eff.GetModel() != llmbuild.DefaultChatModel("anthropic") {
		t.Errorf("effective chat = %+v, want the anthropic chat default on the platform key", eff)
	}
}

// A priced chat model, or one with a chat-only key, is left alone.
func TestProviderDeleteVoiceLLM_KeepsValidChatChoice(t *testing.T) {
	t.Parallel()

	t.Run("priced model", func(t *testing.T) {
		t.Parallel()
		store := newFakeProviderStore()
		client := llmServer(t, store, llmbuild.DeploymentDefault{}, bothKeys, &fakeKeyGate{allowed: true})
		save(t, client, &managementv1.SaveProviderConfigRequest{Provider: "anthropic", Secret: "test-anthropic-own-key-1234"})
		mustSaveChat(t, client, "anthropic", "claude-opus-5-5", "")
		if deleteSlot(t, client, "anthropic").GetChatModelReset() {
			t.Error("a priced chat model was reset")
		}
		if rows := chatRowsOf(store); len(rows) != 1 || rows[0].Model != "claude-opus-5-5" {
			t.Errorf("chat rows = %+v, want claude-opus-5-5 kept", rows)
		}
	})

	t.Run("chat-only key", func(t *testing.T) {
		t.Parallel()
		store := newFakeProviderStore()
		client := llmServer(t, store, llmbuild.DeploymentDefault{}, bothKeys, &fakeKeyGate{allowed: true})
		save(t, client, &managementv1.SaveProviderConfigRequest{Provider: "anthropic", Secret: "test-anthropic-own-key-1234"})
		mustSaveChat(t, client, "anthropic", "claude-fable-5-2", "test-anthropic-chat-key-5678")
		if deleteSlot(t, client, "anthropic").GetChatModelReset() {
			t.Error("a chat model on its own chat key was reset")
		}
		if rows := chatRowsOf(store); len(rows) != 1 || rows[0].Model != "claude-fable-5-2" {
			t.Errorf("chat rows = %+v, want claude-fable-5-2 kept", rows)
		}
	})
}

// #712 item 3: an activation that decided "no row yet" must not clear a key
// saved between that read and its write.
func TestProviderSave_ActivateNeverClearsAConcurrentKey(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	client := llmServer(t, store, llmbuild.DeploymentDefault{}, bothKeys, &fakeKeyGate{allowed: true})
	// The concurrent save lands after the handler read the slot as empty.
	store.beforeInsert = func() {
		store.configs["llm|anthropic"] = storage.ProviderConfig{
			ID: uuid.New(), Component: storage.ComponentLLM, Provider: "anthropic", Model: "claude-opus-5-5",
			CredentialsCiphertext: []byte("sealed"), CredentialsLast4: "4242", UpdatedAt: store.now(),
		}
	}

	cred := save(t, client, &managementv1.SaveProviderConfigRequest{Provider: "anthropic", Activate: true})
	row := store.configs["llm|anthropic"]
	if row.CredentialsLast4 != "4242" || string(row.CredentialsCiphertext) != "sealed" || row.Model != "claude-opus-5-5" {
		t.Fatalf("row after activate = %+v, want the concurrently saved key untouched", row)
	}
	if !cred.GetEverSaved() || cred.GetLast4() != "4242" {
		t.Errorf("returned credential = %+v, want the saved key's view", cred)
	}
}

// The linked server's name, read by the guild-admin proof, is stored at link
// time, listed as guild_name, and cleared on release.
func TestProviderDiscordSettings_StoresGuildName(t *testing.T) {
	t.Parallel()
	store := newFakeProviderStore()
	srv := rpc.NewProviderServer(store, testCipher(t), nil)
	srv.SetEnvBotToken("central-token")
	client, _ := clientForServer(t, srv)
	// After clientForServer, which stubs the proof to a name-less pass.
	srv.SetGuildProofNamedForTest(func(context.Context, string, string, string) (string, error) {
		return "Tides over Saltmarsh", nil
	})
	ctx := context.Background()

	if _, err := client.SaveDiscordSettings(ctx, connect.NewRequest(&managementv1.SaveDiscordSettingsRequest{
		GuildId: strPtr("472093001100"),
	})); err != nil {
		t.Fatalf("link: %v", err)
	}
	list := func() *managementv1.ListProviderConfigsResponse {
		t.Helper()
		resp, err := client.ListProviderConfigs(ctx, connect.NewRequest(&managementv1.ListProviderConfigsRequest{}))
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		return resp.Msg
	}
	if got := list(); got.GetGuildId() != "472093001100" || got.GetGuildName() != "Tides over Saltmarsh" {
		t.Fatalf("after link = %q/%q, want the id and the name", got.GetGuildId(), got.GetGuildName())
	}

	if _, err := client.ReleaseDiscordGuild(ctx, connect.NewRequest(&managementv1.ReleaseDiscordGuildRequest{GuildId: "472093001100"})); err != nil {
		t.Fatalf("release: %v", err)
	}
	if got := list(); got.GetGuildId() != "" || got.GetGuildName() != "" {
		t.Errorf("after release = %q/%q, want both empty", got.GetGuildId(), got.GetGuildName())
	}
}
